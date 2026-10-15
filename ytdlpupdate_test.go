package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeYtDlp liefert ein gültiges ELF-Binary in Mindestgröße.
func fakeYtDlp() []byte {
	var buf bytes.Buffer
	buf.WriteString("\x7fELF")
	buf.Write(bytes.Repeat([]byte{0}, minYtDlpBytes+1024))
	return buf.Bytes()
}

const fakePlugin = "# srt_fix\nfrom yt_dlp_plugins.postprocessor.srt_fix import X\n" +
	"# padding padding padding padding padding padding padding padding padding\n"

// ytDlpTestServer stellt Binary und Plugin bereit; ytStatus/srtStatus steuern
// die Statuscodes.
func ytDlpTestServer(t *testing.T, ytStatus, srtStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/yt-dlp_linux", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(ytStatus)
		if ytStatus == http.StatusOK {
			w.Write(fakeYtDlp())
			return
		}
		fmt.Fprint(w, "error page")
	})
	mux.HandleFunc("/srt_fix.py", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(srtStatus)
		if srtStatus == http.StatusOK {
			fmt.Fprint(w, fakePlugin)
			return
		}
		fmt.Fprint(w, "<html>404</html>")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// pointYtDlpURLs biegt die Download-Quellen auf den Testserver um.
func pointYtDlpURLs(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldYt, oldSrt := ytDlpURL, srtFixURL
	ytDlpURL = srv.URL + "/yt-dlp_linux"
	srtFixURL = srv.URL + "/srt_fix.py"
	t.Cleanup(func() { ytDlpURL, srtFixURL = oldYt, oldSrt })
}

// Erfolgsfall: Binary + Plugin landen im Zielverzeichnis, Binary ist ausführbar,
// keine .tmp-Reste.
func TestInstallYtDlpWritesBinaryAndPlugin(t *testing.T) {
	srv := ytDlpTestServer(t, http.StatusOK, http.StatusOK)
	pointYtDlpURLs(t, srv)
	dir := t.TempDir()

	if err := installYtDlp(dir); err != nil {
		t.Fatalf("installYtDlp: %v", err)
	}

	ytPath := filepath.Join(dir, "yt-dlp_linux")
	info, err := os.Stat(ytPath)
	if err != nil {
		t.Fatalf("Binary fehlt: %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("Binary ist nicht ausführbar (Mode %v)", info.Mode().Perm())
	}
	if info.Size() != int64(len(fakeYtDlp())) {
		t.Errorf("Binary-Größe = %d, erwartet %d", info.Size(), len(fakeYtDlp()))
	}

	pluginPath := filepath.Join(dir, pluginRelPath)
	if data, err := os.ReadFile(pluginPath); err != nil || string(data) != fakePlugin {
		t.Errorf("Plugin fehlt oder ist falsch: %v", err)
	}

	if _, err := os.Stat(ytPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporäre Datei blieb liegen: %v", err)
	}
}

// Bleibt der Download fehlgeschlagen, darf das vorhandene Binary nicht
// angetastet werden (früher wurde die Datei sofort mit os.Create geleert).
func TestInstallYtDlpKeepsExistingBinaryOnDownloadError(t *testing.T) {
	srv := ytDlpTestServer(t, http.StatusInternalServerError, http.StatusOK)
	pointYtDlpURLs(t, srv)
	dir := t.TempDir()

	ytPath := filepath.Join(dir, "yt-dlp_linux")
	const existing = "bestehendes yt-dlp binary"
	if err := os.WriteFile(ytPath, []byte(existing), 0755); err != nil {
		t.Fatalf("Vorbedingung: %v", err)
	}

	if err := installYtDlp(dir); err == nil {
		t.Fatalf("erwartet Fehler beim Download")
	}

	data, err := os.ReadFile(ytPath)
	if err != nil {
		t.Fatalf("Binary verschwunden: %v", err)
	}
	if string(data) != existing {
		t.Errorf("vorhandenes Binary wurde verändert: %q", data)
	}
	if _, err := os.Stat(ytPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporäre Datei blieb liegen: %v", err)
	}
}

// Eine HTML-Fehlerseite mit Status 200 darf ein vorhandenes Binary nicht
// ersetzen.
func TestInstallYtDlpRejectsNonELFResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/yt-dlp_linux", func(w http.ResponseWriter, r *http.Request) {
		// Status 200, aber Inhalt ist eine Fehlerseite.
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Not Found</body></html>"))
	})
	mux.HandleFunc("/srt_fix.py", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, fakePlugin)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	pointYtDlpURLs(t, srv)
	dir := t.TempDir()

	ytPath := filepath.Join(dir, "yt-dlp_linux")
	if err := os.WriteFile(ytPath, []byte("altes binary"), 0755); err != nil {
		t.Fatalf("Vorbedingung: %v", err)
	}

	err := installYtDlp(dir)
	if err == nil {
		t.Fatalf("erwartet Fehler bei HTML-Antwort")
	}
	if !strings.Contains(err.Error(), "ELF") && !strings.Contains(err.Error(), "Fehlerseite") {
		t.Errorf("unerwartete Fehlermeldung: %v", err)
	}
	if data, _ := os.ReadFile(ytPath); string(data) != "altes binary" {
		t.Errorf("vorhandenes Binary wurde ersetzt: %q", data)
	}
}

// Das Plugin wird ebenfalls geprüft: eine HTML-Antwort wird abgelehnt.
func TestInstallYtDlpRejectsInvalidPlugin(t *testing.T) {
	srv := ytDlpTestServer(t, http.StatusOK, http.StatusOK)
	pointYtDlpURLs(t, srv)
	dir := t.TempDir()

	// Plugin-Quelle auf eine HTML-Antwort umbiegen.
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>Fehlerseite</html>")
	})
	htmlSrv := httptest.NewServer(mux)
	defer htmlSrv.Close()
	srtFixURL = htmlSrv.URL + "/html"

	if err := installYtDlp(dir); err == nil {
		t.Fatalf("erwartet Fehler bei ungültigem Plugin")
	}
	if _, err := os.Stat(filepath.Join(dir, pluginRelPath)); !os.IsNotExist(err) {
		t.Errorf("ungültiges Plugin wurde gespeichert")
	}
}

// Regressionsschutz für die UI-Verdrahtung: Der Inhalt des Konfig-Modals wird
// bei jedem Öffnen neu gerendert (configBody.innerHTML = …). Ein Klick-Listener,
// der beim Laden an die Buttons gehängt wird, läuft deshalb ins Leere – der
// Button "yt-dlp aktualisieren" war genau deshalb ohne Funktion. Er muss
// delegiert am Container hängen.
func TestUpdateButtonIsWiredByDelegation(t *testing.T) {
	raw, err := templateFS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("Template lesen: %v", err)
	}
	html := string(raw)

	if !strings.Contains(html, "configBody.addEventListener('click'") {
		t.Errorf("kein delegierter Klick-Listener am Konfig-Container")
	}
	if !strings.Contains(html, "closest('.config-update-btn')") {
		t.Errorf("delegierter Listener greift den Aktionsbutton nicht ab")
	}
	if strings.Contains(html, "querySelectorAll('.config-update-btn')") {
		t.Errorf("Listener wird wieder beim Laden an die (noch nicht gerenderten) Buttons gehängt")
	}
	if !strings.Contains(html, "'/api/update-ytdlp'") {
		t.Errorf("Button ruft /api/update-ytdlp nicht auf")
	}
}
