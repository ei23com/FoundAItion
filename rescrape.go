package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ─── Einzelnen Eintrag neu einlesen ──────────────────────────────────────────
//
// Zwei getrennte Aktionen für die Detailansicht:
//
//	POST /api/links/rescrape?id=N    → Inhalt neu laden (Crawl bzw. Untertitel)
//	POST /api/links/resummarize?id=N → Zusammenfassung neu erstellen
//
// So lassen sich alte Einträge gezielt korrigieren, deren Inhalt z. B. in der
// falschen Sprache (etwa Arabisch) geladen wurde – ohne die Zusammenfassung
// ungewollt mit zu erneuern.

// rescrapeResponse ist die Antwort von POST /api/links/rescrape.
type rescrapeResponse struct {
	ID           int64    `json:"id"`
	Source       string   `json:"source"` // "youtube" oder "web"
	ContentChars int      `json:"content_chars"`
	TitleUpdated bool     `json:"title_updated,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// resummarizeResponse ist die Antwort von POST /api/links/resummarize.
type resummarizeResponse struct {
	ID             int64    `json:"id"`
	ContentChars   int      `json:"content_chars"`
	SummaryChars   int      `json:"summary_chars"`
	ContentFetched bool     `json:"content_fetched,omitempty"` // Inhalt musste erst geladen werden
	TitleUpdated   bool     `json:"title_updated,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// rescrapeLink behandelt POST /api/links/rescrape?id=N.
// Lädt ausschließlich den Inhalt neu (Webseite crawlen bzw. Untertitel holen)
// und lässt die Zusammenfassung unangetastet.
func (a *App) rescrapeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}

	link, err := a.fetchLinkByID(id, false)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Eintrag %d nicht gefunden", id))
		return
	}
	if strings.TrimSpace(link.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Eintrag %d hat keine URL", id))
		return
	}
	if a.linkJobBusy() {
		writeJSONError(w, http.StatusConflict, "Es läuft bereits eine Verarbeitung – bitte abwarten")
		return
	}

	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.setProgress(1, 0, 1, fmt.Sprintf("Inhalt wird neu geladen (Eintrag %d)…", id))
	defer a.finishLinkJob("Inhalt neu geladen")

	resp := rescrapeResponse{ID: id}
	if isYouTubeURL(link.URL) {
		resp.Source = "youtube"
	} else {
		resp.Source = "web"
	}

	content, pageTitle, cerr := a.fetchLinkContent(link)
	if cerr != nil {
		log.Printf("[rescrape] id=%d %s failed: %v", id, resp.Source, cerr)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Inhalt konnte nicht geladen werden: %v", cerr))
		return
	}
	if content == "" {
		writeJSONError(w, http.StatusBadGateway, "Kein Inhalt gefunden (leere Seite bzw. keine Untertitel)")
		return
	}

	if err := a.saveContent(id, content); err != nil {
		log.Printf("[rescrape] id=%d save content failed: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Inhalt konnte nicht gespeichert werden: %v", err))
		return
	}
	resp.ContentChars = len(content)

	// Der HTML-Titel ist immer die beste Titelquelle – beim Neuladen wird er
	// mitgeführt bzw. korrigiert (z. B. "Electrek Logo" → echter Seitentitel).
	if resp.Source == "web" {
		if title := truncateTitleMax(pageTitle, 150); title != "" && title != strings.TrimSpace(link.Title) {
			if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "title" = ? WHERE "id" = ?`, TableName), title, id); uerr != nil {
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("Titel nicht gespeichert: %v", uerr))
			} else {
				resp.TitleUpdated = true
			}
		}
	}

	log.Printf("[rescrape] id=%d %s content reloaded (%d chars), summary untouched", id, resp.Source, len(content))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// resummarizeLink behandelt POST /api/links/resummarize?id=N.
// Erstellt die Zusammenfassung aus dem aktuellen Inhalt neu (ersetzt den
// vorhandenen Text). Fehlt der Inhalt, wird er zuvor geladen.
func (a *App) resummarizeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseLinkID(w, r)
	if !ok {
		return
	}
	if a.cfg.OpenAIKey == "" {
		writeJSONError(w, http.StatusBadGateway, "Kein OPENAI_API_KEY konfiguriert – Zusammenfassung nicht möglich")
		return
	}

	link, err := a.fetchLinkByID(id, true) // mit Inhalt
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Eintrag %d nicht gefunden", id))
		return
	}
	if a.linkJobBusy() {
		writeJSONError(w, http.StatusConflict, "Es läuft bereits eine Verarbeitung – bitte abwarten")
		return
	}

	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	a.setProgress(2, 0, 1, "Zusammenfassung wird neu erstellt…")
	defer a.finishLinkJob("Zusammenfassung neu erstellt")

	resp := resummarizeResponse{ID: id}

	// Inhalt: vorhandenen nutzen, sonst laden (z. B. bei noch leerem Eintrag).
	content := strings.TrimSpace(link.Content)
	if content == "" {
		if strings.TrimSpace(link.URL) == "" {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Eintrag %d hat weder Inhalt noch URL", id))
			return
		}
		fetched, fetchedTitle, ferr := a.fetchLinkContent(link)
		if ferr != nil {
			log.Printf("[resummarize] id=%d content fetch failed: %v", id, ferr)
			writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Inhalt konnte nicht geladen werden: %v", ferr))
			return
		}
		if strings.TrimSpace(fetched) == "" {
			writeJSONError(w, http.StatusBadGateway, "Kein Inhalt vorhanden – bitte zuerst den Inhalt neu laden")
			return
		}
		content = fetched
		resp.ContentFetched = true
		// Fehlt der Titel komplett, gleich mit dem HTML-Titel füllen.
		if strings.TrimSpace(link.Title) == "" && strings.TrimSpace(fetchedTitle) != "" {
			if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "title" = ? WHERE "id" = ?`, TableName), truncateTitleMax(fetchedTitle, 150), id); uerr == nil {
				resp.TitleUpdated = true
			}
		}
		if err := a.saveContent(id, content); err != nil {
			log.Printf("[resummarize] id=%d save fetched content failed: %v", id, err)
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Inhalt konnte nicht gespeichert werden: %v", err))
			return
		}
	}
	resp.ContentChars = len(content)

	summary, serr := a.generateSummary(content, a.resolvePromptName(link.Note), a.cfg.UILanguage)
	if serr != nil {
		if strings.Contains(serr.Error(), ErrTooLong) {
			// Wie in der Pipeline: Platzhalter setzen, damit der Eintrag nicht hängen bleibt.
			if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "zu langer Text", id); uerr != nil {
				writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Zusammenfassung nicht gespeichert: %v", uerr))
				return
			}
			resp.SummaryChars = len("zu langer Text")
			resp.Warnings = append(resp.Warnings, "Inhalt überschreitet das Token-Limit")
			log.Printf("[resummarize] id=%d content too long, placeholder stored", id)
			writeJSON(w, resp)
			return
		}
		log.Printf("[resummarize] id=%d summary failed: %v", id, serr)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Zusammenfassung fehlgeschlagen: %v", serr))
		return
	}

	// Eine leere Modellantwort darf die vorhandene Zusammenfassung nicht löschen.
	if strings.TrimSpace(summary) == "" {
		log.Printf("[resummarize] id=%d model returned empty summary – keeping existing text", id)
		writeJSONError(w, http.StatusBadGateway, "Das Modell hat keine Zusammenfassung geliefert – der vorhandene Text bleibt erhalten")
		return
	}

	if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), summary, id); uerr != nil {
		log.Printf("[resummarize] id=%d summary update failed: %v", id, uerr)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Zusammenfassung nicht gespeichert: %v", uerr))
		return
	}
	resp.SummaryChars = len(summary)
	a.afterTextChange(id) // Vektor verwerfen/erneuern (Themenkarte)
	log.Printf("[resummarize] id=%d summary regenerated (%d chars from %d chars content)", id, len(summary), len(content))

	writeJSON(w, resp)
}

