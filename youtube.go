package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ─── YouTube-Erkennung ───────────────────────────────────────────────────────

// youtubeRe erkennt YouTube-URLs (youtube.com, youtu.be, mit/ohne www/mobile).
var youtubeRe = regexp.MustCompile(
	`(?i)^(?:https?://)?(?:www\.|m\.|music\.)?(?:youtube\.com/(?:watch\?.*v=|embed/|shorts/)|youtu\.be/)`,
)

// isYouTubeURL prüft, ob eine URL auf ein YouTube-Video zeigt.
func isYouTubeURL(url string) bool {
	return youtubeRe.MatchString(url)
}

// isYouTubePlaylist prüft, ob eine URL eine YouTube-Playlist ist.
func isYouTubePlaylist(url string) bool {
	return strings.Contains(strings.ToLower(url), "youtube.com/playlist")
}

// ─── yt-dlp Integration ──────────────────────────────────────────────────────

// getBinaryDir gibt das Verzeichnis zurück, in dem die eigene Binärdatei liegt.
// yt-dlp und yt-dlp-plugins liegen direkt daneben → cmd.Dir muss dort gesetzt sein.
func getBinaryDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// getYTDLPPath gibt den Standard-Pfad zu yt-dlp zurück.
func getYTDLPPath() string {
	return filepath.Join(getBinaryDir(), "yt-dlp_linux")
}

// downloadYouTubeAudio lädt die Audiospur eines YouTube-Videos herunter.
// Gibt den Pfad zur heruntergeladenen Datei zurück.
func (a *App) downloadYouTubeAudio(url string) (string, error) {
	// Ausgabeverzeichnis sicherstellen
	if err := os.MkdirAll(a.cfg.YTDLAOutputDir, 0755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	outputFile := filepath.Join(a.cfg.YTDLAOutputDir, fmt.Sprintf("audio_%d.m4a", time.Now().UnixNano()))

	cmd := exec.Command(a.cfg.YTDLPPath,
		"--extract-audio",
		"--audio-format", "m4a",
		"--audio-quality", "0",
		"--no-download-archive",
		"--no-playlist",
		"-o", outputFile,
		"--quiet",
		url,
	)
	cmd.Dir = getBinaryDir()

	// Timeout über Goroutine (exec.CommandContext killt nur den Top-Level-Prozess)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var output []byte
	errCh := make(chan error, 1)
	go func() {
		var e error
		output, e = cmd.CombinedOutput()
		errCh <- e
	}()

	select {
	case cmdErr := <-errCh:
		if cmdErr != nil {
			return "", fmt.Errorf("yt-dlp failed: %s", string(output))
		}
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-errCh
		return "", fmt.Errorf("yt-dlp timed out")
	}

	// Prüfen, ob die Datei existiert
	if _, err := os.Stat(outputFile); err != nil {
		return "", fmt.Errorf("audio file not found after download")
	}

	log.Printf("Downloaded audio: %s (%d bytes)", outputFile, fileSize(outputFile))
	return outputFile, nil
}

// fetchYouTubeTitle nutzt yt-dlp --dump-json, um Video-Metadaten zu holen.
func (a *App) fetchYouTubeTitle(url string) string {
	cmd := exec.Command(a.cfg.YTDLPPath, "--dump-json", "--no-download", url)
	cmd.Dir = getBinaryDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var output []byte
	errCh := make(chan error, 1)
	go func() {
		var e error
		output, e = cmd.CombinedOutput()
		errCh <- e
	}()

	select {
	case cmdErr := <-errCh:
		if cmdErr != nil {
			log.Printf("[yt-dlp title] error: %s", string(output))
			return ""
		}
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-errCh
		log.Printf("[yt-dlp title] timeout")
		return ""
	}

	var meta struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(output, &meta); err == nil && meta.Title != "" {
		return meta.Title
	}
	return ""
}

// sanitizeYouTubeURL extrahiert die kanonische YouTube-URL.
// Schritt 1: Reine Go-Extraktion der Video-ID aus watch-URLs (schnell, immer verfügbar).
// Schritt 2: Fallback via yt-dlp, falls Schritt 1 nicht passt.
// Gibt eine saubere URL wie https://www.youtube.com/watch?v=VIDEO_ID zurück.
func sanitizeYouTubeURL(rawURL, ytdlpPath string) (string, error) {
	// 1. Reine Go-Extraktion: Video-ID aus ?v= oder nach /watch?v=
	// Erkennt auch URLs mit &list=, &index=, &pp= etc. – nur die ID bleibt.
	canonical := extractYouTubeCanonicalURL(rawURL)
	if canonical != "" {
		return canonical, nil
	}

	// 2. Fallback: yt-dlp (für youtu.be Kurzlinks oder exotische Formate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ytdlpPath,
		"-q", "--no-playlist", "--skip-download",
		"--print", "https://www.youtube.com/watch?v=%(id)s",
		rawURL,
	)
	cmd.Dir = getBinaryDir()

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp sanitize: %w", err)
	}

	canonical = strings.TrimSpace(string(output))
	if canonical == "" || !strings.Contains(canonical, "watch?v=") {
		return "", fmt.Errorf("invalid canonical URL: %s", canonical)
	}

	return canonical, nil
}

