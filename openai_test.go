package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// llmRequest ist die aufgezeichnete Anfrage an die Chat-API.
type llmRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// llmRecorder ist ein Fake-LLM-Server, der vorgegebene Antworten liefert und
// alle Anfragen mitschreibt.
type llmRecorder struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []llmRequest
}

func newLLMRecorder(t *testing.T, responses ...string) *llmRecorder {
	t.Helper()
	rec := &llmRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req llmRequest
		_ = json.Unmarshal(body, &req)

		rec.mu.Lock()
		idx := len(rec.requests)
		rec.requests = append(rec.requests, req)
		rec.mu.Unlock()

		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responses[idx])
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *llmRecorder) calls() []llmRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]llmRequest(nil), rec.requests...)
}

// llmAnswer baut eine Chat-Antwort mit gegebener finish_reason.
func llmAnswer(content, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": finish,
		}},
	})
	return string(b)
}

func summaryApp(t *testing.T, cfg Config, rec *llmRecorder) *App {
	t.Helper()
	cfg.OpenAIKey = "test-key"
	cfg.OpenAIBaseURL = rec.srv.URL
	cfg.OpenAIModel = "test-model"
	return testAppWithDB(t, cfg)
}

// Das Ausgabelimit ist konfigurierbar (statt fest 4096) – Standard 8192.
func TestSummaryOutputLimitIsConfigurable(t *testing.T) {
	cases := []struct {
		configured int
		want       int
	}{
		{0, DefaultSummaryMaxOutputTokens},
		{-5, DefaultSummaryMaxOutputTokens},
		{1234, 1234},
		{32768, 32768},
	}
	for _, c := range cases {
		rec := newLLMRecorder(t, llmAnswer("ok", "stop"))
		app := summaryApp(t, Config{SummaryMaxOutputTokens: c.configured}, rec)

		if _, err := app.generateSummary("Inhalt", "summary", "de"); err != nil {
			t.Fatalf("generateSummary: %v", err)
		}
		calls := rec.calls()
		if len(calls) != 1 {
			t.Fatalf("erwartet 1 Aufruf, bekam %d", len(calls))
		}
		if calls[0].MaxTokens != c.want {
			t.Errorf("SUMMARY_MAX_OUTPUT_TOKENS=%d → max_tokens=%d, erwartet %d",
				c.configured, calls[0].MaxTokens, c.want)
		}
		if calls[0].Model != "test-model" {
			t.Errorf("Modell = %q", calls[0].Model)
		}
	}
}

// Abgeschnittene Antwort (finish_reason "length") wird automatisch fortgesetzt
// und zusammengesetzt – genau der Fall der abrupt endenden Feed-Zusammenfassung.
func TestSummaryContinuesWhenTruncated(t *testing.T) {
	rec := newLLMRecorder(t,
		llmAnswer("## Sicherheit\n\n* **Cyberangriffe:** Ein Akteur nutzte KI-Tools, um Banken anzugreifen [(08.10.26, 12:11)](https://www.reddit.com/r/LocalLL", "length"),
		llmAnswer("aMA/comments/1x0n4pt/).\n* **Nächster Punkt:** fertig.", "stop"),
	)
	app := summaryApp(t, Config{SummaryMaxOutputTokens: 4096}, rec)

	summary, err := app.generateSummary("Feed-Inhalt", "summary", "de")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}

	calls := rec.calls()
	if len(calls) != 2 {
		t.Fatalf("erwartet 2 Aufrufe (Antwort + Fortsetzung), bekam %d", len(calls))
	}
	// Die Fortsetzung bekommt die bisherige Antwort plus Aufforderung.
	cont := calls[1].Messages
	if len(cont) != 4 {
		t.Fatalf("Fortsetzungs-Anfrage hat %d Nachrichten, erwartet 4", len(cont))
	}
	if cont[2].Role != "assistant" || !strings.Contains(cont[2].Content, "Cyberangriffe") {
		t.Errorf("bisherige Antwort fehlt in der Fortsetzungs-Anfrage: %+v", cont[2])
	}
	if cont[3].Role != "user" || !strings.Contains(cont[3].Content, "fort") {
		t.Errorf("Fortsetzungs-Aufforderung fehlt: %+v", cont[3])
	}

	// Die Teile werden nahtlos zusammengesetzt – die URL ist wieder vollständig.
	if !strings.Contains(summary, "[(08.10.26, 12:11)](https://www.reddit.com/r/LocalLLaMA/comments/1x0n4pt/)") {
		t.Errorf("Antwort nicht nahtlos zusammengesetzt:\n%s", summary)
	}
	if !strings.Contains(summary, "Nächster Punkt") {
		t.Errorf("Fortsetzung fehlt in der Zusammenfassung:\n%s", summary)
	}
	if strings.Contains(summary, "abgeschnitten") {
		t.Errorf("Hinweis auf Abbruch trotz erfolgreicher Fortsetzung:\n%s", summary)
	}
}

