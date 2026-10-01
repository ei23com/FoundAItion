package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLLM startet einen OpenAI-kompatiblen Testserver, der eine feste
// Zusammenfassung zurückgibt, und liefert dessen URL.
func fakeLLM(t *testing.T, summary string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Messages []map[string]string `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Messages) == 0 {
			t.Errorf("LLM-Aufruf ohne Nachrichten")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`, summary)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func llmConfig(baseURL string) Config {
	return Config{
		OpenAIKey:        "test-key",
		OpenAIBaseURL:    baseURL,
		OpenAIModel:      "test-model",
		SummaryMaxTokens: 64000,
	}
}

func resummarizeRequest(app *App, id int64, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, fmt.Sprintf("/api/links/resummarize?id=%d", id), nil)
	rec := httptest.NewRecorder()
	app.resummarizeLink(rec, req)
	return rec
}

func TestResummarizeValidation(t *testing.T) {
	app := testAppWithDB(t, llmConfig(fakeLLM(t, "neu")))

	// fehlende ID
	req := httptest.NewRequest(http.MethodPost, "/api/links/resummarize", nil)
	rec := httptest.NewRecorder()
	app.resummarizeLink(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ohne id: Status %d, erwartet 400", rec.Code)
	}

	// unbekannte ID
	if rec := resummarizeRequest(app, 4242, http.MethodPost); rec.Code != http.StatusNotFound {
		t.Fatalf("unbekannte id: Status %d, erwartet 404", rec.Code)
	}

	// falsche Methode
	if rec := resummarizeRequest(app, 1, http.MethodGet); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: Status %d, erwartet 405", rec.Code)
	}
}

func TestResummarizeRequiresAPIKey(t *testing.T) {
	app := testAppWithDB(t, Config{}) // ohne OPENAI_API_KEY
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "Inhalt")

	rec := resummarizeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Status %d, erwartet 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OPENAI_API_KEY") {
		t.Fatalf("Meldung nennt den fehlenden Schlüssel nicht: %s", rec.Body.String())
	}
}

func TestResummarizeBusyGuard(t *testing.T) {
	app := testAppWithDB(t, llmConfig(fakeLLM(t, "neu")))
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "Inhalt")

	app.progress.mu.Lock()
	app.progress.Status = "running"
	app.progress.mu.Unlock()

	if rec := resummarizeRequest(app, id, http.MethodPost); rec.Code != http.StatusConflict {
		t.Fatalf("laufender Job: Status %d, erwartet 409", rec.Code)
	}
}

