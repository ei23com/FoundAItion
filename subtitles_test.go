package main

import (
	"strings"
	"testing"
)

// TestDefaultSubLangsGuard stellt sicher, dass die Sprachvorgabe nie wieder auf
// ".*-orig" zurückfällt: dieses Muster lud jede Original-Spur (bei englischen
// Videos häufig Arabisch) und die Dateiauswahl griff dann die falsche Sprache.
func TestDefaultSubLangsGuard(t *testing.T) {
	if DefaultSubLangs != "en.*,de.*" {
		t.Fatalf("DefaultSubLangs = %q, erwartet \"en.*,de.*\"", DefaultSubLangs)
	}
	if p := subLangPatterns(DefaultSubLangs); len(p) != 2 || p[0] != "en.*" || p[1] != "de.*" {
		t.Fatalf("unerwartete Muster: %#v", p)
	}
}

func TestSubLangPatterns(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"en", []string{"en.*"}},
		{"en,de", []string{"en.*", "de.*"}},
		{" en , de ", []string{"en.*", "de.*"}},
		{"en.*,de.*,.*-orig", []string{"en.*", "de.*", ".*-orig"}},
		{"de-DE", []string{"de-DE.*"}}, // Bindestrich ist kein Regex-Zeichen
		{"", []string{"en.*", "de.*"}}, // leer → Default
	}
	for _, c := range cases {
		got := subLangPatterns(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("subLangPatterns(%q) = %#v, erwartet %#v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("subLangPatterns(%q) = %#v, erwartet %#v", c.in, got, c.want)
			}
		}
	}
}

func TestSrtLangTag(t *testing.T) {
	cases := map[string]string{
		"3SBf7hD0sEw.en-orig-fixed.srt": "en-orig",
		"3SBf7hD0sEw.en-orig.srt":       "en-orig",
		"3SBf7hD0sEw.ar-orig-fixed.srt": "ar-orig",
		"3SBf7hD0sEw.de.srt":            "de",
		"3SBf7hD0sEw.en-US.srt":         "en-US",
		"ohne-sprache.srt":              "",
	}
	for in, want := range cases {
		if got := srtLangTag(in); got != want {
			t.Errorf("srtLangTag(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// TestPickSubtitleFileEnglishVideo ist der Regressionstest zum gemeldeten
// Fehler: bei einem englischen Video lagen arabische UND englische Spuren vor,
// die alphabetisch erste Datei (ar-…) wurde gewählt.
func TestPickSubtitleFileEnglishVideo(t *testing.T) {
	files := []string{
		"3SBf7hD0sEw.ar-orig.srt",
		"3SBf7hD0sEw.ar-orig-fixed.srt",
		"3SBf7hD0sEw.en-orig.srt",
		"3SBf7hD0sEw.en-orig-fixed.srt",
	}
	got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: ""})
	if got != "3SBf7hD0sEw.en-orig-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet englische Originalspur", got)
	}
}

// Deutsches Video: deutsche Originalspur gewinnt gegen die englische Übersetzung.
func TestPickSubtitleFileGermanVideo(t *testing.T) {
	files := []string{
		"abc12345678.de-orig.srt",
		"abc12345678.de-orig-fixed.srt",
		"abc12345678.en.srt",
		"abc12345678.en-fixed.srt",
	}
	got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: ""})
	if got != "abc12345678.de-orig-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Originalspur", got)
	}
}

// Nur übersetzte Fassungen (keine -orig-Spur): Musterreihenfolge entscheidet.
func TestPickSubtitleFileTranslatedOnly(t *testing.T) {
	files := []string{"abc12345678.ar.srt", "abc12345678.en.srt", "abc12345678.de.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: ""}); got != "abc12345678.en.srt" {
		t.Fatalf("gewählt: %q, erwartet englische Übersetzung", got)
	}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns("de"), VideoLang: ""}); got != "abc12345678.de.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Übersetzung", got)
	}
}

// Manuelle (menschliche) Untertitel haben kein "-orig", müssen aber gefunden werden.
func TestPickSubtitleFileManualSubs(t *testing.T) {
	files := []string{"abc12345678.en.srt", "abc12345678.en-fixed.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns("en"), VideoLang: ""}); got != "abc12345678.en-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet bereinigte Datei", got)
	}
	// Nur die rohe Variante vorhanden → diese nehmen.
	if got := pickSubtitle(subSelect{Files: []string{"abc12345678.en.srt"}, Patterns: subLangPatterns("en"), VideoLang: ""}); got != "abc12345678.en.srt" {
		t.Fatalf("gewählt: %q, erwartet rohe Datei", got)
	}
}

