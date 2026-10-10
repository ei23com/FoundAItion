package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// htmlArticleText ist lang genug für die Mindestlänge der Inhaltsextraktion.
var htmlArticleText = "Dies ist der eigentliche Artikeltext der Testseite und er ist " +
	"bewusst so lang gewählt, dass die Inhaltsextraktion ihn als sauberen Inhalt akzeptiert. " +
	strings.Repeat("Der Text wiederholt sich daher mehrfach. ", 5)

// htmlPage liefert eine minimale HTML-Seite mit HTML-Titel für die Scrape-Pfade.
var htmlPage = `<!doctype html><html><head><title>HTML Titel</title></head>` +
	`<body><article><p>` + htmlArticleText + `</p></article></body></html>`

// linkshareRequest schickt einen POST-Body an receiveLink.
func linkshareRequest(app *App, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/linkshare", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.receiveLink(rec, req)
	return rec
}

func decodeLinkAddResponse(t *testing.T, rec *httptest.ResponseRecorder) LinkAddResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200 (Body: %s)", rec.Code, rec.Body.String())
	}
	var resp LinkAddResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Antwort dekodieren: %v (Body: %s)", err, rec.Body.String())
	}
	return resp
}

func loadLink(t *testing.T, app *App, id int64) *Link {
	t.Helper()
	link, err := app.fetchLinkByID(id, true)
	if err != nil {
		t.Fatalf("Link %d laden: %v", id, err)
	}
	return link
}

// Mitgelieferter Inhalt: es darf kein HTTP-Request stattfinden.
func TestLinkShareProvidedContentSkipsScraping(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, htmlPage)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})

	content := strings.TrimSpace("Extern gelieferter Inhalt: " + strings.Repeat("Zeile mit Text. ", 20))
	body := fmt.Sprintf(`{"url":%q,"note":"","title":"Externer Titel","content":%q}`,
		srv.URL+"/artikel", content)
	resp := decodeLinkAddResponse(t, linkshareRequest(app, body))

	if resp.Status != "ok" {
		t.Fatalf("Status = %q, erwartet \"ok\"", resp.Status)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("Server wurde %d mal angefragt, erwartet 0 (kein Scraping bei geliefertem Inhalt)", n)
	}

	link := loadLink(t, app, resp.ID)
	if link.Title != "Externer Titel" {
		t.Errorf("Titel = %q, erwartet \"Externer Titel\"", link.Title)
	}
	if link.Content != content {
		t.Errorf("Inhalt = %q, erwartet den gelieferten Text", link.Content)
	}
}

// Ohne gescrapten HTML-Titel bleibt der Titel leer, wenn keiner mitgeliefert wird.
func TestLinkShareProvidedContentWithoutTitle(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, htmlPage)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})

	content := strings.TrimSpace(strings.Repeat("Nur Inhalt, kein Titel. ", 15))
	resp := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":%q,"content":%q}`, srv.URL+"/ohne-titel", content)))

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("Server wurde %d mal angefragt, erwartet 0", n)
	}
	link := loadLink(t, app, resp.ID)
	if link.Title != "" {
		t.Errorf("Titel = %q, erwartet \"\"", link.Title)
	}
	if link.Content != content {
		t.Errorf("Inhalt wurde nicht 1:1 gespeichert")
	}
}

// Gelieferter Inhalt wird auf das Token-Limit gekürzt – ohne halbe UTF-8-Zeichen.
func TestLinkShareProvidedContentIsCapped(t *testing.T) {
	app := testAppWithDB(t, Config{SummaryMaxTokens: 100}) // → 350 Zeichen
	limit := 350

	content := strings.Repeat("ä", 500) // 2 Byte pro Zeichen
	resp := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":"https://example.org/lang","content":%q}`, content)))

	link := loadLink(t, app, resp.ID)
	if len(link.Content) > limit {
		t.Errorf("Inhalt = %d Bytes, erwartet <= %d", len(link.Content), limit)
	}
	if len(link.Content) < limit-3 {
		t.Errorf("Inhalt = %d Bytes, erwartet nahe %d", len(link.Content), limit)
	}
	if !utf8.ValidString(link.Content) {
		t.Errorf("Inhalt endet mit einer halben UTF-8-Sequenz")
	}
}

