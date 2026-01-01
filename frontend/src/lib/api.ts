import type {
  SpotifyMetadataResponse,
  DownloadRequest,
  DownloadResponse,
  HealthResponse,
  LyricsDownloadRequest,
  LyricsDownloadResponse,
  CoverDownloadRequest,
  CoverDownloadResponse,
} from "@/types/api";
import { GetSpotifyMetadata, DownloadTrack, DownloadLyrics, DownloadCover } from "../../wailsjs/go/main/App";
import { main } from "../../wailsjs/go/models";

function getWailsApp(): any | null {
  if (typeof window === "undefined") return null;
  return (window as any)?.go?.main?.App || null;
}

// Safe wrappers that surface a clear error when the Wails runtime isn't present (e.g., standalone Vite dev server).
const StartSpotifyLogin = () => {
  const app = getWailsApp();
  if (!app?.StartSpotifyLogin) throw new Error("Wails runtime not available");
  return app.StartSpotifyLogin();
};
const GetSpotifyAuthStatus = () => {
  const app = getWailsApp();
  if (!app?.GetSpotifyAuthStatus) throw new Error("Wails runtime not available");
  return app.GetSpotifyAuthStatus();
};
const LogoutSpotifyAccount = () => {
  const app = getWailsApp();
  if (!app?.LogoutSpotifyAccount) throw new Error("Wails runtime not available");
  return app.LogoutSpotifyAccount();
};
const GetSpotifyPlaylists = () => {
  const app = getWailsApp();
  if (!app?.GetSpotifyPlaylists) throw new Error("Wails runtime not available");
  return app.GetSpotifyPlaylists();
};
const GetSpotifySavedTracks = () => {
  const app = getWailsApp();
  if (!app?.GetSpotifySavedTracks) throw new Error("Wails runtime not available");
  return app.GetSpotifySavedTracks();
};
const GetSpotifyPlaylistTracks = (id: string) => {
  const app = getWailsApp();
  if (!app?.GetSpotifyPlaylistTracks) throw new Error("Wails runtime not available");
  return app.GetSpotifyPlaylistTracks(id);
};

export async function fetchSpotifyMetadata(
  url: string,
  batch: boolean = true,
  delay: number = 1.0,
  timeout: number = 300.0
): Promise<SpotifyMetadataResponse> {
  const req = new main.SpotifyMetadataRequest({
    url,
    batch,
    delay,
    timeout,
  });

  const jsonString = await GetSpotifyMetadata(req);
  return JSON.parse(jsonString);
}

export async function downloadTrack(
  request: DownloadRequest
): Promise<DownloadResponse> {
  const req = new main.DownloadRequest(request);
  return await DownloadTrack(req);
}

export async function downloadLyrics(
  request: LyricsDownloadRequest
): Promise<LyricsDownloadResponse> {
  const req = new main.LyricsDownloadRequest(request);
  return await DownloadLyrics(req);
}

export async function downloadCover(
  request: CoverDownloadRequest
): Promise<CoverDownloadResponse> {
  const req = new main.CoverDownloadRequest(request);
  return await DownloadCover(req);
}

export async function checkHealth(): Promise<HealthResponse> {
  // For Wails, we can just return a simple health check
  // since the app is running locally
  return {
    status: "ok",
    time: new Date().toISOString(),
  };
}

export async function startSpotifyLogin() {
  return StartSpotifyLogin();
}

export async function getSpotifyAuthStatus() {
  return GetSpotifyAuthStatus();
}

export async function logoutSpotifyAccount() {
  return LogoutSpotifyAccount();
}

export async function getSpotifyPlaylists() {
  return GetSpotifyPlaylists();
}

export async function getSpotifySavedTracks() {
  return GetSpotifySavedTracks();
}

export async function getSpotifyPlaylistTracks(id: string) {
  return GetSpotifyPlaylistTracks(id);
}