// getPrompts behandelt GET /api/prompts und liefert die verfügbaren
// Prompt-Templates (Dateinamen ohne ".md"). Die Detailansicht zeigt sie als
// Vorschläge für die Notiz an.
func (a *App) getPrompts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"prompts": a.promptNames()})
}

// ─── Helfer ──────────────────────────────────────────────────────────────────

// fetchLinkContent holt den Inhalt einer URL: Untertitel bei YouTube, sonst
// die gecrawlte Webseite. Gespeichert wird hier nichts. Der zweite Rückgabewert
// ist der HTML-Titel der Seite ("" bei YouTube bzw. wenn keiner ermittelbar).
func (a *App) fetchLinkContent(link *Link) (string, string, error) {
	if isYouTubeURL(link.URL) {
		transcript, err := a.resolveYouTubeTranscript(link.URL)
		if err != nil {
			return "", "", err
		}
		return strings.TrimSpace(transcript), "", nil
	}

	pageTitle, crawled, err := a.scrapeURL(link.URL)
	if err != nil {
		return "", "", err
	}
	content := strings.TrimSpace(crawled)
	// Wie beim Anlegen begrenzen, damit das Token-Limit nicht überschritten wird.
	if max := int(float64(a.cfg.SummaryMaxTokens) * 3.5); max > 0 && len(content) > max {
		content = content[:max]
	}
	return content, pageTitle, nil
}

// parseLinkID liest den id-Parameter; bei Fehlern antwortet sie bereits.
func parseLinkID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, `Parameter "id" fehlt oder ist ungültig`)
		return 0, false
	}
	return id, true
}

// linkJobBusy meldet, ob bereits eine Verarbeitung läuft.
func (a *App) linkJobBusy() bool {
	a.progress.mu.Lock()
	defer a.progress.mu.Unlock()
	return a.progress.Status == "running"
}

// setProgress aktualisiert den gemeinsamen Fortschrittszustand.
func (a *App) setProgress(phase, current, total int, message string) {
	a.progress.mu.Lock()
	defer a.progress.mu.Unlock()
	a.progress.Status = "running"
	a.progress.Phase = phase
	a.progress.Current = current
	a.progress.Total = total
	a.progress.Message = message
}

// finishLinkJob schließt den Fortschritt ab, damit das Overlay schließt.
func (a *App) finishLinkJob(message string) {
	a.progress.mu.Lock()
	defer a.progress.mu.Unlock()
	a.progress.Status = "done"
	a.progress.Phase = 0
	if a.progress.Total > 0 {
		a.progress.Current = a.progress.Total
	}
	a.progress.Message = message
}

// writeJSON schreibt eine JSON-Antwort.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// writeJSONError liefert einen Fehler als JSON (die UI zeigt die Meldung direkt an).
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
