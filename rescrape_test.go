package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// testAppWithDB baut eine App auf einer In-Memory-Datenbank.
func testAppWithDB(t *testing.T, cfg Config) *App {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureTableExists(db, TableName); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	if err := ensureVectorColumns(db, TableName); err != nil {
		t.Fatalf("ensure vector columns: %v", err)
	}
	if cfg.CrawlTimeoutSec <= 0 {
		cfg.CrawlTimeoutSec = 10
	}
	if cfg.SummaryMaxTokens <= 0 {
		cfg.SummaryMaxTokens = 64000
	}
	return &App{db: db, cfg: cfg}
}

func insertTestLink(t *testing.T, app *App, url, title, content string) int64 {
	t.Helper()
	q := fmt.Sprintf(`INSERT INTO %s ("timestamp", "url", "title", "content") VALUES ('2026-01-01T00:00:00Z', ?, ?, ?)`, TableName)
	res, err := app.db.Exec(q, url, title, content)
	if err != nil {
		t.Fatalf("insert link: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func rescrapeRequest(app *App, id int64, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, fmt.Sprintf("/api/links/rescrape?id=%d", id), nil)
	rec := httptest.NewRecorder()
	app.rescrapeLink(rec, req)
	return rec
}

func TestRescrapeValidation(t *testing.T) {
	app := testAppWithDB(t, Config{})

	// fehlende ID
	req := httptest.NewRequest(http.MethodPost, "/api/links/rescrape", nil)
	rec := httptest.NewRecorder()
	app.rescrapeLink(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ohne id: Status %d, erwartet 400", rec.Code)
	}

	// unbekannte ID
	rec = rescrapeRequest(app, 9999, http.MethodPost)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unbekannte id: Status %d, erwartet 404", rec.Code)
	}

	// falsche Methode
	rec = rescrapeRequest(app, 1, http.MethodGet)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: Status %d, erwartet 405", rec.Code)
	}
}

func TestRescrapeBusyGuard(t *testing.T) {
	app := testAppWithDB(t, Config{})
	id := insertTestLink(t, app, "https://example.org/a", "Titel", "alter inhalt")

	app.progress.mu.Lock()
	app.progress.Status = "running"
	app.progress.mu.Unlock()

	rec := rescrapeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusConflict {
		t.Fatalf("laufender Job: Status %d, erwartet 409", rec.Code)
	}
}

// TestRescrapeWebPage lädt eine Webseite über einen lokalen Testserver neu und
// prüft, dass der alte Inhalt ersetzt und der leere Titel ergänzt wird.
func TestRescrapeWebPage(t *testing.T) {
	page := `<!doctype html><html><head><title>Neuer Seitentitel</title></head>
	<body><main><h1>Neuer Seitentitel</h1><p>Frischer Inhalt der Webseite mit ausreichend Text.</p></main></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})                               // ohne OPENAI_API_KEY → Zusammenfassung wird übersprungen
	id := insertTestLink(t, app, srv.URL, "", "النص العربي القديم") // "alter arabischer Text"

	rec := rescrapeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d, Body: %s", rec.Code, rec.Body.String())
	}

	var resp rescrapeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Antwort nicht lesbar: %v (%s)", err, rec.Body.String())
	}
	if resp.Source != "web" {
		t.Fatalf("source = %q, erwartet \"web\"", resp.Source)
	}
	if resp.ContentChars == 0 {
		t.Fatalf("kein Inhalt geladen")
	}

	var content, title string
	if err := app.db.QueryRow(fmt.Sprintf(`SELECT "content", "title" FROM %s WHERE "id" = ?`, TableName), id).Scan(&content, &title); err != nil {
		t.Fatalf("select: %v", err)
	}
	if strings.Contains(content, "العربي") {
		t.Fatalf("alter Inhalt wurde nicht ersetzt: %q", content)
	}
	if !strings.Contains(content, "Frischer Inhalt") {
		t.Fatalf("neuer Inhalt fehlt: %q", content)
	}
	if title != "Neuer Seitentitel" {
		t.Fatalf("Titel = %q, erwartet \"Neuer Seitentitel\"", title)
	}
	if !resp.TitleUpdated {
		t.Fatalf("title_updated sollte true sein")
	}
	if len(resp.Warnings) == 0 {
		t.Fatalf("Warnung zum fehlenden OPENAI_API_KEY erwartet")
	}

	// Fortschritt muss wieder freigegeben sein (Overlay schließt).
	app.progress.mu.Lock()
	status := app.progress.Status
	app.progress.mu.Unlock()
	if status != "done" {
		t.Fatalf("Fortschrittsstatus = %q, erwartet \"done\"", status)
	}
}

// Ein vorhandener (ggf. bearbeiteter) Titel darf nicht überschrieben werden.
func TestRescrapeKeepsExistingTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Seitentitel</title></head><body><main>Inhalt</main></body></html>`)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})
	id := insertTestLink(t, app, srv.URL, "Mein eigener Titel", "alt")

	if rec := rescrapeRequest(app, id, http.MethodPost); rec.Code != http.StatusOK {
		t.Fatalf("Status %d: %s", rec.Code, rec.Body.String())
	}

	var title string
	app.db.QueryRow(fmt.Sprintf(`SELECT "title" FROM %s WHERE "id" = ?`, TableName), id).Scan(&title)
	if title != "Mein eigener Titel" {
		t.Fatalf("Titel wurde überschrieben: %q", title)
	}
}

// Nicht erreichbare Seite → Fehler mit klarer Meldung, alter Inhalt bleibt erhalten.
func TestRescrapeUnreachablePageKeepsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close() // Server sofort schließen → Verbindung schlägt fehl

	app := testAppWithDB(t, Config{CrawlTimeoutSec: 2})
	id := insertTestLink(t, app, deadURL, "Titel", "bewahrter Inhalt")

	rec := rescrapeRequest(app, id, http.MethodPost)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Status %d, erwartet 502 (Body: %s)", rec.Code, rec.Body.String())
	}

	var content string
	app.db.QueryRow(fmt.Sprintf(`SELECT "content" FROM %s WHERE "id" = ?`, TableName), id).Scan(&content)
	if content != "bewahrter Inhalt" {
		t.Fatalf("Inhalt wurde trotz Fehler geändert: %q", content)
	}
}
