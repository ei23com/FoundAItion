package main

// Live-Test gegen ein echtes Modell (nur mit LIVE_LLM_TEST=1).
//
// Regressionsfall: Eine Zusammenfassung, die das Ausgabelimit des Modells
// erreicht (`finish_reason: length`), wurde früher stillschweigend mitten im
// Text abgeschnitten. Mit dem Standardlimit und der automatischen Fortsetzung
// muss die Zusammenfassung vollständig sein.

import (
	"os"
	"strings"
	"testing"
)

func TestLiveFeedSummaryIsComplete(t *testing.T) {
	if os.Getenv("LIVE_LLM_TEST") == "" {
		t.Skip("setze LIVE_LLM_TEST=1, um diesen Test gegen ein echtes Modell auszuführen")
	}
	cfg := loadConfig()
	if cfg.OpenAIKey == "" {
		t.Skip("kein OPENAI_API_KEY konfiguriert")
	}

	const feedURL = "http://10.1.1.11:2224/i/?a=rss&get=f_80&state=3&nb=500&hours=168"

	fetcher := &App{cfg: cfg}
	_, content, err := fetcher.scrapeURL(feedURL)
	if err != nil {
		t.Skipf("Feed nicht erreichbar: %v", err)
	}

	app := testAppWithDB(t, cfg)
	summary, err := app.generateSummary(content, "summary", "de")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}
	t.Logf("%d Zeichen Inhalt → %d Zeichen Zusammenfassung (Limit %d Token)",
		len(content), len(summary), cfg.summaryMaxOutputTokens())

	if strings.Contains(summary, "abgeschnitten") {
		t.Fatalf("Zusammenfassung abgeschnitten – SUMMARY_MAX_OUTPUT_TOKENS erhöhen (aktuell %d)", cfg.summaryMaxOutputTokens())
	}
	if len(summary) < 200 {
		t.Fatalf("Zusammenfassung unerwartet kurz: %q", summary)
	}
}