// Englische UI: Fortsetzungs-Aufforderung und Hinweis auf Englisch.
func TestSummaryContinuationEnglish(t *testing.T) {
	rec := newLLMRecorder(t, llmAnswer("Part one", "length"), llmAnswer(" and part two", "stop"))
	app := summaryApp(t, Config{}, rec)

	summary, err := app.generateSummary("content", "summary", "en")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}
	if summary != "Part one and part two" {
		t.Errorf("Zusammenfassung = %q", summary)
	}
	calls := rec.calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Messages[3].Content, "cut off") {
		t.Errorf("englische Fortsetzungs-Aufforderung fehlt: %+v", calls[len(calls)-1].Messages)
	}
}

// Bleibt es abgeschnitten, wird die Anzahl der Fortsetzungen begrenzt und die
// Zusammenfassung sichtbar gekennzeichnet (statt still abzuschneiden).
func TestSummaryTruncationIsMarkedAfterMaxContinuations(t *testing.T) {
	rec := newLLMRecorder(t, llmAnswer("immer weiter und weiter", "length"))
	app := summaryApp(t, Config{SummaryMaxOutputTokens: 4096}, rec)

	summary, err := app.generateSummary("Inhalt", "summary", "de")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}

	if calls := len(rec.calls()); calls != maxSummaryContinuations+1 {
		t.Fatalf("erwartet %d Aufrufe, bekam %d", maxSummaryContinuations+1, calls)
	}
	if !strings.Contains(summary, "abgeschnitten") || !strings.Contains(summary, "SUMMARY_MAX_OUTPUT_TOKENS") {
		t.Errorf("Abbruch-Hinweis fehlt:\n%s", summary)
	}
	if !strings.Contains(summary, "4096") {
		t.Errorf("Ausgabelimit nicht im Hinweis:\n%s", summary)
	}
}

// Unvollständige Antwort + Fehler in der Fortsetzung: bisheriger Text bleibt.
func TestSummaryKeepsPartialTextOnContinuationError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, llmAnswer("erster Teil", "length"))
			return
		}
		http.Error(w, "kaputt", http.StatusInternalServerError)
	}))
	defer srv.Close()

	app := testAppWithDB(t, Config{
		OpenAIKey: "test-key", OpenAIBaseURL: srv.URL, OpenAIModel: "test-model",
	})
	summary, err := app.generateSummary("Inhalt", "summary", "de")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}
	if !strings.Contains(summary, "erster Teil") {
		t.Errorf("Teiltext verloren: %q", summary)
	}
	// Die Zusammenfassung ist unvollständig – das muss sichtbar sein.
	if !strings.Contains(summary, "abgeschnitten") {
		t.Errorf("fehlender Abbruch-Hinweis: %q", summary)
	}
}

// Kategorisierung wird nie fortgesetzt (dort ist die Antwort ein Wort).
func TestCategorizeIsNotContinued(t *testing.T) {
	rec := newLLMRecorder(t, llmAnswer("Technologie & Software", "length"))
	app := summaryApp(t, Config{}, rec)

	category, err := app.generateSummary("Beispielinhalt", "categorize", "de")
	if err != nil {
		t.Fatalf("generateSummary: %v", err)
	}
	if category != "Technologie & Software" {
		t.Errorf("Kategorie = %q", category)
	}
	if calls := len(rec.calls()); calls != 1 {
		t.Fatalf("erwartet 1 Aufruf, bekam %d", calls)
	}
}

// Abgeschnittene Antwort ganz ohne Text (z. B. Denk-Modell verbraucht das
// Limit für Reasoning): kein sinnloser Fortsetzungsversuch, sondern Fehler.
func TestSummaryEmptyTruncatedResponseIsError(t *testing.T) {
	rec := newLLMRecorder(t, llmAnswer("", "length"))
	app := summaryApp(t, Config{SummaryMaxOutputTokens: 64}, rec)

	summary, err := app.generateSummary("Inhalt", "summary", "de")
	if err == nil {
		t.Fatalf("erwartet Fehler, bekam %q", summary)
	}
	if !strings.Contains(err.Error(), "SUMMARY_MAX_OUTPUT_TOKENS") {
		t.Errorf("Fehlermeldung ohne Hinweis auf die Einstellung: %v", err)
	}
	if calls := len(rec.calls()); calls != 1 {
		t.Errorf("erwartet 1 Aufruf, bekam %d", calls)
	}
}
