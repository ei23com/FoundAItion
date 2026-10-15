package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ─── Untertitel-Sprachauswahl ────────────────────────────────────────────────
//
// Wichtig: NIEMALS ".*-orig" als Sprachmuster verwenden. Das Muster lädt jede
// Original-Spur, die YouTube anbietet – bei englischen Videos ist das häufig
// Arabisch ("ar-orig") – und die anschließende Dateiauswahl ("erste .srt
// gewinnt") greift dann genau die falsche Sprache ab.
//
// YouTube benennt automatisierte Spuren als "<ziel>-<quelle>" (z. B. "en-de-DE"
// = englischer Text aus der deutschen Fassung) bzw. "<sprache>-orig" für die
// gesprochene Originalspur. yt-dlp matcht Sprachmuster case-insensitiv, daher
// lassen sich Übersetzungen nicht per Muster ausschließen – die Auswahl
// entscheidet deshalb anhand der im selben Aufruf ermittelten Videosprache.

// DefaultSubLangs ist die Standard-Priorität: erst Englisch, dann Deutsch.
const DefaultSubLangs = "en.*,de.*"

// subLangPatterns zerlegt eine kommagetrennte Sprachliste in einzelne
// yt-dlp-Muster. Kurze Codes werden zu Präfixmustern erweitert, damit auch
// Varianten wie "en-orig" oder "de-DE" gefunden werden ("en" → "en.*").
func subLangPatterns(list string) []string {
	var out []string
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, normalizeSubLang(p))
	}
	if len(out) == 0 {
		return subLangPatterns(DefaultSubLangs)
	}
	return out
}

// narrowSubLangs leitet aus den Mustern eine minimale Anforderungsliste ab und
// stellt die "-orig"-Spuren voran: "en.*,de.*" → "en.*-orig,de.*-orig,en,de".
//
// Zwei Gründe für diese Reihenfolge:
//
//  1. Seit YouTubes automatischer Synchronisierung ("dubbed-auto") sind die
//     reinen Sprachcodes häufig Übersetzungen aus der Synchronfassung, während
//     "<sprache>-orig" die Untertitel der Original-Tonspur enthält. Nur diese
//     "-orig"-Spuren liefern zuverlässig Text.
//  2. yt-dlp bricht den gesamten Untertitel-Download ab, sobald eine Sprache
//     scheitert ("Unable to download video subtitles for 'en': HTTP Error
//     429"). Die reinen Codes antworten bei synchronisierten Videos oft mit
//     HTTP 429 – standen sie am Anfang der Liste, wurde keine einzige Datei
//     geladen. Mit --ignore-errors (siehe runSubsDownload) läuft der Download
//     ohnehin weiter; die Reihenfolge spart trotzdem Anfragen, weil zuerst die
//     funktionierenden Spuren geholt werden.
//
// Das Muster "<base>.*-orig" deckt auch Regionalformen wie "en-US-orig" ab.
// Die Liste bleibt minimal (keine Auto-Dub-Varianten wie "en-de-DE"), weil zu
// viele Anfragen ins HTTP-429-Limit laufen.
func narrowSubLangs(patterns []string) string {
	var orig, plain []string
	seen := map[string]bool{}
	add := func(dst *[]string, p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			*dst = append(*dst, p)
		}
	}
	for _, p := range patterns {
		base := strings.TrimSuffix(p, ".*")
		if base == p || base == "" {
			add(&plain, p) // kein Präfixmuster (z. B. "de-DE" oder ".*-orig")
			continue
		}
		add(&orig, base+".*-orig") // Untertitel der Original-Tonspur
		add(&plain, base)          // menschliche Untertitel bzw. Übersetzung
	}
	return strings.Join(append(orig, plain...), ",")
}

// normalizeSubLang erweitert einen reinen Sprachcode zum Präfixmuster.
func normalizeSubLang(p string) string {
	if !strings.ContainsAny(p, ".*+?[]()|^$\\") {
		return p + ".*"
	}
	return p
}