// Ohne passende Sprache bleibt die Auswahl leer statt einen Fremdsprachenrest
// zu liefern – so entsteht kein arabisches Transkript mehr.
func TestPickSubtitleFileNoMatchIsEmpty(t *testing.T) {
	files := []string{"abc12345678.ar-orig.srt", "abc12345678.ar-orig-fixed.srt"}
	if got := pickSubtitle(subSelect{Files: nil, Patterns: subLangPatterns("en"), VideoLang: ""}); got != "" {
		t.Fatalf("leere Liste: %q, erwartet \"\"", got)
	}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns("en"), VideoLang: ""}); got != "" {
		t.Fatalf("nur Fremdsprache: %q, erwartet \"\" (kein arabisches Transkript)", got)
	}
}

// ─── Videosprache entscheidet ────────────────────────────────────────────────

// Deutsches Video: YouTube liefert "en-de" (englischer Text aus deutscher
// Quelle) und "en" als Übersetzungen. Ohne Videosprache gewänne "en-de"
// alphabetisch – mit Videosprache muss die deutsche Spur gewinnen.
func TestPickSubtitleFileGermanVideoBeatsTranslation(t *testing.T) {
	files := []string{"abc12345678.de.srt", "abc12345678.en-de.srt", "abc12345678.en.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de"}); got != "abc12345678.de.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Spur", got)
	}
	// Gegenprobe: ohne Videosprache greift die Musterreihenfolge (en zuerst).
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: ""}); got != "abc12345678.en-de.srt" {
		t.Fatalf("ohne Videosprache: %q", got)
	}
}

// Englisches Video: die Varianten "en-en" und "en-de-DE" sind Übersetzungen
// fremder Fassungen, "en" ist die Originalspur.
func TestPickSubtitleFileEnglishVideoVariants(t *testing.T) {
	files := []string{"dQw4w9WgXcQ.en.srt", "dQw4w9WgXcQ.en-de-DE.srt", "dQw4w9WgXcQ.en-en.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "en"}); got != "dQw4w9WgXcQ.en.srt" {
		t.Fatalf("gewählt: %q, erwartet englische Originalspur", got)
	}
}

// Regionalvariante gilt als Originalspur ("de-DE" bei Videosprache de).
func TestPickSubtitleFileRegionVariant(t *testing.T) {
	files := []string{"abc12345678.de-DE.srt", "abc12345678.en-de.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de"}); got != "abc12345678.de-DE.srt" {
		t.Fatalf("gewählt: %q, erwartet de-DE", got)
	}
	// VTT wird nur genutzt, wenn keine SRT-Fassung vorliegt.
	files = []string{"abc12345678.de-DE.vtt", "abc12345678.de-DE-fixed.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de"}); got != "abc12345678.de-DE-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet SRT vor VTT", got)
	}
}

func TestIsOriginalTag(t *testing.T) {
	cases := []struct {
		tag, lang string
		want      bool
	}{
		{"en", "en", true},
		{"en-orig", "en", true},
		{"en-US", "en", true},
		{"de-DE", "de", true},
		{"en-US-orig", "en", true},   // Originalspur mit Regionalcode
		{"de-DE-orig", "de", true},   // dito
		{"pt-BR-orig", "pt", true},   // dito
		{"en-Latn-orig", "en", true}, // Schriftsatz
		{"zh-Hans-orig", "zh", true}, // Schriftsatz (chinesisch vereinfacht)
		{"zh-Hant", "zh", true},      // Schriftsatz ohne -orig
		{"en-de-DE", "en", false},
		{"en-en", "en", false},
		{"en-ja", "en", false},
		{"en-de-DE-orig", "en", false}, // Übersetzung bleibt Übersetzung
		{"de", "en", false},
		{"", "en", false},
	}
	for _, c := range cases {
		if got := isOriginalTag(c.tag, c.lang); got != c.want {
			t.Errorf("isOriginalTag(%q, %q) = %v, erwartet %v", c.tag, c.lang, got, c.want)
		}
	}
}

