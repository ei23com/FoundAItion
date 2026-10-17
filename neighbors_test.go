package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Nachbarn in Listenreihenfolge (id absteigend): prev = naechsthoehere id,
// next = naechstniedrigere id.
func TestNeighborLinks(t *testing.T) {
	app := testAppWithDB(t, Config{})
	first := insertTestLink(t, app, "https://example.com/a", "A", "x")
	second := insertTestLink(t, app, "https://example.com/b", "B", "x")
	third := insertTestLink(t, app, "https://example.com/c", "C", "x")

	call := func(query string) (*httptest.ResponseRecorder, struct {
		Prev *int64 `json:"prev"`
		Next *int64 `json:"next"`
	}) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/links/neighbors?"+query, nil)
		app.neighborLinks(rec, req)
		var out struct {
			Prev *int64 `json:"prev"`
			Next *int64 `json:"next"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return rec, out
	}

	cases := []struct {
		name     string
		id       int64
		wantPrev *int64
		wantNext *int64
	}{
		{"mitte", second, &third, &first},
		{"neuester", third, nil, &second},
		{"aeltester", first, &second, nil},
		{"unbekannt", 9999, nil, &third}, // id ueber allem: nur "next" zeigt auf den neuesten
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, out := call("id=" + itoa(tc.id))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !samePtr(out.Prev, tc.wantPrev) {
				t.Fatalf("prev = %v, want %v", deref(out.Prev), deref(tc.wantPrev))
			}
			if !samePtr(out.Next, tc.wantNext) {
				t.Fatalf("next = %v, want %v", deref(out.Next), deref(tc.wantNext))
			}
		})
	}

	if rec, _ := call("id=abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status = %d, want 400", rec.Code)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func samePtr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