// langPatternRe kompiliert ein yt-dlp-Sprachmuster zu einem verankerten Regex.
// Ungültige Muster werden als Literal behandelt statt zu paniken.
func langPatternRe(pattern string) *regexp.Regexp {
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return regexp.MustCompile("(?i)^" + regexp.QuoteMeta(pattern) + "$")
	}
	return re
}

// srtLangTag extrahiert das Sprachkürzel aus einem yt-dlp-Untertitelnamen.
// "3SBf7hD0sEw.en-orig-fixed.srt" → "en-orig"
func srtLangTag(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	base = strings.TrimSuffix(base, "-fixed") // Zwilling des srt_fix-Plugins
	i := strings.LastIndex(base, ".")
	if i < 0 {
		return ""
	}
	return base[i+1:]
}

// subSelect beschreibt, welche heruntergeladenen Untertiteldateien zur Auswahl
// stehen und was über das Video bekannt ist.
type subSelect struct {
	Files       []string // Dateinamen im Untertitelverzeichnis
	Patterns    []string // gewünschte Sprachmuster in Prioritätsreihenfolge
	VideoLang   string   // gesprochene Sprache des Videos ("" = unbekannt)
	ManualLangs []string // Sprachen mit menschlichen Untertiteln (leer = unbekannt)
}

// pickSubtitle wählt aus mehreren heruntergeladenen Untertiteldateien die
// beste aus. Priorität:
//  1. Originalspur der im Video gesprochenen Sprache (videoLang): "en", "en-orig",
//     "en-US" oder "en-US-orig" – nicht aber Übersetzungen wie "en-de-DE"
//     (englischer Text aus deutscher Quelle).
//  2. "-orig"-Spur passend zur Musterreihenfolge.
//  3. irgendeine Spur in Musterreihenfolge (z. B. liegt nur eine Übersetzung vor)
//
// Innerhalb einer Sprache gewinnen menschliche Untertitel (ManualLangs) vor der
// automatischen Originalspur ("-orig") vor einer gewöhnlichen Auto-Spur: bei
// automatisch synchronisierten Videos ist der reine Sprachcode oft nur die
// Übersetzung der Synchronfassung, während "<sprache>-orig" die Originalspur
// enthält. Danach entscheiden die bereinigte SRT-Datei und SRT vor VTT.
//
// Passt keine Datei, wird "" geliefert: lieber kein Transkript als ein
// fremdsprachiges.
func pickSubtitle(s subSelect) string {
	if len(s.Files) == 0 {
		return ""
	}
	sorted := append([]string(nil), s.Files...)
	sort.Strings(sorted) // deterministisch, unabhängig von der Verzeichnisreihenfolge

	// Stufe 1: Originalspur der Videosprache (nur wenn die Sprache bekannt ist).
	if lang := baseLang(s.VideoLang); lang != "" && !strings.EqualFold(lang, "NA") {
		var hits []string
		for _, n := range sorted {
			if isOriginalTag(srtLangTag(n), lang) {
				hits = append(hits, n)
			}
		}
		if len(hits) > 0 {
			return bestSubtitle(hits, s.scorer(lang))
		}
	}

	// Stufe 2 + 3: Musterreihenfolge, erst mit "-orig", dann ohne Einschränkung.
	for _, wantOrig := range []bool{true, false} {
		for _, p := range s.Patterns {
			re := langPatternRe(p)
			var hits []string
			for _, n := range sorted {
				tag := srtLangTag(n)
				if tag == "" || !re.MatchString(tag) {
					continue
				}
				if wantOrig && !isOrigTag(tag) {
					continue
				}
				hits = append(hits, n)
			}
			if len(hits) > 0 {
				return bestSubtitle(hits, s.scorer(""))
			}
		}
	}
	return ""
}