func TestParseVideoLang(t *testing.T) {
	cases := map[string]string{
		"en\n":           "en",
		"de-DE\n":        "de-DE",
		"NA\n":           "",
		"\n":             "",
		"en\nwarning\n":  "en",
		"WARNING: bla\n": "",
	}
	for in, want := range cases {
		if got := parseVideoLang(in); got != want {
			t.Errorf("parseVideoLang(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// ─── VTT-Konvertierung (ohne ffmpeg) ─────────────────────────────────────────

func TestConvertSubtitleToSimpleVTT(t *testing.T) {
	vtt := "WEBVTT\nKind: captions\nLanguage: en\n\n" +
		"00:00:00.080 --> 00:00:03.129 align:start position:0%\n \nhello and welcome\n\n" +
		"00:00:03.129 --> 00:00:03.139 align:start position:0%\nhello and welcome\nto the show\n\n" +
		"00:00:06.000 --> 00:00:08.000 align:start position:0%\n<c>colored</c> text <00:00:07.000>here\n"
	got := convertSubtitleToSimple(vtt)
	want := "00:00:00 hello and welcome\n00:00:03 to the show\n00:00:06 colored text here"
	if got != want {
		t.Fatalf("VTT-Konvertierung:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvertSubtitleToSimpleSRT(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHallo Welt\n\n2\n00:00:03,000 --> 00:00:04,000\nZweite Zeile\n"
	got := convertSubtitleToSimple(srt)
	if got != "00:00:01 Hallo Welt\n00:00:03 Zweite Zeile" {
		t.Fatalf("SRT-Konvertierung: %q", got)
	}
	if convertSubtitleToSimple("   ") != "" {
		t.Fatalf("leerer Inhalt muss leer bleiben")
	}
}

// Menschliche Untertitel (über yt-dlp als "subtitles" gemeldet) haben Vorrang
// vor der automatischen Originalspur – die tagesschau liefert z. B. sauberere
// "de"-Untertitel parallel zu "de-orig".
func TestPickSubtitleFilePrefersManualOverAuto(t *testing.T) {
	files := []string{"00ns5-Gryw4.de-de.srt", "00ns5-Gryw4.de-orig.srt", "00ns5-Gryw4.de.srt"}
	got := pickSubtitle(subSelect{
		Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de",
		ManualLangs: []string{"de"},
	})
	if got != "00ns5-Gryw4.de.srt" {
		t.Fatalf("gewählt: %q, erwartet menschliche de-Untertitel", got)
	}
	// Ohne menschliche Spur gewinnt die Originalspur.
	files = []string{"00ns5-Gryw4.de-de.srt", "00ns5-Gryw4.de-orig.srt"}
	if got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de"}); got != "00ns5-Gryw4.de-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet de-orig", got)
	}
}

// ─── Automatische Synchronisierung ("dubbed-auto") ───────────────────────────

// Regressionstest zum gemeldeten Fehler: bei Videos mit YouTubes automatischer
// Synchronisierung heißen die brauchbaren Spuren "en-orig"/"de-orig". Der reine
// Code ("en") ist dort die Übersetzung aus einer Synchronfassung und wird von
// YouTube zudem mit HTTP 429 abgewiesen. Die Originalspur muss gewinnen.
func TestPickSubtitlePrefersOriginalAudioOverDubbedTranslation(t *testing.T) {
	files := []string{"a4vfv4EMgYs.de-orig.srt", "a4vfv4EMgYs.en-orig.srt", "a4vfv4EMgYs.en.srt"}
	got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "en-US"})
	if got != "a4vfv4EMgYs.en-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet en-orig (Originalspur)", got)
	}
	// Auch ohne Regionszusatz in der Videosprache ("en" statt "en-US").
	got = pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "en"})
	if got != "a4vfv4EMgYs.en-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet en-orig", got)
	}
	// Teilweise fehlgeschlagene Sprachen: nur -orig-Dateien vorhanden.
	got = pickSubtitle(subSelect{
		Files:    []string{"a4vfv4EMgYs.de-orig.srt", "a4vfv4EMgYs.en-orig.srt"},
		Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "en-US",
	})
	if got != "a4vfv4EMgYs.en-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet en-orig", got)
	}
}

// Deutsches Video mit englischer Synchronfassung: nur "en-orig" (ASR der
// Synchronfassung) und die deutsche Auto-Spur liegen vor. Die Videosprache
// entscheidet – die deutsche Spur bleibt erste Wahl.
func TestPickSubtitleGermanVideoWithEnglishDub(t *testing.T) {
	files := []string{"xfHMB44dvig.de.srt", "xfHMB44dvig.en-orig.srt"}
	got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de-DE"})
	if got != "xfHMB44dvig.de.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Spur", got)
	}
	// Fällt die deutsche Spur aus (429), bleibt die englische Originalspur.
	got = pickSubtitle(subSelect{
		Files:    []string{"xfHMB44dvig.en-orig.srt"},
		Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "de-DE",
	})
	if got != "xfHMB44dvig.en-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet en-orig", got)
	}
}

