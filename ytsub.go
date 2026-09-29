package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── yt-sub: manueller Untertitel-Download ───────────────────────────────────
//
// Fallback-Werkzeug zur automatischen Transkript-Pipeline. Lädt Untertitel mit
// yt-dlp in einer frei wählbaren Sprache, speichert die SRT-Datei und legt
// zusätzlich die konvertierte "HH:MM:SS Text"-Fassung ab – genau das Format,
// das die Pipeline in der Spalte "content" ablegt. Ist die URL in der Datenbank
// vorhanden, wird das Transkript dort eingetragen, damit Zusammenfassung und
// Karte es weiterverwenden können.
//
// Aufruf: yt-sub <sprache> <url> [optionen]

const ytSubUsage = `yt-sub – YouTube-Untertitel manuell herunterladen (nutzt yt-dlp)

Aufruf:
  yt-sub <sprache> <url> [optionen]

Beispiele:
  yt-sub en "https://www.youtube.com/watch?v=XXXXXXXXXXX"
  yt-sub de "https://youtu.be/XXXXXXXXXXX"
  yt-sub en,de "https://www.youtube.com/watch?v=XXXXXXXXXXX"
  yt-sub --list "https://www.youtube.com/watch?v=XXXXXXXXXXX"

Sprache:
  Sprachcode (en, de) oder yt-dlp-Muster (z. B. "en.*", "de-DE", ".*-orig").
  Priorität bei mehreren Angaben: erst Originalspur ("-orig"), dann Reihenfolge.
  Mehrere Sprachen mit Komma trennen.

Optionen:
  --out DIR      Zielverzeichnis (Default: YT_SUBS_DIR bzw. ./subs)
  --manual       nur menschliche Untertitel (--write-subs)
  --auto         nur automatische Untertitel (Default: erst auto, dann manual)
  --list         verfügbare Untertitelsprachen des Videos anzeigen
  --no-db        Transkript nicht in die Datenbank schreiben (nur Dateien)
  --timeout SEC  Zeitlimit pro yt-dlp-Aufruf (Default: 120)
  -h, --help     diese Hilfe

Ausgabe (im Zielverzeichnis):
  <videoID>.<sprache>-fixed.srt   bereinigte SRT-Datei (srt_fix-Plugin)
  <videoID>.<sprache>.txt         konvertiertes "HH:MM:SS Text"-Transkript
`

// splitFlagsAndArgs trennt Flags von den Positionsargumenten (<sprache>, <url>),
// unabhängig von deren Reihenfolge. Flags mit Wert (--out, --timeout) nehmen
// den Folgewert mit, sofern er nicht selbst ein Flag ist.
func splitFlagsAndArgs(args []string) (flagArgs, posArgs []string) {
	valueFlags := map[string]bool{"-out": true, "--out": true, "-timeout": true, "--timeout": true}

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" { // Rest sind Positionsargumente
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		if len(a) > 1 && strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		posArgs = append(posArgs, a)
	}
	return flagArgs, posArgs
}

// subAttempt beschreibt einen Download-Versuch.
type subAttempt struct {
	auto   bool // automatische Untertitel laden
	manual bool // menschliche Untertitel laden
	label  string
}

// ytSubOptions bündelt die Kommandozeilen-Optionen.
type ytSubOptions struct {
	OutDir  string
	Manual  bool
	Auto    bool
	List    bool
	NoDB    bool
	Timeout int
	Lang    string
	URL     string
}

