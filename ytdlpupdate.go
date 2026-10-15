package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ─── yt-dlp Download / Update ───────────────────────────────────────────────

// ytDlpURL is the download URL for yt-dlp Linux binary.
// var (nicht const), damit Tests die Quelle umbiegen können.
var ytDlpURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"

// srtFixURL is the download URL for the srt_fix plugin.
var srtFixURL = "https://raw.githubusercontent.com/bindestriche/srt_fix/master/yt_dlp_plugins/postprocessor/srt_fix.py"

// pluginRelPath is where the srt_fix plugin is stored relative to the binary dir.
const pluginRelPath = "yt-dlp-plugins/srt_fix/yt_dlp_plugins/postprocessor/srt_fix.py"

// downloadTimeout begrenzt einen einzelnen Download (yt-dlp ist ~30 MB).
const downloadTimeout = 10 * time.Minute

// minYtDlpBytes ist die Mindestgröße des yt-dlp-Binaries – kleinere Downloads
// sind Fehlerseiten o. Ä. und dürfen ein vorhandenes Binary nicht ersetzen.
const minYtDlpBytes = 1 << 20 // 1 MB

// installYtDlp downloads yt-dlp_linux and the srt_fix plugin,
// then makes the binary executable. Equivalent to install.sh.
func installYtDlp(binaryDir string) error {
	// 1. Create plugin directory
	pluginDir := filepath.Join(binaryDir, filepath.Dir(pluginRelPath))
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return fmt.Errorf("create plugin dir: %w", err)
	}

	// 2. Download srt_fix.py
	pluginPath := filepath.Join(binaryDir, pluginRelPath)
	log.Printf("[yt-dlp] downloading srt_fix.py → %s", pluginPath)
	if err := downloadFileChecked(pluginPath, srtFixURL, validatePlugin); err != nil {
		return fmt.Errorf("download srt_fix.py: %w", err)
	}

	// 3. Download yt-dlp_linux
	ytPath := filepath.Join(binaryDir, "yt-dlp_linux")
	log.Printf("[yt-dlp] downloading yt-dlp_linux → %s", ytPath)
	if err := downloadFileChecked(ytPath, ytDlpURL, validateYtDlpBinary); err != nil {
		return fmt.Errorf("download yt-dlp: %w", err)
	}

	// 4. Make executable
	if err := os.Chmod(ytPath, 0755); err != nil {
		return fmt.Errorf("chmod yt-dlp: %w", err)
	}

	log.Printf("[yt-dlp] installation complete: %s", ytPath)
	return nil
}

// downloadFile loads a URL to a local path (atomar, mit Prüfung).
func downloadFile(path, url string) error {
	return downloadFileChecked(path, url, nil)
}

// downloadFileChecked lädt eine URL in eine temporäre Datei neben dem Ziel und
// ersetzt das Ziel erst nach erfolgreichem Download (os.Rename ist atomar).
// Dadurch bleibt eine vorhandene, funktionierende Datei bei Fehlern oder
// abgebrochenen Downloads unangetastet – und ein laufendes yt-dlp wird nicht
// unter den Füßen wegüberschrieben (ETXTBSY).
func downloadFileChecked(path, url string, validate func(tmpPath string) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d from %s", resp.StatusCode, url)
	}

	tmpPath := path + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpPath, err)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}

	if validate != nil {
		if err := validate(tmpPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("%s: %w", url, err)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// validateYtDlpBinary prüft, dass der Download wirklich ein ausführbares
// yt-dlp-Binary ist (ELF) und keine Fehlerseite.
func validateYtDlpBinary(tmpPath string) error {
	info, err := os.Stat(tmpPath)
	if err != nil {
		return err
	}
	if info.Size() < minYtDlpBytes {
		return fmt.Errorf("Download zu klein (%d Bytes) – vermutlich eine Fehlerseite", info.Size())
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("Datei nicht lesbar: %w", err)
	}
	if string(magic[:]) != "\x7fELF" {
		return fmt.Errorf("keine ausführbare Datei (kein ELF-Kopf)")
	}
	return nil
}

// validatePlugin prüft, dass das heruntergeladene Plugin wirklich Python-Code
// ist und nicht z. B. eine HTML-Fehlerseite.
func validatePlugin(tmpPath string) error {
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	if len(data) < 100 || !strings.Contains(string(data), "yt_dlp_plugins") && !strings.Contains(string(data), "yt_dlp") {
		return fmt.Errorf("kein gültiges Plugin (%d Bytes)", len(data))
	}
	return nil
}

// ytDlpVersion liest die Version des installierten yt-dlp (leer bei Fehler).
func ytDlpVersion(binaryDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, filepath.Join(binaryDir, "yt-dlp_linux"), "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// needsYtDlpInstall checks whether yt-dlp_linux is present and executable.
func needsYtDlpInstall(binaryDir string) bool {
	ytPath := filepath.Join(binaryDir, "yt-dlp_linux")
	_, err := os.Stat(ytPath)
	return os.IsNotExist(err)
}

// ─── API: Update yt-dlp ─────────────────────────────────────────────────────

// handleUpdateYtDlp triggers a fresh download of yt-dlp and the plugin.
// POST /api/update-ytdlp
func (a *App) handleUpdateYtDlp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	binaryDir := getBinaryDir()

	if err := installYtDlp(binaryDir); err != nil {
		log.Printf("[update-ytdlp] error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"ok":    "false",
			"error": err.Error(),
		})
		return
	}

	message := "yt-dlp updated successfully"
	if version := ytDlpVersion(binaryDir); version != "" {
		message = "yt-dlp " + version + " installiert"
	}
	log.Printf("[update-ytdlp] %s", message)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"ok":      "true",
		"message": message,
	})
}