// Regionalcode in der Originalspur ("en-US-orig", "pt-BR-orig"): muss als
// Originalspur erkannt und gegen die reine Übersetzung bevorzugt werden.
func TestPickSubtitleRegionOriginalTag(t *testing.T) {
	files := []string{"abc12345678.en.srt", "abc12345678.en-US-orig.srt"}
	got := pickSubtitle(subSelect{Files: files, Patterns: subLangPatterns(DefaultSubLangs), VideoLang: "en-US"})
	if got != "abc12345678.en-US-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet en-US-orig", got)
	}
}

// parseManualSubLangs liest die --print-Ausgabe von %(subtitles)j.
func TestParseManualSubLangs(t *testing.T) {
	jsonLine := `{"de": [{"ext": "json3", "url": "https://example.invalid/x", "name": "German"}]}`
	got := parseManualSubLangs("de-DE\n" + jsonLine + "\n")
	if len(got) != 1 || got[0] != "de" {
		t.Fatalf("parseManualSubLangs = %#v, erwartet [de]", got)
	}
	if got := parseManualSubLangs("en\nNA\n"); got != nil {
		t.Fatalf("ohne menschliche Untertitel: %#v, erwartet nil", got)
	}
	if got := parseManualSubLangs(""); got != nil {
		t.Fatalf("leere Ausgabe: %#v, erwartet nil", got)
	}
	// Mehrere Sprachen, sortiert; die JSON-Zeile darf die Videosprache nicht stören.
	got = parseManualSubLangs(`{"pt-BR": [], "en": [], "de": []}`)
	if len(got) != 3 || got[0] != "de" || got[1] != "en" || got[2] != "pt-BR" {
		t.Fatalf("parseManualSubLangs = %#v", got)
	}
}

// stripSubsInfo hält die Untertitel-URLs aus dem Logtext heraus.
func TestStripSubsInfo(t *testing.T) {
	out := "en-US\n" + `{"en": [{"url": "https://www.youtube.com/api/timedtext?v=x&signature=geheim"}]}` + "\nWARNING: etwas"
	got := stripSubsInfo(out)
	if strings.Contains(got, "signature=geheim") {
		t.Fatalf("JSON-Zeile nicht entfernt: %q", got)
	}
	if !strings.Contains(got, "en-US") || !strings.Contains(got, "WARNING: etwas") {
		t.Fatalf("relevante Zeilen fehlen: %q", got)
	}
}

func TestSubTagRank(t *testing.T) {
	cases := []struct {
		tag, lang string
		want      int
	}{
		{"de-orig", "de", 0}, // Originalspur (ASR der gesprochenen Tonspur)
		{"de-DE-orig", "de", 0},
		{"de", "de", 1}, // exakter Code: menschlich oder Auto der Originalspur
		{"de-DE", "de", 1},
		{"de-de", "de", 2}, // Übersetzung
		{"en", "de", 2},
	}
	for _, c := range cases {
		if got := subTagRank(c.tag, c.lang); got != c.want {
			t.Errorf("subTagRank(%q, %q) = %d, erwartet %d", c.tag, c.lang, got, c.want)
		}
	}
}

// Die Anforderungsliste muss minimal bleiben: "en.*" zog über Auto-Dub-Varianten
// (en-de-DE, en-ja, …) rund zehn Requests nach sich und lief ins HTTP-429-Limit.
// Die "-orig"-Spuren stehen vorn, weil yt-dlp den Download beim ersten
// Sprachfehler abbricht und die reinen Codes synchronisierter Videos (en, de)
// mit HTTP 429 antworten.
func TestNarrowSubLangs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"en.*,de.*", "en.*-orig,de.*-orig,en,de"},
		{"en", "en.*-orig,en"},
		{"en,de", "en.*-orig,de.*-orig,en,de"},
		{"de-DE", "de-DE.*-orig,de-DE"},
		{".*-orig", ".*-orig"},
	}
	for _, c := range cases {
		if got := narrowSubLangs(subLangPatterns(c.in)); got != c.want {
			t.Errorf("narrowSubLangs(%q) = %q, erwartet %q", c.in, got, c.want)
		}
	}
}