// scorer liefert die Bewertungsfunktion für Dateien einer Sprache. lang ist die
// Videosprache ("" = unbekannt, dann zählt nur "-orig" als Originalspur).
func (s subSelect) scorer(lang string) func(string) [4]int {
	return func(name string) [4]int {
		tag := srtLangTag(name)
		human := 1
		if isManualLang(s.ManualLangs, tag) {
			human = 0 // menschliche Untertitel
		}
		rank := subTagRank(tag, lang)
		if lang == "" && isOrigTag(tag) {
			rank = 0 // ohne Videosprache zählt die Originalspur
		}
		lower := strings.ToLower(name)
		fixed := 1
		if strings.HasSuffix(lower, "-fixed.srt") {
			fixed = 0
		}
		ext := 1
		if strings.HasSuffix(lower, ".srt") {
			ext = 0
		}
		return [4]int{human, rank, fixed, ext}
	}
}

// isManualLang meldet, ob eine Sprache unter den menschlichen Untertiteln ist.
func isManualLang(langs []string, tag string) bool {
	for _, l := range langs {
		if strings.EqualFold(strings.TrimSpace(l), strings.TrimSpace(tag)) {
			return true
		}
	}
	return false
}

// trackOrigin beschreibt die Herkunft einer gewählten Untertitelspur für Log
// und Ausgabe.
func trackOrigin(manualLangs []string, tag string) string {
	switch {
	case isManualLang(manualLangs, tag):
		return "menschliche Untertitel"
	case isOrigTag(tag):
		return "Originalspur"
	default:
		return "Übersetzung"
	}
}

// isOrigTag erkennt Sprachkürzel mit Originalspur-Kennung: "en-orig",
// "en-US-orig", "pt-BR-orig".
func isOrigTag(tag string) bool {
	parts := strings.Split(strings.TrimSpace(tag), "-")
	return len(parts) > 1 && strings.EqualFold(parts[len(parts)-1], "orig")
}

// baseLang liefert den Sprachteil ohne Regionalzusatz ("de-DE" → "de").
func baseLang(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.Index(tag, "-"); i > 0 {
		return tag[:i]
	}
	return tag
}

// isOriginalTag prüft, ob ein Sprachkürzel die Originalspur der Sprache lang
// bezeichnet: "en", "en-orig", "en-US", "en-US-orig" – nicht aber Übersetzungen
// wie "en-de-DE" oder "en-en" (hier ist die Quelle eine andere Fassung).
func isOriginalTag(tag, lang string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	parts := strings.Split(tag, "-")
	if !strings.EqualFold(parts[0], lang) {
		return false
	}
	if len(parts) == 1 {
		return true // exakter Sprachcode
	}
	rest := parts[1:]
	if strings.EqualFold(rest[len(rest)-1], "orig") {
		rest = rest[:len(rest)-1] // Originalspur-Kennung entfernen
	}
	if len(rest) == 0 {
		return true // "en-orig"
	}
	// Regionalzusätze ("DE", "US", "419") oder Schriftsätze ("Latn", "Hans",
	// wie in "zh-Hans") sind keine Übersetzungen – Kleinbuchstaben dagegen
	// schon ("en-de-DE" = englischer Text aus deutscher Quelle).
	for _, r := range rest {
		if r == strings.ToUpper(r) || isScriptSubtag(r) {
			continue
		}
		return false
	}
	return true
}

// isScriptSubtag erkennt einen Schriftsatz wie "Latn" oder "Hans":
// vier Buchstaben, erster groß, Rest klein.
func isScriptSubtag(tag string) bool {
	runes := []rune(tag)
	if len(runes) != 4 || !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// subTagRank bewertet ein Sprachkürzel innerhalb derselben Sprache:
// 0 = Originalspur ("-orig", ASR der gesprochenen Tonspur), 1 = exakter Code
// oder Regionalvariante, 2 = Übersetzung/Sonstiges (z. B. "en-de-DE").
func subTagRank(tag, lang string) int {
	if tag == "" || !isOriginalTag(tag, lang) {
		return 2
	}
	if isOrigTag(tag) {
		return 0
	}
	return 1
}

// bestSubtitle wählt aus mehreren Dateien derselben Sprache die beste: zuerst
// nach Bewertung (menschlich, Originalspur, bereinigt, SRT vor VTT), dann
// alphabetisch.
func bestSubtitle(names []string, score func(string) [4]int) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := score(sorted[i]), score(sorted[j])
		for k := range si {
			if si[k] != sj[k] {
				return si[k] < sj[k]
			}
		}
		return sorted[i] < sorted[j]
	})
	return sorted[0]
}

