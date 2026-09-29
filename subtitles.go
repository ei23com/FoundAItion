package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// narrowSubLangs leitet aus den Mustern eine minimale Anforderungsliste ab:
// "en.*" → "en,en-orig". So werden nur die Originalspuren angefragt statt
// aller Auto-Dub-Varianten (en-de-DE, en-ja, …). Das schont YouTubes
// Ratelimit – zu viele Anfragen führen sonst zu HTTP 429 und der Lauf
// liefert gar keine Untertitel.
func narrowSubLangs(patterns []string) string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range patterns {
		base := strings.TrimSuffix(p, ".*")
		if base == p || base == "" {
			add(p) // kein Präfixmuster (z. B. "de-DE" oder ".*-orig")
			continue
		}
		add(base)           // exakter Code (meist menschliche Untertitel)
		add(base + "-orig") // gesprochene Originalspur
	}
	return strings.Join(out, ",")
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

// pickSubtitleFile wählt aus mehreren heruntergeladenen Untertiteldateien die
// beste aus. Priorität:
//  1. Originalspur der im Video gesprochenen Sprache (videoLang): exakter Code,
//     "<lang>-orig" oder Regionalvariante wie "de-DE" – nicht aber Übersetzungen
//     wie "en-de-DE" (englischer Text aus deutscher Quelle).
//  2. "-orig"-Spur passend zur Musterreihenfolge.
//  3. irgendeine Spur in Musterreihenfolge (z. B. liegt nur eine Übersetzung vor)
//
// Passt keine Datei, wird "" geliefert: lieber kein Transkript als ein
// fremdsprachiges. Innerhalb einer Sprache gewinnt die bereinigte SRT-Datei,
// danach SRT vor VTT.
func pickSubtitleFile(names []string, patterns []string, videoLang string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted) // deterministisch, unabhängig von der Verzeichnisreihenfolge

	// Stufe 1: Originalspur der Videosprache (nur wenn die Sprache bekannt ist).
	if lang := baseLang(videoLang); lang != "" && !strings.EqualFold(lang, "NA") {
		var hits []string
		for _, n := range sorted {
			if isOriginalTag(srtLangTag(n), lang) {
				hits = append(hits, n)
			}
		}
		if len(hits) > 0 {
			return bestSubtitle(hits, lang)
		}
	}

	// Stufe 2 + 3: Musterreihenfolge, erst mit "-orig", dann ohne Einschränkung.
	for _, wantOrig := range []bool{true, false} {
		for _, p := range patterns {
			re := langPatternRe(p)
			var hits []string
			for _, n := range sorted {
				tag := srtLangTag(n)
				if tag == "" || !re.MatchString(tag) {
					continue
				}
				if wantOrig && !strings.HasSuffix(strings.ToLower(tag), "-orig") {
					continue
				}
				hits = append(hits, n)
			}
			if len(hits) > 0 {
				return bestSubtitle(hits, "")
			}
		}
	}
	return ""
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
// bezeichnet: "en", "en-orig", "en-US", "de-DE" – nicht aber Übersetzungen wie
// "en-de-DE" oder "en-en" (hier ist die Quelle eine andere Fassung).
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
	rest := strings.Join(parts[1:], "-")
	if strings.EqualFold(rest, "orig") {
		return true // gesprochene Originalspur
	}
	// Regionalvariante: Zusatz ohne Kleinbuchstaben ("DE", "US", "419")
	return rest == strings.ToUpper(rest)
}

// subTagRank bewertet ein Sprachkürzel innerhalb derselben Sprache:
// 0 = exakter Code (meist menschliche Untertitel), 1 = "-orig" (automatisch
// erzeugte Originalspur), 2 = Regionalvariante, 3 = Übersetzung/Sonstiges.
func subTagRank(tag, lang string) int {
	if tag == "" || !isOriginalTag(tag, lang) {
		return 3
	}
	parts := strings.Split(tag, "-")
	if len(parts) == 1 {
		return 0
	}
	rest := strings.Join(parts[1:], "-")
	if strings.EqualFold(rest, "orig") {
		return 1
	}
	return 2
}

// bestSubtitle wählt aus mehreren Dateien derselben Sprache die beste:
// zuerst nach Sprachrang, dann bereinigte SRT-Datei, dann SRT vor VTT.
func bestSubtitle(names []string, lang string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := subtitleScore(sorted[i], lang), subtitleScore(sorted[j], lang)
		for k := range si {
			if si[k] != sj[k] {
				return si[k] < sj[k]
			}
		}
		return sorted[i] < sorted[j]
	})
	return sorted[0]
}

// subtitleScore liefert die Sortierkriterien: Rang, bereinigt, SRT vor VTT.
func subtitleScore(name, lang string) [3]int {
	lower := strings.ToLower(name)
	fixed := 1
	if strings.HasSuffix(lower, "-fixed.srt") {
		fixed = 0
	}
	ext := 1
	if strings.HasSuffix(lower, ".srt") {
		ext = 0
	}
	return [3]int{subTagRank(srtLangTag(name), lang), fixed, ext}
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
	Files     []string // neu erzeugte .srt/.vtt-Dateinamen
	VideoLang string   // im Video gesprochene Sprache ("" wenn unbekannt)
	Output    string   // yt-dlp-Ausgabe (Warnungen/Fehler) für Log und Anzeige
}

// downloadSubtitles lädt Untertitel per yt-dlp herunter und gibt die dabei NEU
// erzeugten Untertiteldateien zurück.
//
// Verwendet dieselben Schritte wie die Transkript-Pipeline:
// --skip-download, --sub-format srt/vtt, --convert-subs srt, srt_fix-Plugin.
// Zusätzlich wird mit --print die Videosprache ermittelt (gleicher Aufruf, kein
// zweiter yt-dlp-Lauf) – sie entscheidet später, welche Spur gewinnt.
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

	// Stufe 1: nur die Originalspuren anfragen (wenige Requests).
	res, err := a.runSubsDownload(req, narrow)
	if len(res.Files) > 0 || narrow == full {
		return res, err
	}

	// Bei Rate-Limit (HTTP 429) ist ein zweiter, breiterer Versuch kontraproduktiv:
	// er verschärft das Limit und würde ohnehin scheitern.
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
		"--no-simulate", "--print", "%(language)s",
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
	res.Output = strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))

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
