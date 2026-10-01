package main

// Live-Tests gegen echte Webseiten (nur mit CRAWL_LIVE_TEST=1).
//
// Diese Tests dokumentieren zwei Scraper-Regressionsfälle:
//
//   - Electrek: <svg><title>Electrek Logo</title>-Tags im Body hatten früher
//     den echten HTML-Titel überschrieben.
//   - Hacker News: blockiert den HTTP/2-Fingerprint des Go-Stacks mit HTTP 419
//     (curl/Firefox bekommen 200). Der allgemeine HTTP/1.1-Fallback in
//     fetchHTML muss das ohne Seiten-Sonderbehandlung lösen.

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