// ─── Download ────────────────────────────────────────────────────────────────

// subsRequest beschreibt einen yt-dlp-Untertitel-Download.
type subsRequest struct {
	URL     string
	OutDir  string
	Langs   string // yt-dlp-Sprachmuster, kommagetrennt (leer → Config)
	Auto    bool   // automatische Untertitel laden
	Manual  bool   // menschliche Untertitel laden (schlägt Auto bei gleicher Sprache)
	Timeout time.Duration
}

// listSubtitleFiles liefert die Untertiteldateien (.srt/.vtt) in dir (Namen, sortiert).
// VTT wird mitgeführt, weil "srt"-Konvertierung ffmpeg benötigt und Spuren wie
// "de-DE" nur als VTT vorliegen; die Textkonvertierung erledigt Go (siehe
// convertSubtitleToSimple).
func listSubtitleFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if strings.HasSuffix(lower, ".srt") || strings.HasSuffix(lower, ".vtt") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// subsResult bündelt das Ergebnis eines Untertitel-Downloads.
type subsResult struct {
	Files       []string // neu erzeugte .srt/.vtt-Dateinamen
	VideoLang   string   // im Video gesprochene Sprache ("" wenn unbekannt)
	ManualLangs []string // Sprachen mit menschlichen Untertiteln ("" wenn keine)
	Output      string   // yt-dlp-Ausgabe (Warnungen/Fehler) für Log und Anzeige
}

// downloadSubtitles lädt Untertitel per yt-dlp herunter und gibt die dabei NEU
// erzeugten Untertiteldateien zurück.
//
// Verwendet dieselben Schritte wie die Transkript-Pipeline:
// --skip-download, --sub-format srt/vtt, --convert-subs srt, srt_fix-Plugin.
// Zusätzlich werden mit --print die Videosprache und die Sprachen mit
// menschlichen Untertiteln ermittelt (gleicher Aufruf, kein zweiter
// yt-dlp-Lauf) – sie entscheiden später, welche Spur gewinnt.
//
// Der Aufruf muss im Binärverzeichnis laufen, weil dort yt-dlp und das
// Plugin liegen.
func (a *App) downloadSubtitles(req subsRequest) (subsResult, error) {
	if req.Timeout <= 0 {
		req.Timeout = 60 * time.Second
	}
	langs := req.Langs
	if strings.TrimSpace(langs) == "" {
		langs = a.cfg.YouTubeSubLangs
	}
	if strings.TrimSpace(langs) == "" {
		langs = DefaultSubLangs
	}
	if err := os.MkdirAll(req.OutDir, 0755); err != nil {
		return subsResult{}, fmt.Errorf("create subtitle dir: %w", err)
	}

	patterns := subLangPatterns(langs)
	full := strings.Join(patterns, ",")
	narrow := narrowSubLangs(patterns)

	// Stufe 1: schmale Liste – die Originalspuren zuerst (wenige Requests).
	res, err := a.runSubsDownload(req, narrow)
	if len(res.Files) > 0 || narrow == full {
		return res, err
	}

	// Bei Rate-Limit (HTTP 429) ist ein zweiter, breiterer Versuch kontraproduktiv:
	// er verschärft das Limit und würde ohnehin scheitern. Die schmale Liste
	// enthält die Originalspuren (auch mit Regionalcode), deshalb scheitert der
	// Lauf nicht mehr an den 429-Antworten der reinen Codes.
	if strings.Contains(res.Output, "429") {
		log.Printf("[yt-dlp subs] Rate-Limit (429) – kein zweiter Versuch")
		return res, err
	}

	// Stufe 2: breite Muster, falls die Sprache nur als Variante vorliegt
	// (z.B. "de-DE" ohne einfaches "de").
	log.Printf("[yt-dlp subs] keine Spur für %s – breiter Versuch mit %s", narrow, full)
	wide, wideErr := a.runSubsDownload(req, full)
	if len(wide.Files) > 0 {
		return wide, nil
	}
	if wide.VideoLang == "" {
		wide.VideoLang = res.VideoLang
	}
	if wide.Output == "" {
		wide.Output = res.Output
	}
	if wideErr != nil {
		return wide, wideErr
	}
	return wide, err
}

