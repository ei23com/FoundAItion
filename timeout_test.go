package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestLLMTimeoutDefaults: Standard ist 240 s, niedrigere Werte werden angehoben,
// höhere bleiben erhalten.
func TestLLMTimeoutDefaults(t *testing.T) {
	cases := []struct {
		configured int
		want       time.Duration
	}{
		{0, 240 * time.Second},   // nicht gesetzt → Minimum
		{60, 240 * time.Second},  // zu klein → angehoben
		{239, 240 * time.Second}, // knapp darunter → angehoben
		{240, 240 * time.Second}, // genau das Minimum
		{600, 600 * time.Second}, // größer → übernommen
	}
	for _, c := range cases {
		cfg := Config{LLMTimeoutSec: c.configured}
		if got := cfg.llmTimeout(); got != c.want {
			t.Errorf("LLMTimeoutSec=%d → %v, erwartet %v", c.configured, got, c.want)
		}
	}
}

// Das Zeitlimit muss auch bei generateSummary wirksam sein: ein Modell, das
// deutlich länger als die früheren 120 s braucht, darf nicht abgeschnitten
// werden. Läuft nur mit SLOW_LLM_TEST=1 (dauert ~2,5 Minuten).
func TestGenerateSummarySurvivesSlowModelLoad(t *testing.T) {
	if os.Getenv("SLOW_LLM_TEST") == "" {
		t.Skip("setze SLOW_LLM_TEST=1, um diesen Langsam-Modell-Test auszuführen")
	}
	const delay = 135 * time.Second // länger als das frühere 120-s-Limit

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(delay) // simuliert das Laden eines großen Modells
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"spät aber da"}}]}`)
	}))
	defer srv.Close()

	cfg := Config{
		OpenAIKey:        "test-key",
		OpenAIBaseURL:    srv.URL,
		OpenAIModel:      "slow-model",
		SummaryMaxTokens: 64000,
		LLMTimeoutSec:    240,
	}
	app := testAppWithDB(t, cfg)

	start := time.Now()
	summary, err := app.generateSummary("Inhalt", "summary", "de")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Aufruf nach %v fehlgeschlagen: %v", elapsed.Round(time.Second), err)
	}
	if summary != "spät aber da" {
		t.Fatalf("unerwartete Zusammenfassung: %q", summary)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("erwartet genau einen Aufruf, bekam %d", calls)
	}
	t.Logf("Antwort nach %v akzeptiert (früheres Limit: 120 s)", elapsed.Round(time.Second))
}

// Das Schreib-Zeitlimit des HTTP-Servers muss über dem LLM-Limit liegen,
// sonst würde eine langsame Modell-Antwort abgeschnitten.
func TestServerWriteTimeoutCoversLLM(t *testing.T) {
	if serverWriteTimeout <= time.Duration(MinLLMTimeoutSec)*time.Second {
		t.Fatalf("serverWriteTimeout (%v) muss über dem LLM-Timeout (%v) liegen",
			serverWriteTimeout, time.Duration(MinLLMTimeoutSec)*time.Second)
	}
	cfg := Config{}
	if cfg.llmTimeout() != time.Duration(MinLLMTimeoutSec)*time.Second {
		t.Fatalf("Standard-LLM-Timeout = %v, erwartet %v", cfg.llmTimeout(), time.Duration(MinLLMTimeoutSec)*time.Second)
	}
}
