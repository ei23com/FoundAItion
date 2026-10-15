package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeYtDlpStub schreibt ein ausführbares Shell-Skript, das yt-dlp ersetzt: es
// protokolliert seine Argumente und legt die angegebenen Untertiteldateien im
// Zielverzeichnis an.
func fakeYtDlpStub(t *testing.T, outDir string, files []string, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Shell-Stub nur unter Unix")
	}
	path := filepath.Join(t.TempDir(), "yt-dlp_fake")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + filepath.Join(outDir, "args.txt") + "\n")
	for _, f := range files {
		b.WriteString(": > " + filepath.Join(outDir, f) + "\n")
	}
	b.WriteString(script)
	if err := os.WriteFile(path, []byte(b.String()), 0755); err != nil {
		t.Fatalf("Stub schreiben: %v", err)
	}
	return path
}

// TestRunSubsDownloadRequestsOrigFirst prüft die yt-dlp-Argumente: Die
// Originalspuren ("en.*-orig") müssen vor den reinen Codes stehen und
// --ignore-errors muss gesetzt sein. Ohne das bricht yt-dlp den Download beim
// ersten HTTP 429 einer Sprache ab und lädt gar keine Untertitel – genau der
// gemeldete Fehler bei neueren (automatisch synchronisierten) Videos.
func TestRunSubsDownloadRequestsOrigFirst(t *testing.T) {
	outDir := t.TempDir()
	langs := "en.*-orig,de.*-orig,en,de"
	bin := fakeYtDlpStub(t, outDir, []string{"fakeid.en.srt", "fakeid.en-orig.srt"},
		"printf 'en-US\\n'\nprintf '%s\\n' '{\"de\": [{\"url\": \"https://example.invalid/geheim\"}]}'\nexit 1\n")

	app := &App{cfg: Config{YTDLPPath: bin}}
	res, err := app.runSubsDownload(subsRequest{
		URL:     "https://www.youtube.com/watch?v=fakeid",
		OutDir:  outDir,
		Auto:    true,
		Manual:  true,
		Timeout: 30 * time.Second,
	}, langs)
	if err != nil {
		t.Fatalf("runSubsDownload: %v", err)
	}

	args, err := os.ReadFile(filepath.Join(outDir, "args.txt"))
	if err != nil {
		t.Fatalf("Argumente nicht protokolliert: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	for _, want := range []string{"--ignore-errors", "--write-subs", "--write-auto-subs", "%(subtitles)j", "%(language)s"} {
		if !contains(got, want) {
			t.Errorf("Argument %q fehlt in %v", want, got)
		}
	}
	if i := indexOf(got, "--sub-langs"); i < 0 || i+1 >= len(got) || got[i+1] != langs {
		t.Errorf("--sub-langs: %v, erwartet %q", got, langs)
	}
	if !strings.HasPrefix(got[len(got)-1], "https://") {
		t.Errorf("URL muss am Ende stehen: %v", got)
	}

	// Der Teilfehler (einzelne Sprache scheitert mit 429) darf den Lauf nicht
	// scheitern lassen, solange Dateien angekommen sind.
	if len(res.Files) != 2 {
		t.Fatalf("Files = %v, erwartet zwei Dateien", res.Files)
	}
	if res.VideoLang != "en-US" {
		t.Errorf("VideoLang = %q, erwartet \"en-US\"", res.VideoLang)
	}
	if len(res.ManualLangs) != 1 || res.ManualLangs[0] != "de" {
		t.Errorf("ManualLangs = %v, erwartet [de]", res.ManualLangs)
	}
	// Die Untertitel-URLs gehören nicht ins Log.
	if strings.Contains(res.Output, "geheim") {
		t.Errorf("JSON-Zeile nicht aus der Ausgabe gefiltert: %q", res.Output)
	}
	if !strings.Contains(res.Output, "en-US") {
		t.Errorf("Videosprache fehlt in der Ausgabe: %q", res.Output)
	}

	// Und die Auswahl greift die Originalspur – nicht die reine Auto-Spur.
	best := pickSubtitle(subSelect{
		Files:       res.Files,
		Patterns:    subLangPatterns(DefaultSubLangs),
		VideoLang:   res.VideoLang,
		ManualLangs: res.ManualLangs,
	})
	if best != "fakeid.en-orig.srt" {
		t.Errorf("gewählt: %q, erwartet fakeid.en-orig.srt", best)
	}
}

// Ohne jede Datei bleibt der Lauf ein Fehler (die Pipeline nutzt dann Whisper).
func TestRunSubsDownloadFailsWithoutFiles(t *testing.T) {
	outDir := t.TempDir()
	bin := fakeYtDlpStub(t, outDir, nil,
		"printf 'ERROR: Unable to download video subtitles for en: HTTP Error 429: Too Many Requests\\n' 1>&2\nexit 1\n")

	app := &App{cfg: Config{YTDLPPath: bin}}
	res, err := app.runSubsDownload(subsRequest{
		URL:     "https://www.youtube.com/watch?v=fakeid",
		OutDir:  outDir,
		Auto:    true,
		Manual:  true,
		Timeout: 30 * time.Second,
	}, "en.*-orig,en")
	if err == nil {
		t.Fatalf("erwartet Fehler, bekam %#v", res)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("Fehlertext ohne yt-dlp-Meldung: %v", err)
	}
}

func contains(list []string, want string) bool { return indexOf(list, want) >= 0 }

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}
