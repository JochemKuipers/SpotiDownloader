package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	spotifyAuthorizeURL = "https://accounts.spotify.com/authorize"
	spotifyMeURL        = "https://api.spotify.com/v1/me"
	defaultCallbackHost = "127.0.0.1:3000"
)

// Scopes needed to read the user's library and playlists.
const spotifyScopes = "user-library-read playlist-read-private playlist-read-collaborative"

// spotifyWorkerCount controls concurrent page fetches for large lists of tracks.
const spotifyWorkerCount = 8

// spotifyTokenStore keeps the access/refresh tokens and expiry.
type spotifyTokenStore struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// SpotifyAuthStatus is returned to the frontend to describe the current auth state.
type SpotifyAuthStatus struct {
	Authenticated bool   `json:"authenticated"`
	DisplayName   string `json:"display_name"`
	UserID        string `json:"user_id"`
	AvatarURL     string `json:"avatar_url"`
	ExpiresAt     int64  `json:"expires_at"`
	Scope         string `json:"scope"`
}

// SpotifyLoginResponse wraps the authorization URL that the frontend should open.
type SpotifyLoginResponse struct {
	URL string `json:"url"`
}

// PlaylistSummary contains lightweight playlist info for UI listing.
type PlaylistSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	TracksTotal int    `json:"tracks_total"`
	ImageURL    string `json:"image_url"`
	IsPublic    bool   `json:"is_public"`
}

// PlaylistWithTracks bundles playlist metadata with its tracks for downloading.
type PlaylistWithTracks struct {
	Playlist PlaylistSummary      `json:"playlist"`
	Tracks   []AlbumTrackMetadata `json:"tracks"`
}

type spotifyAuthManager struct {
	mu        sync.Mutex
	tokens    *spotifyTokenStore
	profile   *spotifyUserProfile
	verifier  string
	state     string
	redirect  string
	server    *http.Server
	loginWait chan error
}

var authManager = newSpotifyAuthManager()

func newSpotifyAuthManager() *spotifyAuthManager {
	m := &spotifyAuthManager{}
	_ = m.loadTokensFromDisk() // ignore errors here; handled lazily later
	return m
}

// StartSpotifyLogin initializes PKCE, spins a tiny callback server, and returns the auth URL.
func StartSpotifyLogin(ctx context.Context) (SpotifyLoginResponse, error) {
	return authManager.startLogin(ctx)
}

// GetSpotifyAuthStatus returns the cached auth status, refreshing tokens if needed.
func GetSpotifyAuthStatus(ctx context.Context) (SpotifyAuthStatus, error) {
	return authManager.status(ctx)
}

// LogoutSpotify clears stored tokens and profile information.
func LogoutSpotify() error {
	return authManager.logout()
}

// FetchUserPlaylists returns the user's playlists (private and collaborative included).
func FetchUserPlaylists(ctx context.Context) ([]PlaylistSummary, error) {
	return authManager.fetchUserPlaylists(ctx)
}

// FetchUserSavedTracks returns all liked songs as AlbumTrackMetadata slices.
func FetchUserSavedTracks(ctx context.Context) ([]AlbumTrackMetadata, error) {
	return authManager.fetchUserSavedTracks(ctx)
}

// FetchUserPlaylistTracks returns the tracks of a specific playlist.
func FetchUserPlaylistTracks(ctx context.Context, playlistID string) (*PlaylistWithTracks, error) {
	return authManager.fetchPlaylistWithTracks(ctx, playlistID)
}