// runYTSub führt das yt-sub-Werkzeug aus und liefert den Exit-Code.
func runYTSub(args []string) int {
	fs := flag.NewFlagSet("yt-sub", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, ytSubUsage) }

	var opt ytSubOptions
	fs.StringVar(&opt.OutDir, "out", "", "Zielverzeichnis")
	fs.BoolVar(&opt.Manual, "manual", false, "nur menschliche Untertitel")
	fs.BoolVar(&opt.Auto, "auto", false, "nur automatische Untertitel")
	fs.BoolVar(&opt.List, "list", false, "verfügbare Sprachen anzeigen")
	fs.BoolVar(&opt.NoDB, "no-db", false, "nicht in die Datenbank schreiben")
	fs.IntVar(&opt.Timeout, "timeout", 120, "Zeitlimit in Sekunden")

	// Go's flag-Paket beendet das Parsen beim ersten Nicht-Flag. Damit auch
	// "yt-sub en <url> --no-db" funktioniert, werden Flags vorher herausgezogen.
	flagArgs, posArgs := splitFlagsAndArgs(args)

	if err := fs.Parse(flagArgs); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	rest := posArgs
	if opt.List {
		// --list erlaubt "yt-sub --list <url>" und "yt-sub <sprache> --list <url>"
		if len(rest) == 0 {
			fmt.Fprint(os.Stderr, ytSubUsage)
			return 2
		}
		opt.URL = rest[len(rest)-1]
		if len(rest) > 1 {
			opt.Lang = rest[0]
		}
	} else {
		if len(rest) < 2 {
			fmt.Fprint(os.Stderr, ytSubUsage)
			return 2
		}
		opt.Lang = rest[0]
		opt.URL = rest[1]
	}

	if opt.Manual && opt.Auto {
		fmt.Fprintln(os.Stderr, "Fehler: --manual und --auto schließen sich aus.")
		return 2
	}

	cfg := loadConfig()
	if opt.OutDir == "" {
		opt.OutDir = cfg.YTDSubsDir
	}
	if opt.OutDir == "" {
		opt.OutDir = filepath.Join(getBinaryDir(), "subs")
	}

	// ─── Sprachen auflisten ───────────────────────────────────────────────
	if opt.List {
		fmt.Printf("Verfügbare Untertitel für %s:\n\n", opt.URL)
		out, err := (&App{cfg: cfg}).listAvailableSubs(opt.URL, time.Duration(opt.Timeout)*time.Second)
		if err != nil && strings.TrimSpace(out) == "" {
			fmt.Fprintf(os.Stderr, "yt-dlp --list-subs fehlgeschlagen: %v\n", err)
			return 1
		}
		fmt.Println(strings.TrimRight(out, "\n"))
		return 0
	}

	// ─── Datenbank (optional) ─────────────────────────────────────────────
	var db *sql.DB
	if !opt.NoDB {
		var err error
		db, err = openDatabase(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warnung: Datenbank nicht verfügbar (%v) – Transkript wird nur als Datei gespeichert.\n", err)
			db = nil
		} else {
			defer db.Close()
		}
	}

	app := &App{db: db, cfg: cfg}
	if code := runYTSubDownload(app, opt); code != 0 {
		return code
	}
	return 0
}