// runSubsDownload führt einen yt-dlp-Lauf mit der übergebenen Sprachliste aus.
func (a *App) runSubsDownload(req subsRequest, langs string) (subsResult, error) {
	var res subsResult

	// Zieldateien sind persistent (Tool-Modus) – daher nur die neuen einsammeln.
	before := map[string]bool{}
	for _, n := range listSubtitleFiles(req.OutDir) {
		before[n] = true
	}

	args := []string{
		"--skip-download",
		"--no-playlist",
		// Einzelne Sprachen scheitern regelmäßig mit HTTP 429 (vor allem die
		// reinen Codes synchronisierter Videos). Ohne --ignore-errors bricht
		// yt-dlp den kompletten Untertitel-Download beim ersten Fehler ab und
		// die funktionierenden "-orig"-Spuren werden nie geladen.
		"--ignore-errors",
	}
	if req.Manual {
		args = append(args, "--write-subs")
	}
	if req.Auto {
		args = append(args, "--write-auto-subs")
	}
	if !req.Manual && !req.Auto {
		args = append(args, "--write-auto-subs") // Aufrufer hat nichts gewählt
	}
	args = append(args,
		"--sub-langs", langs,
		"--sub-format", "srt/vtt", // natives SRT bevorzugen (kein ffmpeg nötig)
		"--convert-subs", "srt", // Spuren ohne SRT-Variante konvertieren (braucht ffmpeg)
		"--use-postprocessor", "srt_fix:when=before_dl",
		"-o", filepath.Join(req.OutDir, "%(id)s"),
		"--no-simulate",
		"--print", "%(language)s", // gesprochene Sprache (steuert die Auswahl)
		"--print", "%(subtitles)j", // menschliche Untertitel (haben Vorrang)
		"--quiet",
		req.URL,
	)

	cmd := exec.Command(a.cfg.YTDLPPath, args...)
	cmd.Dir = getBinaryDir()

	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Run() }()

	var runErr error
	select {
	case runErr = <-errCh:
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-errCh
		return res, fmt.Errorf("yt-dlp subtitles timed out")
	}

	res.VideoLang = parseVideoLang(stdout.String())
	res.ManualLangs = parseManualSubLangs(stdout.String())
	res.Output = strings.TrimSpace(strings.Join([]string{
		stripSubsInfo(stdout.String()), stderr.String(),
	}, "\n"))

	for _, n := range listSubtitleFiles(req.OutDir) {
		if !before[n] {
			res.Files = append(res.Files, n)
		}
	}

	// Teilweise Fehler sind normal: YouTube liefert pro Sprachvariante eine
	// eigene Datei, einzelne scheitern mit HTTP 429. Solange mindestens eine
	// Datei angekommen ist, ist der Lauf brauchbar.
	if runErr != nil {
		if len(res.Files) == 0 {
			if msg := res.Output; msg != "" {
				return res, fmt.Errorf("yt-dlp subtitles failed: %v: %s", runErr, msg)
			}
			return res, fmt.Errorf("yt-dlp subtitles failed: %w", runErr)
		}
		log.Printf("[yt-dlp subs] Teilfehler ignoriert (%d Dateien geladen): %v", len(res.Files), runErr)
	}
	return res, nil
}

// parseVideoLang liest die mit --print "%(language)s" ausgegebene Videosprache.
// yt-dlp schreibt "NA", wenn das Feld fehlt.
func parseVideoLang(out string) string {
	re := regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "NA") {
			continue
		}
		if re.MatchString(line) {
			return line
		}
	}
	return ""
}

