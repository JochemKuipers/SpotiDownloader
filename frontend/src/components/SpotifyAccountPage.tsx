import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { DownloadIcon, LibraryIcon, Loader2, LogOut, Music, Pause, Play, RefreshCw, Square, UserRound } from "lucide-react";
import { useSpotifyAccount } from "@/hooks/useSpotifyAccount";
import type { TrackMetadata, PlaylistSummary } from "@/types/api";
import { toastWithSound as toast } from "@/lib/toast-with-sound";

interface DownloadApi {
    handleDownloadAll: (tracks: TrackMetadata[], playlistName?: string, isAlbum?: boolean) => Promise<void>;
    isDownloading: boolean;
    isPaused: boolean;
    downloadProgress: number;
    downloadingTrack: string | null;
    currentDownloadInfo: { name: string; artists: string } | null;
    handleStopDownload: () => void;
    handlePauseDownload: () => void;
    handleResumeDownload: () => void;
}

interface SpotifyAccountPageProps {
    download: DownloadApi;
    spotify: ReturnType<typeof useSpotifyAccount>;
}

export function SpotifyAccountPage({ download, spotify }: SpotifyAccountPageProps) {
    const {
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
    } = spotify;

    const [downloadingLiked, setDownloadingLiked] = useState(false);
    const [downloadingPlaylists, setDownloadingPlaylists] = useState(false);
    const [downloadingPlaylistId, setDownloadingPlaylistId] = useState<string | null>(null);

    const summary = useMemo(() => {
        const totalTracksInPlaylists = playlists.reduce((acc, p) => acc + (p.tracks_total || 0), 0);
        return {
            playlists: playlists.length,
            playlistTracks: totalTracksInPlaylists,
            savedTracks: savedTracks.length,
        };
    }, [playlists, savedTracks]);

    const handleDownloadLiked = async () => {
        if (!savedTracks.length) {
            toast.error("No liked songs fetched yet");
            return;
        }
        setDownloadingLiked(true);
        try {
            await download.handleDownloadAll(savedTracks, "Liked Songs", false);
        } finally {
            setDownloadingLiked(false);
        }
    };

    const handleDownloadPlaylists = async () => {
        if (!playlists.length) {
            toast.error("No playlists fetched yet");
            return;
        }
        setDownloadingPlaylists(true);
        try {
            for (const playlist of playlists) {
                await handleDownloadSinglePlaylist(playlist);
            }
        } finally {
            setDownloadingPlaylists(false);
        }
    };

    const handleDownloadSinglePlaylist = async (playlist: PlaylistSummary) => {
        setDownloadingPlaylistId(playlist.id);
        try {
            const data = await fetchPlaylistTracks(playlist.id);
            if (!data || !data.tracks?.length) {
                toast.error(`No tracks found for ${playlist.name}`);
                return;
            }
            await download.handleDownloadAll(data.tracks, playlist.name, false);
        } finally {
            setDownloadingPlaylistId(null);
        }
    };

    const renderHeader = () => {
        if (loadingStatus) {
            return (
                <div className="flex items-center gap-2 text-muted-foreground">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    Checking Spotify status...
                </div>
            );
        }

        if (!authStatus?.authenticated) {
            return (
                <div className="flex items-center justify-between">
                    <div className="space-y-1">
                        <h1 className="text-2xl font-bold">Connect your Spotify account</h1>
                        <p className="text-sm text-muted-foreground">Sign in to fetch your playlists and liked songs.</p>
                    </div>
                    <Button onClick={login} disabled={loginInProgress} className="gap-2">
                        {loginInProgress ? <Loader2 className="h-4 w-4 animate-spin" /> : <UserRound className="h-4 w-4" />}
                        Connect Spotify
                    </Button>
                </div>
            );
        }

        return (
            <div className="flex items-center justify-between">
                <div className="space-y-1">
                    <h1 className="text-2xl font-bold flex items-center gap-2">
                        <LibraryIcon className="h-5 w-5" />
                        Spotify Library
                    </h1>
                    <div className="flex items-center gap-2 text-sm text-muted-foreground">
                        <span>{authStatus.display_name || "Authenticated"}</span>
                    </div>
                </div>
                <div className="flex items-center gap-2">
                    <Button variant="secondary" onClick={fetchLibrary} disabled={libraryLoading} className="gap-2">
                        {libraryLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
                        Refresh Library
                    </Button>
                    <Button variant="outline" onClick={disconnect} className="gap-2">
                        <LogOut className="h-4 w-4" />
                        Disconnect
                    </Button>
                </div>
            </div>
        );
    };

    const renderStats = () => {
        if (!authStatus?.authenticated) return null;

        return (
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                <Card>
                    <CardHeader>
                        <CardTitle>Playlists</CardTitle>
                        <CardDescription>Includes private and collaborative playlists</CardDescription>
                    </CardHeader>
                    <CardContent className="flex items-center justify-between">
                        <div>
                            <div className="text-3xl font-bold">{summary.playlists}</div>
                            <p className="text-sm text-muted-foreground">{summary.playlistTracks} total tracks</p>
                        </div>
                        <Music className="h-10 w-10 text-muted-foreground" />
                    </CardContent>
                </Card>
                <Card>
                    <CardHeader>
                        <CardTitle>Liked Songs</CardTitle>
                        <CardDescription>Your saved tracks</CardDescription>
                    </CardHeader>
                    <CardContent className="flex items-center justify-between">
                        <div className="text-3xl font-bold">{summary.savedTracks}</div>
                        <DownloadIcon className="h-10 w-10 text-muted-foreground" />
                    </CardContent>
                </Card>
                <Card>
                    <CardHeader>
                        <CardTitle>Download Status</CardTitle>
                        <CardDescription>
                            {download.isDownloading
                                ? `Downloading ${download.currentDownloadInfo?.name || "..."}`
                                : "Idle"}
                        </CardDescription>
                    </CardHeader>
                    <CardContent>
                        <div className="text-3xl font-bold">{download.downloadProgress}%</div>
                    </CardContent>
                </Card>
            </div>
        );
    };

    const renderLibrary = () => {
        if (!authStatus?.authenticated) {
            return (
                <Card>
                    <CardHeader>
                        <CardTitle>Not connected</CardTitle>
                        <CardDescription>Connect Spotify to view your library.</CardDescription>
                    </CardHeader>
                </Card>
            );
        }

        return (
            <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
                <Card className="lg:col-span-1">
                    <CardHeader>
                        <CardTitle>Actions</CardTitle>
                        <CardDescription>Download everything in one click.</CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-3">
                        <Button
                            className="w-full gap-2"
                            onClick={handleDownloadLiked}
                            disabled={downloadingLiked || download.isDownloading || !savedTracks.length}
                        >
                            {downloadingLiked ? <Loader2 className="h-4 w-4 animate-spin" /> : <DownloadIcon className="h-4 w-4" />}
                            Download Liked Songs
                        </Button>
                        <Button
                            variant="secondary"
                            className="w-full gap-2"
                            onClick={handleDownloadPlaylists}
                            disabled={downloadingPlaylists || download.isDownloading || !playlists.length}
                        >
                            {downloadingPlaylists ? <Loader2 className="h-4 w-4 animate-spin" /> : <LibraryIcon className="h-4 w-4" />}
                            Download All Playlists
                        </Button>
                        <div className="flex gap-2">
                            <Button
                                variant="outline"
                                className="flex-1 gap-2"
                                onClick={download.isPaused ? download.handleResumeDownload : download.handlePauseDownload}
                                disabled={!download.isDownloading}
                            >
                                {download.isPaused ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
                                {download.isPaused ? "Resume" : "Pause"}
                            </Button>
                            <Button
                                variant="destructive"
                                className="flex-1 gap-2"
                                onClick={download.handleStopDownload}
                                disabled={!download.isDownloading}
                            >
                                <Square className="h-4 w-4" />
                                Stop
                            </Button>
                        </div>
                        <p className="text-xs text-muted-foreground">
                            Downloads run sequentially to avoid hitting Spotify rate limits.
                        </p>
                    </CardContent>
                </Card>

                <Card className="lg:col-span-2">
                    <CardHeader>
                        <CardTitle>Your Playlists</CardTitle>
                        <CardDescription>Private playlists included when authenticated.</CardDescription>
                    </CardHeader>
                    <CardContent className="space-y-3 max-h-105 overflow-y-auto pr-2">
                        {!playlists.length && <p className="text-sm text-muted-foreground">No playlists fetched yet.</p>}
                        {playlists.map((p: PlaylistSummary) => (
                            <div key={p.id} className="flex items-center justify-between py-2 border-b border-border last:border-b-0">
                                <div>
                                    <div className="font-medium">{p.name}</div>
                                    <div className="text-xs text-muted-foreground">
                                        {p.tracks_total} tracks • {p.owner || "You"}
                                    </div>
                                </div>
                                <div className="flex items-center gap-2">
                                    <Badge variant={p.is_public ? "outline" : "secondary"}>{p.is_public ? "Public" : "Private"}</Badge>
                                    <Button
                                        size="sm"
                                        variant="outline"
                                        className="gap-2"
                                        onClick={() => handleDownloadSinglePlaylist(p)}
                                        disabled={download.isDownloading || downloadingPlaylistId === p.id}
                                    >
                                        {downloadingPlaylistId === p.id ? (
                                            <Loader2 className="h-4 w-4 animate-spin" />
                                        ) : (
                                            <DownloadIcon className="h-4 w-4" />
                                        )}
                                        Download
                                    </Button>
                                </div>
                            </div>
                        ))}
                    </CardContent>
                </Card>
            </div>
        );
    };

    return (
        <div className="space-y-6">
            {renderHeader()}
            <Separator />
            {renderStats()}
            {renderLibrary()}
        </div>
    );
}