// extractYouTubeCanonicalURL extrahiert aus einer YouTube-watch-URL die Video-ID
// und baut eine saubere kanonische URL zurück (nur Go, keine externen Aufrufe).
// Erkennt: youtube.com/watch?v=ID&list=..., youtu.be/ID, etc.
func extractYouTubeCanonicalURL(url string) string {
	// YouTube Video-ID: exakt 11 Zeichen alphanumerisch + _ / -
	const idPattern = `[a-zA-Z0-9_-]{11}`

	// watch?v=ID (mit oder ohne Zusatzparameter wie &list=, &index=, &pp=)
	idx := strings.Index(url, "watch?")
	if idx >= 0 {
		rest := url[idx+6:] // nach "watch?"
		// Finde ?v= oder &v=
		for _, prefix := range []string{"v=", "&v="} {
			vIdx := strings.Index(rest, prefix)
			if vIdx >= 0 {
				idStart := vIdx + len(prefix)
				// ID lesen (bis & oder Ende oder #)
				var idBuf strings.Builder
				for i := idStart; i < len(rest) && idBuf.Len() < 11; i++ {
					c := rest[i]
					if c == '&' || c == '#' || c == ' ' {
						break
					}
					idBuf.WriteByte(c)
				}
				videoID := idBuf.String()
				// Regex: muss exakt 11 gültige Zeichen sein
				idRe := regexp.MustCompile(`^` + idPattern + `$`)
				if idRe.MatchString(videoID) {
					return fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
				}
			}
		}
	}

	// youtu.be/ID (optional mit &param= oder #start=)
	if strings.Contains(url, "youtu.be/") {
		beIdx := strings.Index(url, "youtu.be/")
		rest := url[beIdx+9:]
		var idBuf strings.Builder
		for i := 0; i < len(rest) && idBuf.Len() < 11; i++ {
			c := rest[i]
			if c == '?' || c == '#' || c == ' ' {
				break
			}
			idBuf.WriteByte(c)
		}
		videoID := idBuf.String()
		idRe := regexp.MustCompile(`^` + idPattern + `$`)
		if idRe.MatchString(videoID) {
			return fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
		}
	}

	return ""
}

