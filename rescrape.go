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
// Lädt den Inhalt eines bestehenden Eintrags neu – Webseite erneut crawlen
// bzw. Untertitel erneut holen – und erstellt die Zusammenfassung neu. Damit
// lassen sich alte Einträge korrigieren, deren Inhalt z. B. in der falschen
// Sprache (etwa Arabisch) geladen wurde.

// rescrapeResponse ist die Antwort von POST /api/links/rescrape.
type rescrapeResponse struct {
	ID           int64    `json:"id"`
	Source       string   `json:"source"` // "youtube" oder "web"
	ContentChars int      `json:"content_chars"`
	SummaryChars int      `json:"summary_chars"`
	TitleUpdated bool     `json:"title_updated,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// rescrapeLink behandelt POST /api/links/rescrape?id=N[&summary=0].
//
// Läuft synchron (der Aufrufer pollt /api/progress wie beim Sammel-Job).
// Mit summary=0 wird nur der Inhalt neu geladen.
func (a *App) rescrapeLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, `Parameter "id" fehlt oder ist ungültig`)
		return
	}
	withSummary := r.URL.Query().Get("summary") != "0"

	link, err := a.fetchLinkByID(id, false)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Eintrag %d nicht gefunden", id))
		return
	}
	if strings.TrimSpace(link.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Eintrag %d hat keine URL", id))
		return
	}

	// Nur ein inhaltlicher Job gleichzeitig: lokale Modellserver halten meist
	// nur ein Modell geladen, und der Fortschrittsbalken ist gemeinsam genutzt.
	a.progress.mu.Lock()
	busy := a.progress.Status == "running"
	a.progress.mu.Unlock()
	if busy {
		writeJSONError(w, http.StatusConflict, "Es läuft bereits eine Verarbeitung – bitte abwarten")
		return
	}

	a.llmMu.Lock()
	defer a.llmMu.Unlock()

	a.setProgress(1, 0, 1, fmt.Sprintf("Inhalt wird neu geladen (Eintrag %d)…", id))
	defer func() {
		a.setProgress(0, 1, 1, "Fertig")
		a.progress.mu.Lock()
		a.progress.Status = "done"
		a.progress.mu.Unlock()
	}()

	resp := rescrapeResponse{ID: id}
	web := !isYouTubeURL(link.URL)

	// ─── Schritt 1: Inhalt neu laden ──────────────────────────────────────
	var content string
	if web {
		resp.Source = "web"
		crawled, cerr := a.crawlPage(link.URL)
		if cerr != nil {
			log.Printf("[rescrape] id=%d crawl failed: %v", id, cerr)
			writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Webseite konnte nicht geladen werden: %v", cerr))
			return
		}
		content = strings.TrimSpace(crawled)
		// Wie beim Anlegen begrenzen, damit das Token-Limit nicht überschritten wird.
		if max := int(float64(a.cfg.SummaryMaxTokens) * 3.5); max > 0 && len(content) > max {
			content = content[:max]
		}
		// Titel nur ergänzen, niemals einen vorhandenen (ggf. bearbeiteten) überschreiben.
		if strings.TrimSpace(link.Title) == "" {
			if title := truncateTitleMax(extractTitleFromMarkdown(content), 150); title != "" {
				if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "title" = ? WHERE "id" = ?`, TableName), title, id); uerr == nil {
					resp.TitleUpdated = true
				}
			}
		}
	} else {
		resp.Source = "youtube"
		transcript, terr := a.resolveYouTubeTranscript(link.URL)
		if terr != nil {
			log.Printf("[rescrape] id=%d transcript failed: %v", id, terr)
			writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Untertitel konnten nicht geladen werden: %v", terr))
			return
		}
		content = strings.TrimSpace(transcript)
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
	log.Printf("[rescrape] id=%d %s content reloaded (%d chars)", id, resp.Source, len(content))

	// ─── Schritt 2: Zusammenfassung neu erstellen ─────────────────────────
	switch {
	case !withSummary:
		resp.Warnings = append(resp.Warnings, "Zusammenfassung unverändert (summary=0)")
	case a.cfg.OpenAIKey == "":
		resp.Warnings = append(resp.Warnings, "Kein OPENAI_API_KEY konfiguriert – Zusammenfassung nicht neu erstellt")
	default:
		a.setProgress(2, 0, 1, "Zusammenfassung wird neu erstellt…")
		summary, serr := a.generateSummary(content, resolvePrompt(link.Note), a.cfg.UILanguage)
		if serr != nil {
			if strings.Contains(serr.Error(), ErrTooLong) {
				if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "zu langer Text", id); uerr == nil {
					resp.SummaryChars = len("zu langer Text")
				}
				resp.Warnings = append(resp.Warnings, "Inhalt überschreitet das Token-Limit")
			} else {
				log.Printf("[rescrape] id=%d summary failed: %v", id, serr)
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("Zusammenfassung fehlgeschlagen: %v", serr))
			}
			break
		}
		if _, uerr := a.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), summary, id); uerr != nil {
			log.Printf("[rescrape] id=%d summary update failed: %v", id, uerr)
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("Zusammenfassung nicht gespeichert: %v", uerr))
			break
		}
		resp.SummaryChars = len(summary)
		a.afterTextChange(id) // Vektor verwerfen/erneuern (Themenkarte)
		log.Printf("[rescrape] id=%d summary regenerated (%d chars)", id, len(summary))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
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

// writeJSONError liefert einen Fehler als JSON (die UI zeigt die Meldung direkt an).
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
