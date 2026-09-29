package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testAppWithPrompts erzeugt eine App mit den echten Prompt-Dateien aus ./prompts.
func testAppWithPrompts(t *testing.T, cfg Config) *App {
	t.Helper()
	app := testAppWithDB(t, cfg)
	app.prompts = loadPrompts("prompts")
	if len(app.prompts) == 0 {
		t.Fatalf("keine Prompt-Templates geladen")
	}
	return app
}

// TestResolvePromptNameFromNote prüft die Auswahl des Templates anhand der Notiz.
func TestResolvePromptNameFromNote(t *testing.T) {
	app := testAppWithPrompts(t, Config{})

	// Sicherstellen, dass die Beispiel-Templates vorhanden sind.
	for _, want := range []string{"summary_DE", "summary_EN", "chapters_DE", "chapters_EN", "custom_prompt"} {
		if _, ok := app.prompts[want]; !ok {
			t.Fatalf("Template %q fehlt in ./prompts", want)
		}
	}

	cases := []struct {
		note string
		want string
	}{
		{"custom_prompt", "custom_prompt"},                 // Beispiel-Template
		{"chapters_EN", "chapters_EN"},                     // exakter Dateiname
		{"chapters_de", "chapters_DE"},                     // Schreibweise egal
		{"summary_EN", "summary_EN"},                       // explizit englische Zusammenfassung
		{"Bitte custom_prompt verwenden", "custom_prompt"}, // Name irgendwo in der Notiz
		{"custom_prompt, wichtig", "custom_prompt"},        // mit Komma
		{"", "summary"},                                    // keine Notiz → Standard
		{"voellig_unbekannt", "summary"},                   // unbekannt → Standard
		{"kapitel", "chapters"},                            // bisheriges Schlüsselwort
		{"Chapters bitte", "chapters"},                     // bisheriges Schlüsselwort
		{"kategorisieren", "categorize"},                   // bisheriges Schlüsselwort
	}
	for _, c := range cases {
		if got := app.resolvePromptName(c.note); got != c.want {
			t.Errorf("resolvePromptName(%q) = %q, erwartet %q", c.note, got, c.want)
		}
	}
}

// Ohne geladene Templates darf nichts kaputtgehen (Fallback auf summary).
func TestResolvePromptNameWithoutPrompts(t *testing.T) {
	app := &App{}
	if got := app.resolvePromptName("custom_prompt"); got != "summary" {
		t.Fatalf("ohne Templates: %q, erwartet \"summary\"", got)
	}
}

// recordingLLM liefert eine feste Antwort und merkt sich den Request-Body.
func recordingLLM(t *testing.T, summary string) (string, *string) {
	t.Helper()
	recorded := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*recorded = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`, summary)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, recorded
}

// Der in der Notiz genannte Prompt muss tatsächlich an das LLM gehen.
func TestSummaryUsesPromptFromNote(t *testing.T) {
	cases := []struct {
		name   string
		note   string
		marker string // eindeutiger Text aus dem erwarteten Template
	}{
		{"custom_prompt", "custom_prompt", "Handlungsempfehlung"},
		{"chapters_EN", "chapters_EN", "Please create chapters"},
		{"Standard ohne Notiz", "", "TL;DR"},
		{"Standard bei unbekannter Notiz", "nur eine normale Notiz", "TL;DR"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseURL, recorded := recordingLLM(t, "Antwort")
			cfg := llmConfig(baseURL)
			cfg.UILanguage = "de"
			app := testAppWithPrompts(t, cfg)

			id := insertTestLink(t, app, "https://example.org/a", "Titel", "Der Inhalt des Eintrags.")
			if c.note != "" {
				if _, err := app.db.Exec(fmt.Sprintf(`UPDATE %s SET "note" = ? WHERE "id" = ?`, TableName), c.note, id); err != nil {
					t.Fatalf("note setzen: %v", err)
				}
			}

			rec := resummarizeRequest(app, id, http.MethodPost)
			if rec.Code != http.StatusOK {
				t.Fatalf("Status %d, Body: %s", rec.Code, rec.Body.String())
			}

			var payload struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal([]byte(*recorded), &payload); err != nil {
				t.Fatalf("LLM-Request nicht lesbar: %v", err)
			}
			if len(payload.Messages) < 2 {
				t.Fatalf("erwartet System- und User-Nachricht, bekam %d", len(payload.Messages))
			}
			userMsg := payload.Messages[len(payload.Messages)-1].Content

			if !strings.Contains(userMsg, c.marker) {
				t.Fatalf("Prompt-Marker %q fehlt im LLM-Aufruf.\nAuszug: %s", c.marker, firstRunes(userMsg, 300))
			}
			if !strings.Contains(userMsg, "Der Inhalt des Eintrags.") {
				t.Fatalf("Inhalt wurde nicht eingesetzt:\n%s", firstRunes(userMsg, 300))
			}
		})
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func TestGetPromptsEndpoint(t *testing.T) {
	app := testAppWithPrompts(t, Config{})
	rec := httptest.NewRecorder()
	app.getPrompts(rec, httptest.NewRequest(http.MethodGet, "/api/prompts", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d", rec.Code)
	}
	var resp struct {
		Prompts []string `json:"prompts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Antwort nicht lesbar: %v", err)
	}
	if len(resp.Prompts) < 6 {
		t.Fatalf("zu wenige Templates: %v", resp.Prompts)
	}
	joined := strings.Join(resp.Prompts, ",")
	for _, want := range []string{"custom_prompt", "summary_DE", "chapters_EN"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Template %q fehlt in %v", want, resp.Prompts)
		}
	}
	// sortiert?
	if !sortedStrings(resp.Prompts) {
		t.Fatalf("Template-Liste nicht sortiert: %v", resp.Prompts)
	}
}

func sortedStrings(v []string) bool {
	for i := 1; i < len(v); i++ {
		if v[i-1] > v[i] {
			return false
		}
	}
	return true
}
