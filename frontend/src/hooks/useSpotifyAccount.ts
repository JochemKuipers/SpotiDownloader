import { useEffect, useRef, useState } from "react";
import {
  startSpotifyLogin,
  getSpotifyAuthStatus,
  getSpotifyPlaylists,
  getSpotifySavedTracks,
  getSpotifyPlaylistTracks,
  logoutSpotifyAccount,
} from "@/lib/api";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { openExternal } from "@/lib/utils";
import type { PlaylistSummary, PlaylistWithTracks, SpotifyAuthStatus, TrackMetadata } from "@/types/api";
import { logger } from "@/lib/logger";

export function useSpotifyAccount() {
  const [authStatus, setAuthStatus] = useState<SpotifyAuthStatus>({ authenticated: false });
  const [loadingStatus, setLoadingStatus] = useState(true);
  const [loginInProgress, setLoginInProgress] = useState(false);
  const [playlists, setPlaylists] = useState<PlaylistSummary[]>([]);
  const [savedTracks, setSavedTracks] = useState<TrackMetadata[]>([]);
  const [libraryLoading, setLibraryLoading] = useState(false);
  const pollTimer = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    refreshStatus();
    return () => {
      if (pollTimer.current) {
        clearInterval(pollTimer.current);
      }
    };
  }, []);

  const refreshStatus = async () => {
    try {
      logger.info("[spotify] checking auth status...");
      const status = await getSpotifyAuthStatus();
      setAuthStatus(status);
      logger.success(`[spotify] status: ${status.authenticated ? "authenticated" : "not authenticated"}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      console.error("Failed to fetch Spotify status", error);
      // Avoid noisy toasts when running without Wails runtime (e.g., port 5173 dev server)
      if (!message.toLowerCase().includes("wails runtime")) {
        toast.error(`Failed to fetch Spotify status: ${message}`);
      }
      logger.error(`[spotify] status fetch failed: ${message}`);
    } finally {
      setLoadingStatus(false);
    }
  };

  const login = async () => {
    try {
      setLoginInProgress(true);
      logger.info("[spotify] starting login with PKCE");
      const resp = await startSpotifyLogin();
      if (resp?.url) {
        logger.debug(`[spotify] opening auth url: ${resp.url}`);
        openExternal(resp.url);
      }

      // Poll for completion for up to 2 minutes
      if (pollTimer.current) {
        clearInterval(pollTimer.current);
      }
      let attempts = 0;
      pollTimer.current = setInterval(async () => {
        attempts += 1;
        try {
          const status = await getSpotifyAuthStatus();
          if (status?.authenticated) {
            setAuthStatus(status);
            toast.success("Spotify account connected");
            if (pollTimer.current) clearInterval(pollTimer.current);
          }
        } catch (err) {
          console.error("Status poll failed", err);
        }
        if (attempts > 40 && pollTimer.current) {
          clearInterval(pollTimer.current);
        }
      }, 3000);
    } catch (error) {
      console.error("Failed to start Spotify login", error);
      toast.error("Failed to start Spotify login");
      logger.error(`[spotify] login start failed: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setLoginInProgress(false);
    }
  };

  const disconnect = async () => {
    try {
      await logoutSpotifyAccount();
      setAuthStatus({ authenticated: false });
      setPlaylists([]);
      setSavedTracks([]);
      toast.info("Disconnected Spotify account");
      logger.info("[spotify] disconnected");
    } catch (error) {
      console.error("Failed to disconnect", error);
      toast.error("Failed to disconnect Spotify");
      logger.error(`[spotify] disconnect failed: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  const fetchLibrary = async () => {
    setLibraryLoading(true);
    try {
      logger.info("[spotify] fetching library (playlists + liked tracks)...");
      const [pls, liked] = await Promise.all([
        getSpotifyPlaylists(),
        getSpotifySavedTracks(),
      ]);
      setPlaylists(pls || []);
      setSavedTracks(liked || []);
      logger.success(`[spotify] library fetched: ${pls?.length || 0} playlists, ${liked?.length || 0} liked tracks`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      console.error("Failed to fetch library", error);
      toast.error(`Failed to fetch library: ${message}`);
      logger.error(`[spotify] library fetch failed: ${message}`);
    } finally {
      setLibraryLoading(false);
    }
  };

  const fetchPlaylistTracks = async (playlistID: string): Promise<PlaylistWithTracks | null> => {
    try {
      logger.info(`[spotify] fetching playlist tracks for ${playlistID}...`);
      const data = await getSpotifyPlaylistTracks(playlistID);
      logger.success(`[spotify] fetched playlist ${playlistID} with ${data?.tracks?.length || 0} tracks`);
      return data as PlaylistWithTracks;
    } catch (error) {
      console.error("Failed to fetch playlist tracks", error);
      toast.error("Failed to fetch playlist tracks");
      logger.error(`[spotify] playlist tracks failed: ${error instanceof Error ? error.message : String(error)}`);
      return null;
    }
  };

  useEffect(() => {
    if (authStatus?.authenticated) {
      fetchLibrary();
    }
     
  }, [authStatus?.authenticated]);

  return {
    authStatus,
    loadingStatus,
    login,
    loginInProgress,
    disconnect,
    playlists,
    savedTracks,
    fetchLibrary,
    fetchPlaylistTracks,
    libraryLoading,
    refreshStatus,
  };
}
