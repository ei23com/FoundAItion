package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// ─── Titel ──────────────────────────────────────────────────────────────────

// Der HTML-Titel aus <head> gewinnt – nie der <title> aus SVG-Logos
// (Regression: Electrek speicherte "Electrek Logo" als Titel).
func TestExtractHTMLTitlePrefersHeadTitle(t *testing.T) {
	html := `<!doctype html><html><head>
		<title>Tesla takes on $30 billion in credit | Electrek</title></head>
	<body>
		<a href="/"><svg><title>Electrek Logo</title><path/></svg></a>
		<article><h1>Tesla takes on $30 billion in credit</h1>
		<p>Fließtext des Artikels, lang genug für jeden Zweck und voller Details.</p></article>
		<footer><svg><title>9to5Mac Logo</title><path/></svg></footer>
	</body></html>`
	doc := mustDoc(t, html)
	got := extractHTMLTitle(doc)
	// 1:1 wie im HTML (inkl. "| Electrek") – nur getrimmt.
	if got != "Tesla takes on $30 billion in credit | Electrek" {
		t.Fatalf("Titel = %q", got)
	}
}

// Fallbacks: ohne <head><title> og:title, sonst erste H1.
func TestExtractHTMLTitleFallbacks(t *testing.T) {
	doc := mustDoc(t, `<html><head><meta property="og:title" content="OG Titel"></head><body><h1>H1 Titel</h1></body></html>`)
	if got := extractHTMLTitle(doc); got != "OG Titel" {
		t.Fatalf("og:title-Fallback fehlgeschlagen: %q", got)
	}
	doc = mustDoc(t, `<html><body><h1>H1 Titel</h1><p>Text</p></body></html>`)
	if got := extractHTMLTitle(doc); got != "H1 Titel" {
		t.Fatalf("h1-Fallback fehlgeschlagen: %q", got)
	}
	doc = mustDoc(t, `<html><body><svg><title>Nur Logo</title></svg></body></html>`)
	if got := extractHTMLTitle(doc); got != "" {
		t.Fatalf("SVG-Titel muss ignoriert werden, bekam %q", got)
	}
}

// ─── Inhalt ─────────────────────────────────────────────────────────────────

// Readability/Boilerplate: Cookie-Banner, Navigation und Werbung müssen draußen
// bleiben, der Artikeltext muss erhalten bleiben.
func TestExtractContentDropsBoilerplate(t *testing.T) {
	page := `<!doctype html><html><head><title>Artikel | Testseite</title></head>
	<body>
		<div id="cookie-banner"><p>Wir verwenden Cookies und ähnliche Technologien, um Inhalte zu personalisieren. ALLE COOKIES AKZEPTIEREN oder Einstellungen verwalten.</p></div>
		<nav><ul><li><a href="/">Startseite</a></li><li><a href="/impressum">Impressum</a></li></ul></nav>
		<article>
			<h1>Der eigentliche Artikel</h1>
			<p>Dieser Absatz enthält den echten Inhalt der Seite und ist lang genug, damit die Extraktion ihn als Hauptinhalt erkennt. Er beschreibt ein Thema ausführlich.</p>
			<p>Ein zweiter Absatz mit weiteren Details zum Thema, damit der Artikel noch glaubwürdiger wird und genug Substanz hat.</p>
		</article>
		<aside><p>Newsletter abonnieren! Jetzt anmelden und nichts verpassen.</p></aside>
		<footer><p>Copyright 2026 – alle Rechte vorbehalten.</p></footer>
	</body></html>`
	_, content, err := extractPage([]byte(page), "https://example.org/artikel")
	if err != nil {
		t.Fatalf("extrahieren: %v", err)
	}
	if !strings.Contains(content, "Der eigentliche Artikel") || !strings.Contains(content, "zweiter Absatz") {
		t.Fatalf("Artikeltext fehlt: %q", content)
	}
	for _, junk := range []string{"ALLE COOKIES", "Impressum", "Newsletter", "Copyright"} {
		if strings.Contains(content, junk) {
			t.Fatalf("Boilerplate %q nicht entfernt: %q", junk, content)
		}
	}
}

// Ohne sauberen Inhalt: Fehler liefern, Inhalt bleibt leer – niemals wird eine
// Fehlermeldung als Inhalt gespeichert.
func TestExtractPageFailsCleanly(t *testing.T) {
	page := `<html><head><title>Cookie-Wall</title></head><body><div class="cookie-consent">Bitte akzeptieren Sie alle Cookies, um fortzufahren.</div></body></html>`
	title, content, err := extractPage([]byte(page), "https://example.org/")
	if err == nil {
		t.Fatalf("Fehler erwartet, Titel=%q Inhalt=%q", title, content)
	}
	if content != "" {
		t.Fatalf("Inhalt muss leer sein, bekam %q", content)
	}
	if title != "Cookie-Wall" {
		t.Fatalf("Titel trotzdem ermittelbar erwartet, bekam %q", title)
	}
}

// ─── Laden ──────────────────────────────────────────────────────────────────

// Allgemeiner Fallback: Schlägt der erste Versuch fehl, wird mit erzwungenem
// HTTP/1.1 erneut versucht (kein seiten-spezifischer Spezialfall).
func TestFetchHTMLRetriesWithHTTP11(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Simuliert Seiten, die den Go-HTTP/2-Stack ablehnen (z. B. HTTP 419).
			w.WriteHeader(419)
			fmt.Fprint(w, "Sorry")
			return
		}
		fmt.Fprint(w, `<html><head><title>Klappt</title></head><body><p>Inhalt</p></body></html>`)
	}))
	defer srv.Close()

	a := &App{cfg: Config{CrawlTimeoutSec: 10}}
	body, err := a.fetchHTML(srv.URL)
	if err != nil {
		t.Fatalf("fetchHTML: %v", err)
	}
	if !strings.Contains(string(body), "Klappt") {
		t.Fatalf("falscher Inhalt: %q", body)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("erwartet 2 Versuche (Standard + HTTP/1.1), bekam %d", calls)
	}
}

// Bleiben beide Versuche erfolglos, wird der Statuscode gemeldet – und
// scrapeURL liefert leeren Inhalt statt Fehler-Text.
func TestFetchHTMLReportsStatusWhenBothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(419)
		fmt.Fprint(w, "Sorry")
	}))
	defer srv.Close()

	a := &App{cfg: Config{CrawlTimeoutSec: 10}}
	if _, err := a.fetchHTML(srv.URL); err == nil || !strings.Contains(err.Error(), "HTTP 419") {
		t.Fatalf("Fehler mit \"HTTP 419\" erwartet, bekam %v", err)
	}
	if title, content, err := a.scrapeURL(srv.URL); err == nil || content != "" {
		t.Fatalf("scrapeURL muss fehlschlagen und leeren Inhalt liefern, bekam Titel=%q Inhalt=%q err=%v", title, content, err)
	}
}

// ─── Hilfen ─────────────────────────────────────────────────────────────────

func TestExpandLinksInMarkdown(t *testing.T) {
	in := "[Beispiel](https://example.org/x) und [https://a.de](https://a.de)"
	out := expandLinksInMarkdown(in)
	if !strings.Contains(out, "[Beispiel](https://example.org/x) - `https://example.org/x`") {
		t.Fatalf("URL nicht aufgeklappt: %q", out)
	}
	if strings.Contains(out, "[https://a.de](https://a.de) - `https://a.de`") {
		t.Fatalf("bereits sichtbare URL darf nicht ergänzt werden: %q", out)
	}
}

// mustDoc parst HTML zu einem goquery-Dokument.
func mustDoc(t *testing.T, raw string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}