// spotifyUserProfile holds the subset of /me we care about.
type spotifyUserProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Images      []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (m *spotifyAuthManager) startLogin(ctx context.Context) (SpotifyLoginResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close any existing server to avoid port conflicts.
	if m.server != nil {
		_ = m.server.Close()
		m.server = nil
	}

	// Prefer fixed callback host:port for easier whitelist; fall back to random port if busy
	ln, err := net.Listen("tcp", defaultCallbackHost)
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return SpotifyLoginResponse{}, fmt.Errorf("failed to open callback listener: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	m.redirect = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	verifier, err := generateCodeVerifier()
	if err != nil {
		ln.Close()
		return SpotifyLoginResponse{}, err
	}
	m.verifier = verifier
	m.state, err = randomString(32)
	if err != nil {
		ln.Close()
		return SpotifyLoginResponse{}, err
	}

	authURL := buildAuthorizeURL(m.redirect, m.state, verifier)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", m.handleCallback)

	srv := &http.Server{Handler: mux}
	m.server = srv
	m.loginWait = make(chan error, 1)

	go func() {
		_ = srv.Serve(ln)
	}()

	// Background watcher to stop server once context ends.
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	return SpotifyLoginResponse{URL: authURL}, nil
}

func (m *spotifyAuthManager) handleCallback(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop server after handling to avoid port leaking.
	defer func() {
		if m.server != nil {
			_ = m.server.Close()
		}
		m.server = nil
	}()

	query := r.URL.Query()
	state := query.Get("state")
	code := query.Get("code")
	if state == "" || code == "" {
		http.Error(w, "Invalid response from Spotify", http.StatusBadRequest)
		m.pushLoginError(errors.New("missing state or code"))
		return
	}

	if state != m.state {
		http.Error(w, "State mismatch", http.StatusBadRequest)
		m.pushLoginError(errors.New("state mismatch"))
		return
	}

	tokenResp, err := exchangeCodeForToken(code, m.redirect, m.verifier)
	if err != nil {
		http.Error(w, "Failed to exchange code", http.StatusInternalServerError)
		m.pushLoginError(err)
		return
	}

	if err := m.saveTokens(tokenResp); err != nil {
		http.Error(w, "Failed to persist token", http.StatusInternalServerError)
		m.pushLoginError(err)
		return
	}

	profile, err := m.fetchProfileLocked(r.Context())
	if err != nil {
		http.Error(w, "Failed to fetch profile", http.StatusInternalServerError)
		m.pushLoginError(err)
		return
	}
	m.profile = profile

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<html><body><p>Spotify login successful. You can close this window.</p></body></html>`)
	m.pushLoginError(nil)
}

func (m *spotifyAuthManager) pushLoginError(err error) {
	select {
	case m.loginWait <- err:
	default:
	}
}

func (m *spotifyAuthManager) status(ctx context.Context) (SpotifyAuthStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tokens == nil {
		if err := m.loadTokensFromDisk(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SpotifyAuthStatus{}, err
		}
	}

	if m.tokens == nil {
		return SpotifyAuthStatus{Authenticated: false}, nil
	}

	if err := m.ensureFreshTokenLocked(ctx); err != nil {
		return SpotifyAuthStatus{}, err
	}

	if m.profile == nil {
		prof, err := m.fetchProfileLocked(ctx)
		if err != nil {
			return SpotifyAuthStatus{}, err
		}
		m.profile = prof
	}

	avatar := ""
	if m.profile != nil && len(m.profile.Images) > 0 {
		avatar = m.profile.Images[0].URL
	}

	return SpotifyAuthStatus{
		Authenticated: true,
		DisplayName:   m.profile.DisplayName,
		UserID:        m.profile.ID,
		AvatarURL:     avatar,
		ExpiresAt:     m.tokens.ExpiresAt,
		Scope:         m.tokens.Scope,
	}, nil
}

func (m *spotifyAuthManager) logout() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tokens = nil
	m.profile = nil
	tokenPath, err := spotifyTokenPath()
	if err == nil {
		_ = os.Remove(tokenPath)
	}
	return nil
}

func (m *spotifyAuthManager) ensureFreshTokenLocked(_ context.Context) error {
	if m.tokens == nil {
		return errors.New("no spotify token available")
	}

	// Refresh if expiring in <30s.
	if time.Now().Unix()+30 < m.tokens.ExpiresAt {
		return nil
	}

	if m.tokens.RefreshToken == "" {
		return errors.New("missing refresh token")
	}

	refreshed, err := refreshAccessToken(m.tokens.RefreshToken)
	if err != nil {
		return err
	}
	return m.saveTokens(refreshed)
}

func (m *spotifyAuthManager) saveTokens(tokens *spotifyTokenStore) error {
	m.tokens = tokens
	path, err := spotifyTokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func (m *spotifyAuthManager) loadTokensFromDisk() error {
	path, err := spotifyTokenPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var tokens spotifyTokenStore
	if err := json.Unmarshal(data, &tokens); err != nil {
		return err
	}
	m.tokens = &tokens
	return nil
}

func (m *spotifyAuthManager) fetchProfileLocked(ctx context.Context) (*spotifyUserProfile, error) {
	if m.tokens == nil {
		return nil, errors.New("not authenticated")
	}
	client := NewSpotifyMetadataClient()
	if err := m.ensureFreshTokenLocked(ctx); err != nil {
		return nil, err
	}
	var profile spotifyUserProfile
	if err := client.getJSON(ctx, spotifyMeURL, m.tokens.AccessToken, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (m *spotifyAuthManager) fetchUserPlaylists(ctx context.Context) ([]PlaylistSummary, error) {
	m.mu.Lock()
	if m.tokens == nil {
		m.mu.Unlock()
		return nil, errors.New("not authenticated")
	}
	if err := m.ensureFreshTokenLocked(ctx); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	token := m.tokens.AccessToken
	m.mu.Unlock()

	client := NewSpotifyMetadataClient()
	playlistsURL := "https://api.spotify.com/v1/me/playlists?limit=50"
	var all []PlaylistSummary

	for playlistsURL != "" {
		var page struct {
			Items []struct {
				ID     string  `json:"id"`
				Name   string  `json:"name"`
				Public bool    `json:"public"`
				Images []image `json:"images"`
				Tracks struct {
					Total int `json:"total"`
				} `json:"tracks"`
				Owner struct {
					DisplayName string `json:"display_name"`
					ID          string `json:"id"`
				} `json:"owner"`
			} `json:"items"`
			Next string `json:"next"`
		}

		if err := client.getJSON(ctx, playlistsURL, token, &page); err != nil {
			return nil, fmt.Errorf("fetch playlists page: %w", err)
		}

		for _, p := range page.Items {
			cover := ""
			if len(p.Images) > 0 {
				cover = p.Images[0].URL
			}
			all = append(all, PlaylistSummary{
				ID:          p.ID,
				Name:        p.Name,
				Owner:       p.Owner.DisplayName,
				TracksTotal: p.Tracks.Total,
				ImageURL:    cover,
				IsPublic:    p.Public,
			})
		}
		playlistsURL = page.Next
	}

	return all, nil
}

func (m *spotifyAuthManager) fetchUserSavedTracks(ctx context.Context) ([]AlbumTrackMetadata, error) {
	m.mu.Lock()
	if m.tokens == nil {
		m.mu.Unlock()
		return nil, errors.New("not authenticated")
	}
	if err := m.ensureFreshTokenLocked(ctx); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	token := m.tokens.AccessToken
	m.mu.Unlock()

	client := NewSpotifyMetadataClient()
	limit := 50
	firstURL := fmt.Sprintf("https://api.spotify.com/v1/me/tracks?limit=%d&offset=0", limit)

	var firstPage struct {
		Items []struct {
			Track *trackFull `json:"track"`
		} `json:"items"`
		Next  string `json:"next"`
		Total int    `json:"total"`
	}

	if err := client.getJSON(ctx, firstURL, token, &firstPage); err != nil {
		return nil, fmt.Errorf("fetch saved tracks page: %w", err)
	}

	tracks := make([]AlbumTrackMetadata, 0, firstPage.Total)
	tracks = append(tracks, convertSavedTrackItems(firstPage.Items)...)

	if firstPage.Total <= len(firstPage.Items) {
		return tracks, nil
	}

	offsets := make([]int, 0)
	for offset := limit; offset < firstPage.Total; offset += limit {
		offsets = append(offsets, offset)
	}

	pageFetcher := func(offset int) ([]AlbumTrackMetadata, error) {
		pageURL := fmt.Sprintf("https://api.spotify.com/v1/me/tracks?limit=%d&offset=%d", limit, offset)
		var page struct {
			Items []struct {
				Track *trackFull `json:"track"`
			} `json:"items"`
		}
		if err := client.getJSON(ctx, pageURL, token, &page); err != nil {
			return nil, fmt.Errorf("fetch saved tracks page offset %d: %w", offset, err)
		}
		return convertSavedTrackItems(page.Items), nil
	}

	results, err := runTrackPageWorkers(ctx, offsets, pageFetcher)
	if err != nil {
		return nil, err
	}

	for _, page := range results {
		tracks = append(tracks, page.tracks...)
	}

	return tracks, nil
}

func (m *spotifyAuthManager) fetchPlaylistWithTracks(ctx context.Context, playlistID string) (*PlaylistWithTracks, error) {
	m.mu.Lock()
	if m.tokens == nil {
		m.mu.Unlock()
		return nil, errors.New("not authenticated")
	}
	if err := m.ensureFreshTokenLocked(ctx); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	token := m.tokens.AccessToken
	m.mu.Unlock()

	client := NewSpotifyMetadataClient()
	var playlistInfo struct {
		ID     string  `json:"id"`
		Name   string  `json:"name"`
		Public bool    `json:"public"`
		Images []image `json:"images"`
		Owner  struct {
			DisplayName string `json:"display_name"`
		} `json:"owner"`
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	}

	metaURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s", playlistID)
	if err := client.getJSON(ctx, metaURL, token, &playlistInfo); err != nil {
		return nil, err
	}

	limit := 100
	firstURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/tracks?limit=%d&offset=0", playlistID, limit)
	var firstPage struct {
		Items []struct {
			Track *trackFull `json:"track"`
		} `json:"items"`
		Total int `json:"total"`
	}

	if err := client.getJSON(ctx, firstURL, token, &firstPage); err != nil {
		return nil, err
	}

	tracks := make([]AlbumTrackMetadata, 0, playlistInfo.Tracks.Total)
	tracks = append(tracks, convertSavedTrackItems(firstPage.Items)...)

	if playlistInfo.Tracks.Total > len(firstPage.Items) {
		offsets := make([]int, 0)
		for offset := limit; offset < playlistInfo.Tracks.Total; offset += limit {
			offsets = append(offsets, offset)
		}

		pageFetcher := func(offset int) ([]AlbumTrackMetadata, error) {
			pageURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/tracks?limit=%d&offset=%d", playlistID, limit, offset)
			var page struct {
				Items []struct {
					Track *trackFull `json:"track"`
				} `json:"items"`
			}
			if err := client.getJSON(ctx, pageURL, token, &page); err != nil {
				return nil, fmt.Errorf("fetch playlist tracks offset %d: %w", offset, err)
			}
			return convertSavedTrackItems(page.Items), nil
		}

		results, err := runTrackPageWorkers(ctx, offsets, pageFetcher)
		if err != nil {
			return nil, err
		}
		for _, page := range results {
			tracks = append(tracks, page.tracks...)
		}
	}

	cover := ""
	if len(playlistInfo.Images) > 0 {
		cover = playlistInfo.Images[0].URL
	}

	summary := PlaylistSummary{
		ID:          playlistInfo.ID,
		Name:        playlistInfo.Name,
		Owner:       playlistInfo.Owner.DisplayName,
		TracksTotal: playlistInfo.Tracks.Total,
		ImageURL:    cover,
		IsPublic:    playlistInfo.Public,
	}

	return &PlaylistWithTracks{
		Playlist: summary,
		Tracks:   tracks,
	}, nil
}

// convertSavedTrackItems converts trackFull items while skipping nil tracks.
func convertSavedTrackItems(items []struct {
	Track *trackFull `json:"track"`
}) []AlbumTrackMetadata {
	converted := make([]AlbumTrackMetadata, 0, len(items))
	for _, item := range items {
		if item.Track != nil {
			converted = append(converted, convertTrackToAlbumTrack(*item.Track))
		}
	}
	return converted
}

type trackPageResult struct {
	offset int
	tracks []AlbumTrackMetadata
	err    error
}

// runTrackPageWorkers fetches paginated track pages concurrently with a fixed worker pool.
func runTrackPageWorkers(ctx context.Context, offsets []int, fetch func(offset int) ([]AlbumTrackMetadata, error)) ([]trackPageResult, error) {
	if len(offsets) == 0 {
		return nil, nil
	}

	workerCount := spotifyWorkerCount
	if workerCount > len(offsets) {
		workerCount = len(offsets)
	}

	jobs := make(chan int)
	results := make(chan trackPageResult)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for offset := range jobs {
				if ctx.Err() != nil {
					results <- trackPageResult{offset: offset, err: ctx.Err()}
					continue
				}
				tracks, err := fetch(offset)
				if err != nil {
					results <- trackPageResult{offset: offset, err: err}
					continue
				}
				results <- trackPageResult{offset: offset, tracks: tracks}
			}
		}()
	}

	go func() {
		for _, offset := range offsets {
			jobs <- offset
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	collected := make([]trackPageResult, 0, len(offsets))
	var firstErr error
	for res := range results {
		if res.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed fetching tracks at offset %d: %w", res.offset, res.err)
		}
		if res.tracks != nil {
			collected = append(collected, res)
		}
	}

	if firstErr == nil && ctx.Err() != nil {
		firstErr = ctx.Err()
	}

	if firstErr != nil {
		return nil, firstErr
	}

	sort.Slice(collected, func(i, j int) bool { return collected[i].offset < collected[j].offset })
	return collected, nil
}

func convertTrackToAlbumTrack(track trackFull) AlbumTrackMetadata {
	artists := make([]string, 0, len(track.Artists))
	for _, a := range track.Artists {
		artists = append(artists, a.Name)
	}
	artistNames := strings.Join(artists, ", ")

	albumArtists := make([]string, 0, len(track.Album.Artists))
	for _, a := range track.Album.Artists {
		albumArtists = append(albumArtists, a.Name)
	}
	albumArtistNames := strings.Join(albumArtists, ", ")

	cover := ""
	if len(track.Album.Images) > 0 {
		cover = track.Album.Images[0].URL
	}

	artistData := make([]ArtistSimple, 0, len(track.Artists))
	for _, a := range track.Artists {
		artistData = append(artistData, ArtistSimple{ID: a.ID, Name: a.Name, ExternalURL: a.ExternalURL.Spotify})
	}

	artistID := ""
	artistURL := ""
	if len(track.Artists) > 0 {
		artistID = track.Artists[0].ID
		artistURL = track.Artists[0].ExternalURL.Spotify
	}

	return AlbumTrackMetadata{
		SpotifyID:   track.ID,
		Artists:     artistNames,
		Name:        track.Name,
		AlbumName:   track.Album.Name,
		AlbumArtist: albumArtistNames,
		DurationMS:  track.DurationMS,
		Images:      cover,
		ReleaseDate: track.Album.ReleaseDate,
		TrackNumber: track.TrackNumber,
		TotalTracks: track.Album.TotalTracks,
		DiscNumber:  track.DiscNumber,
		ExternalURL: track.ExternalURL.Spotify,
		ISRC:        track.ExternalID.ISRC,
		AlbumType:   track.Album.AlbumType,
		AlbumID:     track.Album.ID,
		AlbumURL:    track.Album.ExternalURL.Spotify,
		ArtistID:    artistID,
		ArtistURL:   artistURL,
		ArtistsData: artistData,
	}
}

func exchangeCodeForToken(code, redirectURI, verifier string) (*spotifyTokenStore, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("code_verifier", verifier)

	req, err := http.NewRequest(http.MethodPost, spotifyTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	// Set Basic Auth header with client credentials
	clientID := spotifyClientID()
	clientSecret := spotifyClientSecret()
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("missing spotify client credentials")
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &spotifyTokenStore{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
		Scope:        tokenResp.Scope,
		TokenType:    tokenResp.TokenType,
	}, nil
}

func refreshAccessToken(refreshToken string) (*spotifyTokenStore, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)

	req, err := http.NewRequest(http.MethodPost, spotifyTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	// Set Basic Auth header with client credentials
	clientID := spotifyClientID()
	clientSecret := spotifyClientSecret()
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("missing spotify client credentials")
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed: %s", string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		TokenType    string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	if tokenResp.RefreshToken == "" {
		tokenResp.RefreshToken = refreshToken
	}

	return &spotifyTokenStore{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
		Scope:        tokenResp.Scope,
		TokenType:    tokenResp.TokenType,
	}, nil
}

func buildAuthorizeURL(redirectURI, state, verifier string) string {
	challenge := codeChallenge(verifier)
	params := url.Values{}
	params.Set("client_id", spotifyClientID())
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)
	params.Set("scope", spotifyScopes)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("show_dialog", "true")
	return fmt.Sprintf("%s?%s", spotifyAuthorizeURL, params.Encode())
}

func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateCodeVerifier() (string, error) {
	return randomString(64)
}

func randomString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:length], nil
}

func spotifyTokenPath() (string, error) {
	dir, err := getSpotiDownloaderDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spotify_oauth.json"), nil
}

// spotifyClientID returns the decoded client ID string.
func spotifyClientID() string {
	if custom, err := GetSpotifyClientID(); err == nil && custom != "" {
		return custom
	}
	if decoded, err := base64.StdEncoding.DecodeString("NWY1NzNjOTYyMDQ5NGJhZTg3ODkwYzBmMDhhNjAyOTM="); err == nil {
		return string(decoded)
	}
	return ""
}

// GetSpotifyClientID returns custom client ID if set, otherwise empty string.
func GetSpotifyClientID() (string, error) {
	path, err := spotifyClientIDPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SetSpotifyClientID persists a custom Spotify client ID (empty clears it).
func SetSpotifyClientID(id string) error {
	id = strings.TrimSpace(id)
	path, err := spotifyClientIDPath()
	if err != nil {
		return err
	}
	if id == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id), 0600)
}

func spotifyClientIDPath() (string, error) {
	dir, err := getSpotiDownloaderDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spotify_client_id"), nil
}

// spotifyClientSecret returns the decoded client secret string.
func spotifyClientSecret() string {
	if custom, err := GetSpotifyClientSecret(); err == nil && custom != "" {
		return custom
	}
	// Fallback to hardcoded client secret (same as in spotify_metadata.go)
	if decoded, err := base64.StdEncoding.DecodeString("MjEyNDc2ZDliMGYzNDcyZWFhNzYyZDkwYjE5YjBiYTg="); err == nil {
		return string(decoded)
	}
	return ""
}

// GetSpotifyClientSecret returns custom client secret if set, otherwise empty string.
func GetSpotifyClientSecret() (string, error) {
	path, err := spotifyClientSecretPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SetSpotifyClientSecret persists a custom Spotify client secret (empty clears it).
func SetSpotifyClientSecret(secret string) error {
	secret = strings.TrimSpace(secret)
	path, err := spotifyClientSecretPath()
	if err != nil {
		return err
	}
	if secret == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(secret), 0600)
}

func spotifyClientSecretPath() (string, error) {
	dir, err := getSpotiDownloaderDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spotify_client_secret"), nil
}
