package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ─── SQLite Compat Helpers ───────────────────────────────────────────────────

// dbTime implements sql.Scanner for cross-driver time conversion.
// SQLite provides a string (ISO-8601).
type dbTime struct{ time.Time }

func (t *dbTime) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case time.Time:
		t.Time = x
		return nil
	case string:
		parsed, err := time.Parse("2006-01-02 15:04:05", x)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, x)
			if err != nil {
				return fmt.Errorf("dbTime scan from string %q: %w", x, err)
			}
		}
		t.Time = parsed
		return nil
	case []byte:
		parsed, err := time.Parse("2006-01-02 15:04:05", string(x))
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, string(x))
			if err != nil {
				return fmt.Errorf("dbTime scan from []byte %q: %w", string(x), err)
			}
		}
		t.Time = parsed
		return nil
	default:
		return fmt.Errorf("dbTime Scan: unsupported type %T", v)
	}
}

// NullBool implements sql.Scanner for SQLite boolean (INTEGER 0/1).
type NullBool struct {
	Valid bool
	Bool  int8 // 1 = true, 0 = false
}

func (nb *NullBool) Scan(v any) error {
	if v == nil {
		nb.Valid = false
		nb.Bool = 0
		return nil
	}
	nb.Valid = true
	switch x := v.(type) {
	case bool:
		if x {
			nb.Bool = 1
		} else {
			nb.Bool = 0
		}
	case int64:
		nb.Bool = int8(x)
	case int32:
		nb.Bool = int8(x)
	case int:
		nb.Bool = int8(x)
	case []byte:
		s := strings.ToLower(string(x))
		if s == "t" || s == "true" || s == "1" {
			nb.Bool = 1
		} else {
			nb.Bool = 0
		}
	case string:
		s := strings.ToLower(x)
		if s == "t" || s == "true" || s == "1" {
			nb.Bool = 1
		} else {
			nb.Bool = 0
		}
	default:
		return fmt.Errorf("NullBool Scan: unsupported type %T (value: %v)", v, v)
	}
	return nil
}

// ─── Core Data Structures ────────────────────────────────────────────────────

// Link represents a stored link entry from the database.
type Link struct {
	ID          int64  `json:"id"`
	Timestamp   string `json:"timestamp"` // ISO-8601 string
	URL         string `json:"url"`
	Title       string `json:"title"`
	Note        string `json:"note,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Content     string `json:"content,omitempty"`
	ContentLen  int    `json:"content_len"`
	ContentLimit int   `json:"content_limit"`
	Category    string `json:"category"`
	SummaryHTML string `json:"summary_html"`
	Included    *bool  `json:"included,omitempty"` // included flag (true/false/null)
	Marked      bool   `json:"marked"`             // marked flag
	Read        bool   `json:"read"`               // read flag
	Score       float64 `json:"score,omitempty"`  // semantic search similarity (0..1)
}

// ─── API Request / Response Types ────────────────────────────────────────────

type ShareRequest struct {
	IDs []int64 `json:"ids"`
}

// LinkAddRequest is the body for POST /linkshare.
type LinkAddRequest struct {
	URL  string `json:"url"`
	Note string `json:"note"`
}

// LinkAddResponse is returned by POST /linkshare.
type LinkAddResponse struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ProcessResponse is returned by GET /process-entries.
type ProcessResponse struct {
	Processed   int      `json:"processed"`
	Skipped     int      `json:"skipped"`
	Categorized int      `json:"categorized,omitempty"`
	Errors      []string `json:"errors,omitempty"`
	Duration    string   `json:"duration"`
}

// linkListResponse is the response for GET /api/links.
type linkListResponse struct {
	Links []Link `json:"links"`
	Total int    `json:"total"`
}

// mapRegion is a hand-drawn area on the topic map. The polygon is stored in
// world coordinates (the same ±50 space as the projected points).
type mapRegion struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Polygon [][2]float64 `json:"polygon"`
	Color   string      `json:"color,omitempty"`
}

// ─── Helper: scan a single row into a Link pointer ───────────────────────────

// scanLink reads one row from a DB rows iterator and returns a Link pointer.
// Used by getLinks, processEntries, and similar functions.
func scanLink(rows interface {
	Next() bool
	Scan(args ...interface{}) error
}) (*Link, error) {
	var l Link
	var cat sql.NullString
	var note sql.NullString
	var summary sql.NullString
	var content sql.NullString
	var ts dbTime

	var inc NullBool
	var markedVal NullBool
	var readVal NullBool

	if err := rows.Scan(&l.ID, &ts, &l.URL, &l.Title, &summary, &content, &cat, &note, &inc, &markedVal, &readVal); err != nil {
		return nil, err
	}

	l.Timestamp = ts.Format(time.RFC3339)
	if summary.Valid {
		l.Summary = summary.String
	}
	if content.Valid {
		l.Content = content.String
	}
	if cat.Valid {
		l.Category = cat.String
	}
	if note.Valid {
		l.Note = note.String
	}
	// Included: only set when explicitly true
	if inc.Valid && inc.Bool > 0 {
		v := true
		l.Included = &v
	} else if !inc.Valid || inc.Bool == 0 {
		v := false
		l.Included = &v
	}
	// Marked: default false
	if markedVal.Valid && markedVal.Bool > 0 {
		l.Marked = true
	}
	// Read: default false
	if readVal.Valid && readVal.Bool > 0 {
		l.Read = true
	}

	// Markdown → HTML rendering for the summary
	var buf strings.Builder
	if l.Summary != "" {
		_ = mdRenderer.Convert([]byte(l.Summary), &buf)
		l.SummaryHTML = buf.String()
	}

	return &l, nil
}
