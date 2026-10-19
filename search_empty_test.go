package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Leere Trefferliste muss als [] serialisiert werden, nicht als null –
// das Frontend iteriert direkt ueber das Feld.
func TestSearchWithoutHitsReturnsEmptyArray(t *testing.T) {
	app := testAppWithDB(t, Config{})
	insertTestLink(t, app, "https://example.com/a", "Alpha", "x")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/links?q=doesnotexist", nil)
	app.getLinks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"links":[]`) {
		t.Fatalf("body = %s, want \"links\":[]", body)
	}
	var out linkListResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Links) != 0 || out.Total != 0 {
		t.Fatalf("links=%d total=%d, want 0/0", len(out.Links), out.Total)
	}
}