// Ohne content wird weiterhin gescraped; der HTML-Titel gewinnt gegen den
// mitgelieferten Titel.
func TestLinkShareScrapeStillUsedWithoutContent(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, htmlPage)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})

	resp := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":%q,"title":"Titel vom Aufrufer"}`, srv.URL+"/scrape")))

	if n := atomic.LoadInt32(&hits); n == 0 {
		t.Fatalf("Server wurde nicht angefragt – Scraping hat nicht stattgefunden")
	}
	link := loadLink(t, app, resp.ID)
	if link.Title != "HTML Titel" {
		t.Errorf("Titel = %q, erwartet \"HTML Titel\"", link.Title)
	}
	if !strings.Contains(link.Content, "eigentliche Artikeltext") {
		t.Errorf("Inhalt enthält den Artikeltext nicht: %q", link.Content)
	}
}

// Scheitert das Scraping, wird der mitgelieferte Titel genutzt und der Inhalt
// bleibt leer (nie Fehlertext).
func TestLinkShareProvidedTitleAsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{})

	resp := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":%q,"title":"Titel vom Aufrufer"}`, srv.URL+"/kaputt")))

	link := loadLink(t, app, resp.ID)
	if link.Title != "Titel vom Aufrufer" {
		t.Errorf("Titel = %q, erwartet \"Titel vom Aufrufer\"", link.Title)
	}
	if strings.TrimSpace(link.Content) != "" {
		t.Errorf("Inhalt = %q, erwartet leer", link.Content)
	}
}

// Ein bereits vorhandener Eintrag ohne Inhalt wird ergänzt (Status "updated"),
// vorhandener Inhalt wird nie überschrieben.
func TestLinkShareFillsEmptyContentOfExistingLink(t *testing.T) {
	app := testAppWithDB(t, Config{})
	const url = "https://example.org/bestehend"

	id := insertTestLink(t, app, url, "Bestehender Titel", "")
	first := strings.TrimSpace("Nachgelieferter Inhalt " + strings.Repeat("Text. ", 20))

	resp := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":%q,"title":"Neuer Titel","content":%q}`, url, first)))
	if resp.Status != "updated" {
		t.Fatalf("Status = %q, erwartet \"updated\"", resp.Status)
	}
	if resp.ID != id {
		t.Errorf("ID = %d, erwartet %d", resp.ID, id)
	}

	link := loadLink(t, app, id)
	if link.Content != first {
		t.Errorf("Inhalt wurde nicht ergänzt: %q", link.Content)
	}
	if link.Title != "Bestehender Titel" {
		t.Errorf("Titel = %q – vorhandener Titel darf nicht überschrieben werden", link.Title)
	}

	// Zweiter Versuch: Inhalt ist jetzt da → Duplikat, nichts wird geändert.
	second := strings.TrimSpace("Anderer Inhalt " + strings.Repeat("Text. ", 20))
	resp2 := decodeLinkAddResponse(t, linkshareRequest(app,
		fmt.Sprintf(`{"url":%q,"content":%q}`, url, second)))
	if resp2.Status != "duplicate" {
		t.Fatalf("Status = %q, erwartet \"duplicate\"", resp2.Status)
	}
	if got := loadLink(t, app, id).Content; got != first {
		t.Errorf("Inhalt wurde überschrieben: %q", got)
	}
}

// Fehlt url, bleibt es bei 400 – auch wenn content mitkommt.
func TestLinkShareRequiresURL(t *testing.T) {
	app := testAppWithDB(t, Config{})
	rec := linkshareRequest(app, `{"content":"nur inhalt ohne url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Status = %d, erwartet 400", rec.Code)
	}
}