// runYTSubDownload lädt die Untertitel, speichert sie und gibt das Ergebnis aus.
func runYTSubDownload(app *App, opt ytSubOptions) int {
	patterns := subLangPatterns(opt.Lang)
	langList := strings.Join(patterns, ",")

	// Standard: beide Quellen in einem Aufruf – menschliche Untertitel haben
	// Vorrang, automatische dienen als Fallback. --manual/--auto erzwingen eine
	// Quelle. Zusätzliche Versuche helfen, wenn einzelne Spuren an YouTubes
	// Rate-Limit (HTTP 429) scheitern.
	var attempts []subAttempt
	switch {
	case opt.Manual:
		attempts = []subAttempt{{manual: true, label: "manuell"}}
	case opt.Auto:
		attempts = []subAttempt{{auto: true, label: "automatisch"}}
	default:
		attempts = []subAttempt{
			{auto: true, manual: true, label: "manuell + automatisch"},
			{auto: true, label: "automatisch"},
			{manual: true, label: "manuell"},
		}
	}

	if err := os.MkdirAll(opt.OutDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Zielverzeichnis nicht nutzbar: %v\n", err)
		return 1
	}

	var (
		files     []string
		videoLang string
	)
	for _, a := range attempts {
		fmt.Printf("→ yt-dlp: %s Untertitel (%s) …\n", a.label, langList)
		res, err := app.downloadSubtitles(subsRequest{
			URL:     opt.URL,
			OutDir:  opt.OutDir,
			Langs:   langList,
			Auto:    a.auto,
			Manual:  a.manual,
			Timeout: time.Duration(opt.Timeout) * time.Second,
		})
		if err != nil {
			if strings.TrimSpace(res.Output) != "" {
				fmt.Fprintln(os.Stderr, strings.TrimSpace(res.Output))
			}
			fmt.Fprintf(os.Stderr, "yt-dlp-Fehler (%s): %v\n", a.label, err)
			continue
		}
		if len(res.Files) > 0 {
			files = res.Files
			videoLang = res.VideoLang
			break
		}
		fmt.Printf("  keine %s Untertitel in %s gefunden\n", a.label, langList)
	}

	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "\nKeine Untertitel in %s gefunden.\n", langList)
		fmt.Fprintf(os.Stderr, "Tipp: verfügbare Sprachen anzeigen mit\n  yt-sub --list \"%s\"\n", opt.URL)
		return 1
	}

	best := pickSubtitleFile(files, patterns, videoLang)
	if best == "" {
		fmt.Fprintf(os.Stderr, "\nKeine passende Spur gefunden (Videosprache: %q).\n", videoLang)
		fmt.Fprintf(os.Stderr, "Geladen wurden: %s\n", strings.Join(files, ", "))
		fmt.Fprintf(os.Stderr, "Tipp: Sprache direkt angeben, z. B.\n  yt-sub ar \"%s\"\n", opt.URL)
		return 1
	}

	// Untertitel einlesen und wie in der Pipeline in "HH:MM:SS Text" konvertieren.
	srtPath := filepath.Join(opt.OutDir, best)
	data, err := os.ReadFile(srtPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Untertiteldatei nicht lesbar: %v\n", err)
		return 1
	}
	subContent := strings.TrimSpace(string(data))
	simple := convertSubtitleToSimple(subContent)
	if strings.TrimSpace(simple) == "" {
		fmt.Fprintln(os.Stderr, "Untertiteldatei enthält keinen verwertbaren Text.")
		return 1
	}

	langTag := srtLangTag(best)
	videoID := strings.SplitN(strings.TrimSuffix(best, filepath.Ext(best)), ".", 2)[0]
	txtPath := filepath.Join(opt.OutDir, fmt.Sprintf("%s.%s.txt", videoID, langTag))
	if err := os.WriteFile(txtPath, []byte(simple), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Transkript nicht schreibbar: %v\n", err)
		return 1
	}

	// ─── Ergebnis ─────────────────────────────────────────────────────────
	fmt.Printf("\n✓ Sprache: %s (Videosprache: %s, %d → %d Zeichen)\n", langTag, videoLangOrUnknown(videoLang), len(subContent), len(simple))
	fmt.Printf("  Datei:      %s\n", srtPath)
	fmt.Printf("  Transkript: %s\n", txtPath)

	if app.db == nil {
		return 0
	}
	saved, err := saveTranscriptByURL(app, opt.URL, simple)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "  Datenbank: Fehler beim Speichern: %v\n", err)
		return 1
	case saved:
		fmt.Printf("  Datenbank: Transkript in \"content\" gespeichert\n")
	default:
		fmt.Printf("  Datenbank: kein Eintrag zu dieser URL gefunden – nur Dateien geschrieben\n")
		fmt.Printf("             (Link zuerst in FoundAItion anlegen oder --no-db nutzen)\n")
	}
	return 0
}

// videoLangOrUnknown macht eine leere Videosprache lesbar.
func videoLangOrUnknown(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return "unbekannt"
	}
	return lang
}

// saveTranscriptByURL sucht den Link zur URL und schreibt das Transkript in
// die Spalte "content". Sucht zuerst exakt, dann über die kanonische
// YouTube-URL (youtu.be/… und watch?v=…&list=… zeigen auf denselben Eintrag).
func saveTranscriptByURL(app *App, url, transcript string) (bool, error) {
	candidates := []string{url}
	if canonical := extractYouTubeCanonicalURL(url); canonical != "" && canonical != url {
		candidates = append(candidates, canonical)
	}

	for _, u := range candidates {
		var id int64
		q := fmt.Sprintf(`SELECT "id" FROM %s WHERE "url" = ? LIMIT 1`, TableName)
		err := app.db.QueryRow(q, u).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return false, err
		}
		if err := app.saveContent(id, transcript); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
