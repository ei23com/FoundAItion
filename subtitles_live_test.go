package main

import (
	"os"
	"strings"
	"testing"
)

// Live-Test gegen ein echtes YouTube-Video mit automatischer Synchronisierung
// ("dubbed-auto"): dort antworten die reinen Sprachcodes (en, de) mit HTTP 429
// und nur die Originalspuren (en-orig, de-orig) liefern Text. Vor der Korrektur
// brach yt-dlp beim ersten 429 ab und lieferte gar keine Untertitel.
//
// Nur mit YT_LIVE_TEST=1 und vorhandenem yt-dlp:
//
//	YT_LIVE_TEST=1 YTDLP_PATH=$PWD/yt-dlp_linux go test -run TestLiveYouTubeDubbedVideo .
func TestLiveYouTubeDubbedVideo(t *testing.T) {
	if os.Getenv("YT_LIVE_TEST") == "" {
		t.Skip("setze YT_LIVE_TEST=1")
	}
	url := os.Getenv("YT_LIVE_URL")
	if url == "" {
		// Gemeldeter Fall: Video mit 20 automatischen Synchronfassungen.
		url = "https://www.youtube.com/watch?v=a4vfv4EMgYs"
	}
	cfg := loadConfig()
	cfg.YouTubeSubtitleMethod = "yt-dlp"
	app := &App{cfg: cfg}

	transcript, err := app.extractYouTubeSubtitles(url)
	if err != nil {
		t.Fatalf("extractYouTubeSubtitles(%s): %v", url, err)
	}
	if strings.TrimSpace(transcript) == "" {
		t.Fatalf("kein Transkript für %s (Originalspuren wurden nicht geladen)", url)
	}
	if len(transcript) < 2000 {
		t.Errorf("Transkript verdächtig kurz (%d Zeichen)", len(transcript))
	}
	if !strings.HasPrefix(transcript, "00:00:") {
		t.Errorf("unerwartetes Format: %q", transcript[:min(60, len(transcript))])
	}
	t.Logf("Transkript: %d Zeichen, Anfang: %q", len(transcript), transcript[:min(80, len(transcript))])
}