// TestResummarizeReplacesSummary: vorhandene Zusammenfassung wird durch eine
// neu erzeugte ersetzt (nicht nur gelöscht).
func TestResummarizeReplacesSummary(t *testing.T) {
	const neu = "# Neue Zusammenfassung\n\nFrisch erzeugter Text."
	app := testAppWithDB(t, llmConfig(fakeLLM(t, neu)))
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "Der Inhalt des Eintrags.")
	if _, err := app.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "alte Zusammenfassung", id); err != nil {
		t.Fatalf("summary setzen: %v", err)
	}

	rec := resummarizeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d, Body: %s", rec.Code, rec.Body.String())
	}

	var resp resummarizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Antwort nicht lesbar: %v", err)
	}
	if resp.ContentFetched {
		t.Fatalf("content_fetched sollte false sein (Inhalt war vorhanden)")
	}
	if resp.SummaryChars != len(neu) {
		t.Fatalf("summary_chars = %d, erwartet %d", resp.SummaryChars, len(neu))
	}

	var summary string
	app.db.QueryRow(fmt.Sprintf(`SELECT "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&summary)
	if summary != neu {
		t.Fatalf("Zusammenfassung = %q, erwartet %q", summary, neu)
	}

	app.progress.mu.Lock()
	status := app.progress.Status
	app.progress.mu.Unlock()
	if status != "done" {
		t.Fatalf("Fortschrittsstatus = %q, erwartet \"done\"", status)
	}
}

// Bei leerem Inhalt wird er zuvor geladen (Crawl) und dann zusammengefasst.
func TestResummarizeFetchesMissingContent(t *testing.T) {
	page := `<html><head><title>Seite</title></head><body><main><p>Frischer Seiteninhalt für die Zusammenfassung, lang genug für die Inhaltsprüfung: Dieser Absatz beschreibt ausführlich, worum es auf der Seite geht, und enthält damit deutlich mehr als hundert Zeichen.</p></main></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	defer srv.Close()

	app := testAppWithDB(t, llmConfig(fakeLLM(t, "Zusammenfassung aus gecrawltem Inhalt")))
	id := insertTestLink(t, app, srv.URL, "", "") // kein Inhalt

	rec := resummarizeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d, Body: %s", rec.Code, rec.Body.String())
	}

	var resp resummarizeResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.ContentFetched {
		t.Fatalf("content_fetched sollte true sein")
	}
	if resp.ContentChars == 0 || resp.SummaryChars == 0 {
		t.Fatalf("erwartet Inhalt und Zusammenfassung, bekam %d/%d", resp.ContentChars, resp.SummaryChars)
	}

	var content, summary string
	app.db.QueryRow(fmt.Sprintf(`SELECT "content", "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&content, &summary)
	if !strings.Contains(content, "Frischer Seiteninhalt") {
		t.Fatalf("Inhalt wurde nicht gespeichert: %q", content)
	}
	if summary != "Zusammenfassung aus gecrawltem Inhalt" {
		t.Fatalf("Zusammenfassung = %q", summary)
	}
}

// Nicht erreichbare Seite ohne Inhalt → klare Fehlermeldung, nichts geändert.
func TestResummarizeNoContentAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	app := testAppWithDB(t, llmConfig(fakeLLM(t, "neu")))
	id := insertTestLink(t, app, deadURL, "Titel", "")
	if _, err := app.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "alte Zusammenfassung", id); err != nil {
		t.Fatalf("summary setzen: %v", err)
	}

	rec := resummarizeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Status %d, erwartet 502 (Body: %s)", rec.Code, rec.Body.String())
	}

	var summary string
	app.db.QueryRow(fmt.Sprintf(`SELECT "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&summary)
	if summary != "alte Zusammenfassung" {
		t.Fatalf("Zusammenfassung wurde trotz Fehler verändert: %q", summary)
	}
}

// Der LLM-Fehler darf die vorhandene Zusammenfassung nicht löschen.
func TestResummarizeKeepsSummaryOnLLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	app := testAppWithDB(t, llmConfig(srv.URL))
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "Inhalt")
	if _, err := app.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "alte Zusammenfassung", id); err != nil {
		t.Fatalf("summary setzen: %v", err)
	}

	if rec := resummarizeRequest(app, id, http.MethodPost); rec.Code != http.StatusBadGateway {
		t.Fatalf("Status %d, erwartet 502", rec.Code)
	}

	var summary string
	app.db.QueryRow(fmt.Sprintf(`SELECT "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&summary)
	if summary != "alte Zusammenfassung" {
		t.Fatalf("Zusammenfassung wurde bei Fehler verändert: %q", summary)
	}
}

// Eine leere Modellantwort darf die vorhandene Zusammenfassung nicht löschen
// (im Live-Test lieferte das Modell bei einem unpassenden Prompt "" zurück).
func TestResummarizeKeepsSummaryOnEmptyModelAnswer(t *testing.T) {
	app := testAppWithDB(t, llmConfig(fakeLLM(t, "   ")))
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "Inhalt")
	if _, err := app.db.Exec(fmt.Sprintf(`UPDATE %s SET "summary" = ? WHERE "id" = ?`, TableName), "alte Zusammenfassung", id); err != nil {
		t.Fatalf("summary setzen: %v", err)
	}

	rec := resummarizeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Status %d, erwartet 502 (Body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "keine Zusammenfassung") {
		t.Fatalf("Meldung unklar: %s", rec.Body.String())
	}

	var summary string
	app.db.QueryRow(fmt.Sprintf(`SELECT "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&summary)
	if summary != "alte Zusammenfassung" {
		t.Fatalf("Zusammenfassung wurde geleert: %q", summary)
	}
}
