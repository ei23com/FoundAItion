package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ─── Database Connection ─────────────────────────────────────────────────────

// openSQLite opens (or creates) a SQLite database file using modernc.org/sqlite.
func openSQLite(filePath string) (*sql.DB, error) {
	if !filepath.IsAbs(filePath) {
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			return nil, fmt.Errorf("resolve SQLite path: %w", err)
		}
		filePath = absPath
	}

	// NOTE: modernc.org/sqlite only honors `_pragma=…` query parameters
	// (`_timeout`/`_journal_mode` would be silently ignored).
	// busy_timeout must come first so later pragmas wait instead of failing.
	dsn := filePath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}

	log.Printf("Connected to SQLite: %s", filePath)
	return db, nil
}

// ─── Schema Management ───────────────────────────────────────────────────────

// ensureTableExists creates the links table if it does not exist.
// All column names are English for consistency.
func ensureTableExists(db *sql.DB, tblName string) error {
	createSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp    TEXT NOT NULL,
			url          TEXT NOT NULL,
			title        TEXT,
			note         TEXT,
			summary      TEXT,
			content      TEXT,
			category     TEXT,
			included     INTEGER DEFAULT 0,
			marked       INTEGER DEFAULT 0,
			read         INTEGER DEFAULT 0,
			vector       BLOB,
			vec_model    TEXT
		)`, tblName)

	_, err := db.Exec(createSQL)
	if err != nil {
		return fmt.Errorf("ensure table %s: %w", tblName, err)
	}

	var cnt int
	querySQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s LIMIT 1`, tblName)
	if err := db.QueryRow(querySQL).Scan(&cnt); err != nil {
		return fmt.Errorf("verify table %s: %w", tblName, err)
	}

	log.Printf("Table %s ready (%d entries)", tblName, cnt)
	return nil
}

// ─── Schema Migration: List indexes ──────────────────────────────────────────

// listIndexes keep the list/feed endpoints from doing full table scans. The
// links table is ~250 MB because every row carries the scraped text (avg 35 kB,
// up to 1.2 MB), so an unindexed COUNT(*) or GROUP BY reads the whole file –
// that is what makes the first list request after a start drag.
//
// The read index is an expression index: the queries use IFNULL("read", 0) so
// that legacy NULL rows count as unread, and SQLite can only use an index when
// the expression matches exactly.
var listIndexes = []struct {
	name string
	tmpl string // %s = table name
}{
	{"idx_links_category", `CREATE INDEX IF NOT EXISTS idx_links_category ON %s ("category")`},
	{"idx_links_read", `CREATE INDEX IF NOT EXISTS idx_links_read ON %s (IFNULL("read", 0), "id")`},
	{"idx_links_timestamp", `CREATE INDEX IF NOT EXISTS idx_links_timestamp ON %s ("timestamp")`},
	{"idx_links_vec_model", `CREATE INDEX IF NOT EXISTS idx_links_vec_model ON %s ("vec_model")`},
}

// ensureListIndexes creates the list-endpoint indexes. Idempotent; ANALYZE is
// run only when something was actually created, so a normal start pays nothing.
func ensureListIndexes(db *sql.DB, tblName string) error {
	countSQL := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_links%'`
	var before int
	if err := db.QueryRow(countSQL).Scan(&before); err != nil {
		return fmt.Errorf("count indexes: %w", err)
	}

	for _, idx := range listIndexes {
		if _, err := db.Exec(fmt.Sprintf(idx.tmpl, tblName)); err != nil {
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}

	var after int
	if err := db.QueryRow(countSQL).Scan(&after); err != nil {
		return fmt.Errorf("count indexes: %w", err)
	}
	if after > before {
		if _, err := db.Exec(`ANALYZE`); err != nil {
			log.Printf("WARN: ANALYZE after index creation failed: %v", err)
		}
		log.Printf("[migration] created %d list indexes (total %d)", after-before, after)
	}
	return nil
}

// ─── Schema Migration: Vectormap ─────────────────────────────────────────────

// vectorColumns lists the columns added by the vectormap feature.
var vectorColumns = []struct {
	name string
	def  string // SQLite column definition (without name)
}{
	{"vector", "BLOB"},     // text embedding, binary float32 little-endian
	{"vec_model", "TEXT"},  // model that produced the vector (empty/NULL for legacy rows)
}

// ensureVectorColumns adds the vectormap columns if they do not exist yet.
// The migration is idempotent and safe to run on every startup.
func ensureVectorColumns(db *sql.DB, tblName string) error {
	for _, col := range vectorColumns {
		exists, err := columnExists(db, tblName, col.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		alterSQL := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, tblName, col.name, col.def)
		if _, err := db.Exec(alterSQL); err != nil {
			return fmt.Errorf("add column %s to %s: %w", col.name, tblName, err)
		}
		log.Printf("[migration] added column '%s' to table %s", col.name, tblName)
	}

	var total int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tblName)).Scan(&total); err != nil {
		return fmt.Errorf("verify table after migration: %w", err)
	}
	log.Printf("[migration] vectormap columns ready (%d entries)", total)
	return nil
}

// columnExists checks whether the named column is present in the table.
func columnExists(db *sql.DB, tblName, colName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tblName))
	if err != nil {
		return false, fmt.Errorf("inspect schema %s: %w", tblName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("inspect schema %s: %w", tblName, err)
		}
		if name == colName {
			return true, nil
		}
	}
	return false, nil
}
