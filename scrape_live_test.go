package main

// Live-Tests gegen echte Webseiten (nur mit CRAWL_LIVE_TEST=1).
//
// Diese Tests dokumentieren drei Scraper-Regressionsfälle:
//
//   - Electrek: <svg><title>Electrek Logo</title>-Tags im Body hatten früher
//     den echten HTML-Titel überschrieben.
//   - Hacker News: blockiert den HTTP/2-Fingerprint des Go-Stacks mit HTTP 419
//     (curl/Firefox bekommen 200). Der allgemeine HTTP/1.1-Fallback in
//     fetchHTML muss das ohne Seiten-Sonderbehandlung lösen.
//   - RSS-/Atom-Feeds: müssen strukturiert ausgewertet werden (alle Einträge
//     mit Datum/Titel/Beschreibung) statt als HTML-Textbrei.

import (
	"os"
	"strings"
	"testing"
)

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("CRAWL_LIVE_TEST") == "" {
		t.Skip("set CRAWL_LIVE_TEST=1")
	}
}

func TestLiveScrapeElectrekTitle(t *testing.T) {
	requireLive(t)
	a := &App{cfg: Config{CrawlTimeoutSec: 60}}
	title, content, err := a.scrapeURL("https://electrek.co/2026/09/29/tesla-takes-on-30-billion-in-credit-as-it-approaches-unprofitability/")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	t.Logf("title=%q content=%d bytes", title, len(content))
	if strings.Contains(title, "Logo") {
		t.Fatalf("SVG-Logo-Titel als Titel erkannt: %q", title)
	}
	if !strings.Contains(title, "Tesla") {
		t.Fatalf("unerwarteter Titel: %q", title)
	}
	if len(content) < 1000 {
		t.Fatalf("zu wenig Inhalt: %d bytes", len(content))
	}
}

func TestLiveScrapeHackerNews(t *testing.T) {
	requireLive(t)
	a := &App{cfg: Config{CrawlTimeoutSec: 60}}
	title, content, err := a.scrapeURL("https://news.ycombinator.com/item?id=49914236")
	if err != nil {
		t.Fatalf("scrape (HTTP/1.1-Fallback muss greifen): %v", err)
	}
	t.Logf("title=%q content=%d bytes", title, len(content))
	if title == "" {
		t.Fatalf("kein Titel")
	}
	if len(content) < 500 {
		t.Fatalf("zu wenig Inhalt: %d bytes", len(content))
	}
}

// Feed aus dem eigenen Netz (FreshRSS, r/LocalLLaMA): 28 Einträge – früher
// landete davon praktisch nur der erste im Inhalt.
func TestLiveScrapeFreshRSSFeed(t *testing.T) {
	requireLive(t)
	const feedURL = "http://10.1.1.11:2224/i/?a=rss&get=f_80&state=3&nb=500&hours=168"

	a := &App{cfg: Config{CrawlTimeoutSec: 60, SummaryMaxTokens: 64000}}
	title, content, err := a.scrapeURL(feedURL)
	if err != nil {
		t.Skipf("Feed nicht erreichbar (nur im lokalen Netz): %v", err)
	}
	t.Logf("title=%q content=%d bytes, %d Einträge", title, len(content), strings.Count(content, "\n## "))

	if !strings.Contains(title, "LocalLLaMA") {
		t.Fatalf("Feed-Titel nicht erkannt: %q", title)
	}
	if n := strings.Count(content, "\n## "); n < 5 {
		t.Fatalf("nur %d Einträge im Inhalt", n)
	}
	if !strings.Contains(content, "- date: ") || !strings.Contains(content, "- link: ") {
		t.Fatalf("Einträge ohne Datum/Link:\n%s", content[:minLen(len(content), 400)])
	}
	if strings.Contains(content, "external-preview.redd.it") {
		t.Fatalf("Vorschaubild-URLs nicht entfernt")
	}
	if strings.Contains(content, "]]&gt;") {
		t.Fatalf("CDATA-Reste im Inhalt")
	}
	// Deutlich kompakter als der HTML-Pfad (der lag bei ~2,3 kB für 2 Einträge).
	if len(content) > 40000 {
		t.Fatalf("Inhalt zu groß: %d bytes", len(content))
	}
}

// Öffentlicher Feed als stabiler Regressionsfall.
func TestLiveScrapeHackerNewsRSS(t *testing.T) {
	requireLive(t)
	a := &App{cfg: Config{CrawlTimeoutSec: 60, SummaryMaxTokens: 64000}}

	title, content, err := a.scrapeURL("https://news.ycombinator.com/rss")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	t.Logf("title=%q content=%d bytes, %d Einträge", title, len(content), strings.Count(content, "\n## "))

	if title != "Hacker News" {
		t.Fatalf("Titel = %q, erwartet \"Hacker News\"", title)
	}
	if n := strings.Count(content, "\n## "); n < 10 {
		t.Fatalf("nur %d Einträge im Inhalt", n)
	}
	if !strings.Contains(content, "- link: https://") {
		t.Fatalf("Eintrags-Links fehlen:\n%s", content[:minLen(len(content), 400)])
	}
	if !strings.Contains(content, "Comments](https://news.ycombinator.com/item?id=") {
		t.Fatalf("Beschreibung/Link auf die Diskussion fehlt:\n%s", content[:minLen(len(content), 400)])
	}
}
