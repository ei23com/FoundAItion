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
