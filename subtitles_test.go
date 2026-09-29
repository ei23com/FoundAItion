package main

import "testing"

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
	got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "")
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
	got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "")
	if got != "abc12345678.de-orig-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Originalspur", got)
	}
}

// Nur übersetzte Fassungen (keine -orig-Spur): Musterreihenfolge entscheidet.
func TestPickSubtitleFileTranslatedOnly(t *testing.T) {
	files := []string{"abc12345678.ar.srt", "abc12345678.en.srt", "abc12345678.de.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), ""); got != "abc12345678.en.srt" {
		t.Fatalf("gewählt: %q, erwartet englische Übersetzung", got)
	}
	if got := pickSubtitleFile(files, subLangPatterns("de"), ""); got != "abc12345678.de.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Übersetzung", got)
	}
}

// Manuelle (menschliche) Untertitel haben kein "-orig", müssen aber gefunden werden.
func TestPickSubtitleFileManualSubs(t *testing.T) {
	files := []string{"abc12345678.en.srt", "abc12345678.en-fixed.srt"}
	if got := pickSubtitleFile(files, subLangPatterns("en"), ""); got != "abc12345678.en-fixed.srt" {
		t.Fatalf("gewählt: %q, erwartet bereinigte Datei", got)
	}
	// Nur die rohe Variante vorhanden → diese nehmen.
	if got := pickSubtitleFile([]string{"abc12345678.en.srt"}, subLangPatterns("en"), ""); got != "abc12345678.en.srt" {
		t.Fatalf("gewählt: %q, erwartet rohe Datei", got)
	}
}

// Ohne passende Sprache bleibt die Auswahl leer statt einen Fremdsprachenrest
// zu liefern – so entsteht kein arabisches Transkript mehr.
func TestPickSubtitleFileNoMatchIsEmpty(t *testing.T) {
	files := []string{"abc12345678.ar-orig.srt", "abc12345678.ar-orig-fixed.srt"}
	if got := pickSubtitleFile(nil, subLangPatterns("en"), ""); got != "" {
		t.Fatalf("leere Liste: %q, erwartet \"\"", got)
	}
	if got := pickSubtitleFile(files, subLangPatterns("en"), ""); got != "" {
		t.Fatalf("nur Fremdsprache: %q, erwartet \"\" (kein arabisches Transkript)", got)
	}
}

// ─── Videosprache entscheidet ────────────────────────────────────────────────

// Deutsches Video: YouTube liefert "en-de" (englischer Text aus deutscher
// Quelle) und "en" als Übersetzungen. Ohne Videosprache gewänne "en-de"
// alphabetisch – mit Videosprache muss die deutsche Spur gewinnen.
func TestPickSubtitleFileGermanVideoBeatsTranslation(t *testing.T) {
	files := []string{"abc12345678.de.srt", "abc12345678.en-de.srt", "abc12345678.en.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "de"); got != "abc12345678.de.srt" {
		t.Fatalf("gewählt: %q, erwartet deutsche Spur", got)
	}
	// Gegenprobe: ohne Videosprache greift die Musterreihenfolge (en zuerst).
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), ""); got != "abc12345678.en-de.srt" {
		t.Fatalf("ohne Videosprache: %q", got)
	}
}

// Englisches Video: die Varianten "en-en" und "en-de-DE" sind Übersetzungen
// fremder Fassungen, "en" ist die Originalspur.
func TestPickSubtitleFileEnglishVideoVariants(t *testing.T) {
	files := []string{"dQw4w9WgXcQ.en.srt", "dQw4w9WgXcQ.en-de-DE.srt", "dQw4w9WgXcQ.en-en.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "en"); got != "dQw4w9WgXcQ.en.srt" {
		t.Fatalf("gewählt: %q, erwartet englische Originalspur", got)
	}
}

// Regionalvariante gilt als Originalspur ("de-DE" bei Videosprache de).
func TestPickSubtitleFileRegionVariant(t *testing.T) {
	files := []string{"abc12345678.de-DE.srt", "abc12345678.en-de.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "de"); got != "abc12345678.de-DE.srt" {
		t.Fatalf("gewählt: %q, erwartet de-DE", got)
	}
	// VTT wird nur genutzt, wenn keine SRT-Fassung vorliegt.
	files = []string{"abc12345678.de-DE.vtt", "abc12345678.de-DE-fixed.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "de"); got != "abc12345678.de-DE-fixed.srt" {
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
		{"en-de-DE", "en", false},
		{"en-en", "en", false},
		{"en-ja", "en", false},
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

// Menschliche Untertitel (exakter Sprachcode) haben Vorrang vor der
// automatischen Originalspur – die tagesschau liefert z. B. sauberere "de"-
// Untertitel parallel zu "de-orig".
func TestPickSubtitleFilePrefersManualOverAuto(t *testing.T) {
	files := []string{"00ns5-Gryw4.de-de.srt", "00ns5-Gryw4.de-orig.srt", "00ns5-Gryw4.de.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "de"); got != "00ns5-Gryw4.de.srt" {
		t.Fatalf("gewählt: %q, erwartet menschliche de-Untertitel", got)
	}
	// Ohne menschliche Spur gewinnt die Originalspur.
	files = []string{"00ns5-Gryw4.de-de.srt", "00ns5-Gryw4.de-orig.srt"}
	if got := pickSubtitleFile(files, subLangPatterns(DefaultSubLangs), "de"); got != "00ns5-Gryw4.de-orig.srt" {
		t.Fatalf("gewählt: %q, erwartet de-orig", got)
	}
}

func TestSubTagRank(t *testing.T) {
	cases := []struct {
		tag, lang string
		want      int
	}{
		{"de", "de", 0},
		{"de-orig", "de", 1},
		{"de-DE", "de", 2},
		{"de-de", "de", 3},
		{"en", "de", 3},
	}
	for _, c := range cases {
		if got := subTagRank(c.tag, c.lang); got != c.want {
			t.Errorf("subTagRank(%q, %q) = %d, erwartet %d", c.tag, c.lang, got, c.want)
		}
	}
}

// Die Anforderungsliste muss minimal bleiben: "en.*" zog über Auto-Dub-Varianten
// (en-de-DE, en-ja, …) rund zehn Requests nach sich und lief ins HTTP-429-Limit.
func TestNarrowSubLangs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"en.*,de.*", "en,en-orig,de,de-orig"},
		{"en", "en,en-orig"},
		{"en,de", "en,en-orig,de,de-orig"},
		{"de-DE", "de-DE,de-DE-orig"},
		{".*-orig", ".*-orig"},
	}
	for _, c := range cases {
		if got := narrowSubLangs(subLangPatterns(c.in)); got != c.want {
			t.Errorf("narrowSubLangs(%q) = %q, erwartet %q", c.in, got, c.want)
		}
	}
}
