package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Die Liste war nach einem Start auffällig langsam: ohne Indizes müssen
// COUNT(*) und die Kategorien-Abfrage die komplette links-Tabelle scannen
// (~250 MB, weil der gescrapte Text in Overflow-Seiten liegt). ensureListIndexes
// soll genau das verhindern – und der Planner muss die Indizes auch benutzen.
func TestListIndexesUsedByPlanner(t *testing.T) {
	app := testAppWithDB(t, Config{})
	if err := ensureListIndexes(app.db, TableName); err != nil {
		t.Fatalf("ensureListIndexes: %v", err)
	}
	// Zweiter Durchlauf muss nichts Neues anlegen (Idempotenz).
	if err := ensureListIndexes(app.db, TableName); err != nil {
		t.Fatalf("ensureListIndexes (second run): %v", err)
	}

	var names []string
	rows, err := app.db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_links%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan index name: %v", err)
		}
		names = append(names, n)
	}
	rows.Close()
	if len(names) != len(listIndexes) {
		t.Fatalf("indexes = %v, want %d entries", names, len(listIndexes))
	}

	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT COUNT(*) FROM "links" WHERE IFNULL("read", 0) = 0`, "idx_links_read"},
		{`SELECT "category", COUNT(*) FROM "links" WHERE "category" IS NOT NULL AND "category" != '' GROUP BY "category"`, "idx_links_category"},
		{`SELECT COUNT(*) FROM "links" WHERE "timestamp" >= '2026-01-01' AND "timestamp" < '2026-01-03'`, "idx_links_timestamp"},
		{fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN "vec_model" = 'm' THEN 1 ELSE 0 END), 0) FROM %s`, TableName), "idx_links_vec_model"},
	}
	for _, tc := range cases {
		var detail string
		pr, err := app.db.Query("EXPLAIN QUERY PLAN " + tc.sql)
		if err != nil {
			t.Fatalf("plan %q: %v", tc.sql, err)
		}
		for pr.Next() {
			var id, parent, notused int
			var d string
			if err := pr.Scan(&id, &parent, &notused, &d); err != nil {
				pr.Close()
				t.Fatalf("scan plan: %v", err)
			}
			detail += d + "\n"
		}
		pr.Close()
		if !strings.Contains(detail, tc.want) {
			t.Errorf("plan does not use %s for %s:\n%s", tc.want, tc.sql, detail)
		}
		if strings.Contains(detail, "SCAN \"links\"") {
			t.Errorf("plan still scans the whole table for %s:\n%s", tc.sql, detail)
		}
	}
}

// Die Länge des Seitenkörpers kommt jetzt aus SQL (length("content")), damit der
// Text selbst nicht von der Platte geholt werden muss. Das Badge
// "N Chars · ~M Tokens" im Frontend darf trotzdem nicht leer bleiben.
func TestListReportsContentLengthWithoutContent(t *testing.T) {
	app := testAppWithDB(t, Config{})
	if err := ensureListIndexes(app.db, TableName); err != nil {
		t.Fatalf("ensureListIndexes: %v", err)
	}
	const text = "wörter mit umlauten und emoji 🚀"
	if _, err := app.db.Exec(fmt.Sprintf(
		`INSERT INTO %s ("timestamp", "url", "title", "content") VALUES ('2026-01-01 10:00:00', ?, ?, ?)`,
		TableName), "https://example.com/x", "Titel", text); err != nil {
		t.Fatalf("insert: %v", err)
	}

	decode := func(query string) linkListResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		app.getLinks(rec, httptest.NewRequest(http.MethodGet, query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, query)
		}
		var out linkListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	without := decode("/api/links")
	if len(without.Links) != 1 {
		t.Fatalf("links = %d, want 1", len(without.Links))
	}
	if without.Links[0].Content != "" {
		t.Errorf("content shipped although not requested: %q", without.Links[0].Content)
	}
	if got, want := without.Links[0].ContentLen, len([]rune(text)); got != want {
		t.Errorf("content_len = %d, want %d (runes)", got, want)
	}
	if without.Unread != 1 {
		t.Errorf("unread = %d, want 1 (NULL counts as unread)", without.Unread)
	}

	withContent := decode("/api/links?content=true")
	if withContent.Links[0].Content != text {
		t.Errorf("content = %q, want the stored text", withContent.Links[0].Content)
	}
}

// date_from/date_to vergleichen jetzt nackt gegen die Spalte (Index kann greifen).
// Beide Grenzen müssen weiterhin denselben Tag einschließen wie vorher substr().
func TestDateRangeFiltersStayInclusive(t *testing.T) {
	app := testAppWithDB(t, Config{})
	if err := ensureListIndexes(app.db, TableName); err != nil {
		t.Fatalf("ensureListIndexes: %v", err)
	}
	rows := []struct {
		ts   string
		want bool
	}{
		{"2025-12-31 23:59:59", false},
		{"2026-01-01 00:00:00", true},
		{"2026-01-02 23:59:59", true},
		{"2026-01-03T00:00:00Z", false}, // ISO-Format aus älteren Insertions
	}
	for _, row := range rows {
		if _, err := app.db.Exec(fmt.Sprintf(
			`INSERT INTO %s ("timestamp", "url", "title") VALUES (?, ?, ?)`, TableName),
			row.ts, "https://example.com/"+row.ts, row.ts); err != nil {
			t.Fatalf("insert %s: %v", row.ts, err)
		}
	}

	rec := httptest.NewRecorder()
	app.getLinks(rec, httptest.NewRequest(http.MethodGet,
		"/api/links?date_from=2026-01-01&date_to=2026-01-02", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out linkListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 || len(out.Links) != 2 {
		t.Fatalf("total=%d links=%d, want 2/2 (body %s)", out.Total, len(out.Links), rec.Body.String())
	}
	for _, l := range out.Links {
		if !strings.HasPrefix(l.URL, "https://example.com/2026-01-0") {
			t.Errorf("unexpected row %q", l.URL)
		}
	}

	if next, ok := dayAfter("2026-02-28"); !ok || next != "2026-03-01" {
		t.Errorf("dayAfter(2026-02-28) = %q, %v; want 2026-03-01, true", next, ok)
	}
	if _, ok := dayAfter("gestern"); ok {
		t.Error("dayAfter(gestern) should not be ok")
	}
}