// extractYouTubeSubtitles nutzt yt-dlp, um Untertitel herunterzuladen und als
// vereinfachtes "HH:MM:SS Text"-Format zurückzugeben.
//
// Die Sprachauswahl folgt YTDLP_SUB_LANGS (Default "en.*,de.*"): bei englischen
// Videos wird die englische, bei deutschen die deutsche Spur genutzt. Eine
// frühere Version nutzte ".*-orig" und damit *jede* Original-Spur – YouTube
// liefert dabei oft Arabisch, das dann alphabetisch vor Englisch gewann.
//
// Bei Videos mit automatischer Synchronisierung ("dubbed-auto") liegt der
// Originaltext in "<sprache>-orig" (z. B. "en-orig"); die reinen Codes sind dort
// Übersetzungen aus der Synchronfassung und werden von YouTube zusätzlich mit
// HTTP 429 abgewiesen. "-orig" gewinnt deshalb innerhalb einer Sprache, und
// angefragt werden die "-orig"-Spuren zuerst (siehe narrowSubLangs).
//
// Liefert einen leeren String zurück, wenn keine passenden Untertitel
// verfügbar sind (kein Fehler).
func (a *App) extractYouTubeSubtitles(url string) (string, error) {
	tmpDir := filepath.Join(a.cfg.YTDLAOutputDir, fmt.Sprintf("subs_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	patterns := subLangPatterns(a.cfg.YouTubeSubLangs)

	// yt-dlp --skip-download --write-subs --write-auto-subs --sub-langs <en,de>
	// --sub-format srt/vtt + srt_fix-Plugin (bereinigt Dopplungen/Rollzeilen).
	// Menschliche Untertitel haben Vorrang (bessere Qualität), automatische
	// dienen als Fallback – yt-dlp lädt pro Sprache nur eine Datei.
	res, err := a.downloadSubtitles(subsRequest{
		URL:     url,
		OutDir:  tmpDir,
		Langs:   strings.Join(patterns, ","),
		Auto:    true,
		Manual:  true,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return "", err
	}

	best := pickSubtitle(subSelect{
		Files:       res.Files,
		Patterns:    patterns,
		VideoLang:   res.VideoLang,
		ManualLangs: res.ManualLangs,
	})
	if best == "" {
		if len(res.Files) > 0 {
			// Sollte mit eingeschränktem --sub-langs nicht vorkommen; hilft beim Debuggen.
			log.Printf("[yt-dlp subs] keine passende Spur zu %v (Video=%q, vorhanden: %v)", patterns, res.VideoLang, res.Files)
		}
		return "", nil // Keine Untertitel in den gewünschten Sprachen – kein Fehler
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, best))
	if err != nil {
		return "", fmt.Errorf("read subtitle file: %w", err)
	}

	srtContent := strings.TrimSpace(string(data))
	if srtContent == "" {
		return "", nil // Leere Untertitel
	}

	// Untertitel in das vereinfachte Format konvertieren (SRT oder VTT)
	simple := convertSubtitleToSimple(srtContent)
	log.Printf("[yt-dlp subs] lang=%s (%s) video=%s %d chars -> %d chars (simple)",
		srtLangTag(best), trackOrigin(res.ManualLangs, srtLangTag(best)),
		res.VideoLang, len(srtContent), len(simple))
	if strings.TrimSpace(res.Output) != "" {
		log.Printf("[yt-dlp subs] %s", strings.TrimSpace(res.Output))
	}
	return simple, nil
}

// extractPlaylistVideos extrahiert einzelne Video-URLs aus einer YouTube-Playlist.
// Parst den list=-Parameter und übergibt die ID direkt an yt-dlp.
func extractPlaylistVideos(playlistURL, ytdlpPath string) ([]string, error) {
	// Playlist-ID aus URL extrahieren: https://www.youtube.com/playlist?list=<ID>
	idx := strings.Index(playlistURL, "list=")
	if idx < 0 {
		return nil, fmt.Errorf("no list= parameter found in URL")
	}
	playlistID := playlistURL[idx+5:]
	// Trailing params entfernen (z.B. &si=...)
	if amp := strings.Index(playlistID, "&"); amp >= 0 {
		playlistID = playlistID[:amp]
	}
	playlistID = strings.TrimSpace(playlistID)
	if playlistID == "" {
		return nil, fmt.Errorf("empty playlist ID")
	}

	log.Printf("[playlist] extracted ID: %s from %s", playlistID, playlistURL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ytdlpPath,
		"--flat-playlist", "-s",
		"--print", "url",
		playlistID,
	)
	cmd.Dir = getBinaryDir()

	output, err := cmd.Output()
	if err != nil {
		// Nicht fatal – URL ist einfach keine Playlist
		return nil, nil
	}

	var videos []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, "http") {
			videos = append(videos, line)
		}
	}

	if len(videos) == 0 {
		return nil, nil
	}
	log.Printf("[playlist] extracted %d videos from %s", len(videos), playlistURL)
	return videos, nil
}

// ─── Hilfsfunktionen ─────────────────────────────────────────────────────────

// fileSize gibt die Dateigröße in Bytes zurück oder 0 bei Fehler.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