// parseManualSubLangs liest die mit --print "%(subtitles)j" ausgegebenen
// Sprachen menschlicher Untertitel. yt-dlp schreibt "NA", wenn es keine gibt.
func parseManualSubLangs(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var subs map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &subs); err != nil {
			continue
		}
		langs := make([]string, 0, len(subs))
		for lang := range subs {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		return langs
	}
	return nil
}

// stripSubsInfo entfernt die JSON-Zeile der --print-Ausgabe aus dem Logtext
// (sie enthält die vollständigen Untertitel-URLs und würde das Log fluten).
func stripSubsInfo(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// ─── Untertitel → Klartext ───────────────────────────────────────────────────

// vttCueRe erkennt WebVTT-Zeitmarken; Millisekunden sind dort mit "." getrennt.
var vttCueRe = regexp.MustCompile(`(?m)^(\d{1,2}:\d{2}:\d{2})[.,]\d{3}\s+-->\s+\d{1,2}:\d{2}:\d{2}[.,]\d{3}[^\n]*\n?`)

// vttTagRe entfernt Auszeichnungen wie <c>, </c>, <00:00:01.000> oder <b>.
var vttTagRe = regexp.MustCompile(`<[^>]*>`)

// convertSubtitleToSimple wandelt SRT- oder WebVTT-Inhalte in das im Programm
// verwendete "HH:MM:SS Text"-Format um.
//
// SRT kann direkt konvertiert werden; VTT fällt an, wenn ffmpeg zur
// SRT-Konvertierung fehlt oder eine Spur nur als VTT vorliegt.
func convertSubtitleToSimple(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if srtHeaderRe.MatchString(content) {
		return convertSRTToSimple(content)
	}
	return convertVTTToSimple(content)
}

// convertVTTToSimple baut aus WebVTT-Cues "HH:MM:SS Text"-Zeilen. YouTube
// wiederholt bei Rollzeilen die jeweils letzte Zeile im Folge-Cue – diese
// Wiederholungen werden entfernt (analog zum srt_fix-Plugin).
func convertVTTToSimple(content string) string {
	locs := vttCueRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return ""
	}

	var lines []string
	var prevLines []string
	for i, loc := range locs {
		start := content[loc[2]:loc[3]]
		textEnd := len(content)
		if i+1 < len(locs) {
			textEnd = locs[i+1][0]
		}

		cue := vttCueLines(content[loc[1]:textEnd])
		// Führende Rollzeilen verwerfen, die schon am Ende des Vor-Cues standen.
		for len(cue) > 0 && len(prevLines) > 0 && cue[0] == prevLines[len(prevLines)-1] {
			cue = cue[1:]
		}
		if len(cue) == 0 {
			continue // reine Wiederholung
		}
		lines = append(lines, start+" "+strings.Join(cue, " "))
		prevLines = cue
	}
	return strings.Join(lines, "\n")
}

// vttCueLines bereinigt die Textzeilen eines Cues: Auszeichnungen entfernen,
// Leerzeilen und direkt aufeinanderfolgende Wiederholungen verwerfen.
func vttCueLines(raw string) []string {
	var parts []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(vttTagRe.ReplaceAllString(line, ""))
		if line == "" || strings.HasPrefix(line, "NOTE") ||
			strings.HasPrefix(line, "Kind:") || strings.HasPrefix(line, "Language:") {
			continue
		}
		if len(parts) > 0 && parts[len(parts)-1] == line {
			continue
		}
		parts = append(parts, line)
	}
	return parts
}

// listAvailableSubs gibt die von yt-dlp gemeldeten Untertitelsprachen aus.
func (a *App) listAvailableSubs(url string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.cfg.YTDLPPath,
		"--skip-download", "--no-playlist", "--list-subs", url)
	cmd.Dir = getBinaryDir()

	out, err := cmd.CombinedOutput()
	return string(out), err
}
