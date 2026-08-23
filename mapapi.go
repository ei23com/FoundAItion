package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── Map Job State ───────────────────────────────────────────────────────────
//
// The vectormap has two heavy background steps: embedding missing entries and
// projecting the full vector set to 2D. A single pipeline job (embedding →
// projection) may run at a time; its progress is polled via /api/map/status.

type mapJobSnapshot struct {
	Running bool   `json:"running"`
	Phase   string `json:"phase"` // idle | embedding | projecting | error
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Message string `json:"message"`
	Pct     int    `json:"pct"`
}

type mapJobState struct {
	mu sync.Mutex `json:"-"`

	running bool
	phase   string
	done    int
	total   int
	message string
}

func (m *mapJobState) tryStart() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return false
	}
	m.running = true
	m.phase = "embedding"
	m.done, m.total = 0, 0
	m.message = ""
	return true
}

func (m *mapJobState) set(phase string, done, total int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.phase = phase
	m.done, m.total = done, total
	if message != "" {
		m.message = message
	}
}

func (m *mapJobState) fail(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	m.phase = "error"
	m.message = message
}

func (m *mapJobState) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	m.phase = "idle"
}

func (m *mapJobState) snapshot() mapJobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	pct := 0
	if m.total > 0 && m.done >= 0 {
		pct = 100 * m.done / m.total
		if pct > 100 {
			pct = 100
		}
	}
	return mapJobSnapshot{Running: m.running, Phase: m.phase, Done: m.done, Total: m.total, Message: m.message, Pct: pct}
}

// ─── Pipeline ────────────────────────────────────────────────────────────────

// startMapPipeline kicks off the background pipeline if no job is running.
// With projectOnly=true the embedding phase is skipped entirely (used when
// embeddings would cost money and were not explicitly requested).
// Returns false when a job is already in flight.
func (a *App) startMapPipeline(force, projectOnly bool) bool {
	if !a.mapJob.tryStart() {
		return false
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[map] pipeline panic: %v", r)
				a.mapJob.fail(fmt.Sprintf("Unerwarteter Fehler: %v", r))
			} else if a.mapJob.snapshot().Phase != "error" {
				a.mapJob.finish()
			}
		}()
		a.runMapPipeline(force, projectOnly)
	}()
	return true
}

// runMapPipeline embeds all missing entries (all entries when force; skipped
// entirely when projectOnly), then projects every usable vector and persists
// the 2D positions.
func (a *App) runMapPipeline(force, projectOnly bool) {
	model := a.cfg.ActiveEmbeddingModel()

	stats, err := a.loadAllVectors(model)
	if err != nil {
		a.mapJob.fail(fmt.Sprintf("Vektoren konnten nicht geladen werden: %v", err))
		return
	}

	allIDs := make([]int64, 0, stats.Total)
	for id := range stats.Vectors {
		allIDs = append(allIDs, id)
	}
	allIDs = append(allIDs, stats.MissingID...)

	var toEmbed []int64
	if !projectOnly {
		if force {
			toEmbed = allIDs
		} else {
			toEmbed = stats.MissingID
		}
	}

	if len(toEmbed) > 0 {
		a.mapJob.set("embedding", 0, len(toEmbed), "")
		done := 0
		for start := 0; start < len(toEmbed); start += 500 {
			end := min(len(toEmbed), start+500)
			chunk := toEmbed[start:end]

			texts, terr := a.fetchEmbedTexts(chunk)
			if terr != nil {
				a.mapJob.fail(fmt.Sprintf("Einträge konnten nicht geladen werden: %v", terr))
				return
			}

			for _, id := range chunk {
				text := strings.TrimSpace(texts[id])
				if text == "" {
					// nothing to embed yet (no title/summary) – stay missing
					done++
					continue
				}
				vec, verr := a.embedText(text)
				if verr != nil {
					if model == LocalHashModel {
						log.Printf("[map] WARN: hash embedding failed for id=%d: %v", id, verr)
					} else {
						a.mapJob.fail(fmt.Sprintf("Embedding-API-Fehler (id=%d): %v", id, verr))
						return
					}
				} else if serr := a.saveVector(id, vec, model); serr != nil {
					log.Printf("[map] WARN: save vector id=%d: %v", id, serr)
				}
				done++
				if done%10 == 0 || done == len(toEmbed) {
					a.mapJob.set("embedding", done, len(toEmbed), "")
				}
			}
		}
	}

	stats, err = a.loadAllVectors(model)
	if err != nil {
		a.mapJob.fail(fmt.Sprintf("Vektoren konnten nicht geladen werden: %v", err))
		return
	}
	n := len(stats.Vectors)
	if n < 3 {
		a.mapJob.fail(fmt.Sprintf("Nur %d Einträge mit Vektor – es werden mindestens 3 benötigt.", n))
		return
	}

	ids := make([]int64, 0, n)
	for id := range stats.Vectors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	X := make([][]float32, n)
	for i, id := range ids {
		X[i] = stats.Vectors[id]
	}

	fp := vectorFingerprint(append([]int64(nil), ids...), model+"/"+mapLayoutAlgoID)
	if pos, ok, perr := loadCachedPositions(a.db, fp); perr == nil && ok && len(pos) == n {
		// identical data (e.g. forced re-embed produced the same vectors): done
		return
	}

	a.mapJob.set("projecting", 0, 100, "")
	points, err := project2D(ids, X, func(pct int) {
		a.mapJob.set("projecting", pct, 100, "")
	})
	if err != nil {
		a.mapJob.fail(fmt.Sprintf("Projektion fehlgeschlagen: %v", err))
		return
	}
	if err := storePositions(a.db, points, model, fp); err != nil {
		a.mapJob.fail(fmt.Sprintf("Positionen konnten nicht gespeichert werden: %v", err))
		return
	}
	log.Printf("[map] pipeline finished: %d embedded pass, %d points projected", len(toEmbed), n)
}

// fetchEmbedTexts returns id → embedding text for a batch of ids.
func (a *App) fetchEmbedTexts(ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(`SELECT "id", "title", "summary" FROM %s WHERE "id" IN (%s)`, TableName, placeholders)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("fetch texts: %w", err)
	}
	defer rows.Close()

	var title sql.NullString
	var summary sql.NullString
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id, &title, &summary); err != nil {
			return nil, fmt.Errorf("scan text row: %w", err)
		}
		out[id] = embeddingText(Link{Title: title.String, Summary: summary.String})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate text rows: %w", err)
	}
	return out, nil
}

// ─── Handlers ────────────────────────────────────────────────────────────────

type mapPointOut struct {
	ID        int64   `json:"id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Title     string  `json:"title"`
	Category  string  `json:"category"`
	Timestamp string  `json:"timestamp"`
	Read      bool    `json:"read"`
}

// GET /api/map → {status, model, mode, counts, points}
func (a *App) getMap(w http.ResponseWriter, r *http.Request) {
	model := a.cfg.ActiveEmbeddingModel()
	mode := "hash"
	if model != LocalHashModel {
		mode = "api"
	}

	stats, err := a.loadAllVectors(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ids := make([]int64, 0, len(stats.Vectors))
	for id := range stats.Vectors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fp := vectorFingerprint(append([]int64(nil), ids...), model+"/"+mapLayoutAlgoID)

	pos, ok, perr := loadCachedPositions(a.db, fp)
	status := "ready"
	jobMsg := ""
	if perr != nil || !ok {
		// API mode must never spend money implicitly: any pipeline started
		// from a plain GET skips the embedding phase entirely. Hash mode can
		// embed freely (offline and instant).
		projectOnly := mode == "api" && len(stats.MissingID) > 0
		snap := a.mapJob.snapshot()
		switch {
		case snap.Phase == "error":
			// Last auto-run failed – do not restart in a loop.
			status = "error"
			jobMsg = snap.Message
		case len(stats.Vectors) < 3 && mode != "hash":
			status = "needs-embeddings"
		default:
			started := a.startMapPipeline(false, projectOnly)
			switch {
			case !started:
				status = "waiting" // job already running
			case projectOnly || len(stats.Vectors) >= 3:
				status = "projecting"
			default:
				status = "embedding"
			}
		}
		pos = nil
	}

	points := make([]mapPointOut, 0, len(ids))
	if pos != nil && len(pos) > 0 {
		metas, merr := a.fetchMapMeta(ids)
		if merr != nil {
			log.Printf("[map] WARN: metadata unavailable, returning bare points: %v", merr)
			metas = map[int64]mapLinkMeta{}
		}
		for _, id := range ids {
			p, has := pos[id]
			if !has {
				continue
			}
			m := metas[id]
			points = append(points, mapPointOut{
				ID:        id,
				X:         p.X,
				Y:         p.Y,
				Title:     m.Title,
				Category:  m.Category,
				Timestamp: normalizeTimestamp(m.Timestamp),
				Read:      m.Read,
			})
		}
	}

	resp := map[string]any{
		"status": status,
		"model":  model,
		"mode":   mode,
		"counts": map[string]int{
			"embedded": len(stats.Vectors),
			"missing":  len(stats.MissingID),
			"total":    stats.Total,
		},
		"points": points,
	}
	if regions, rerr := a.listMapRegions(); rerr == nil {
		resp["regions"] = regions
	} else {
		resp["regions"] = []mapRegion{}
	}
	if jobMsg != "" {
		resp["message"] = jobMsg
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[map] WARN: encode response: %v", err)
	}
}

// mapLinkMeta is the metadata attached to each map point.
type mapLinkMeta struct {
	Title     string `json:"title"`
	Category  string `json:"category"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`
}

// fetchMapMeta batch-loads title/category/timestamp/read for map points.
func (a *App) fetchMapMeta(ids []int64) (map[int64]mapLinkMeta, error) {
	out := make(map[int64]mapLinkMeta, len(ids))
	scanChunk := func(rows *sql.Rows) error {
		defer rows.Close()
		var title, cat, ts sql.NullString
		var read sql.NullInt64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id, &title, &cat, &ts, &read); err != nil {
				return fmt.Errorf("scan map meta: %w", err)
			}
			out[id] = mapLinkMeta{Title: title.String, Category: cat.String, Timestamp: ts.String, Read: read.Int64 == 1}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate map meta: %w", err)
		}
		return nil
	}
	for start := 0; start < len(ids); start += 500 {
		end := min(len(ids), start+500)
		chunk := ids[start:end]

		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		query := fmt.Sprintf(`SELECT "id", "title", "category", "timestamp", "read" FROM %s WHERE "id" IN (%s)`, TableName, placeholders)
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}

		rows, err := a.db.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("fetch map meta: %w", err)
		}
		if err := scanChunk(rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// semanticLinks handles GET /api/links?q=...&semantic=true: embeds the query,
// ranks every entry by cosine similarity and returns the top hits ordered by
// score. Entries without a vector (or with another model) are ignored.
func (a *App) semanticLinks(w http.ResponseWriter, r *http.Request, q string) {
	model := a.cfg.ActiveEmbeddingModel()

	vec, err := a.embedText(strings.TrimSpace(q))
	if err != nil {
		log.Printf("[search] embed query failed: %v", err)
		http.Error(w, fmt.Sprintf("Embedding fehlgeschlagen: %v", err), http.StatusBadGateway)
		return
	}

	stats, err := a.loadAllVectors(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type scored struct {
		id    int64
		score float64
	}
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")
	hits := make([]scored, 0, len(stats.Vectors))
	for id, v := range stats.Vectors {
		hits = append(hits, scored{id: id, score: cosineF32(vec, v)})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	const maxHits = 60
	if len(hits) > maxHits {
		hits = hits[:maxHits]
	}

	links := make([]Link, 0, len(hits))
	for _, h := range hits {
		l, err := a.fetchLinkByID(h.id, true)
		if err != nil {
			continue
		}
		// Datumsfilter (erste 10 Zeichen = ISO-Datum).
		tsDate := ""
		if len(l.Timestamp) >= 10 {
			tsDate = l.Timestamp[:10]
		}
		if dateFrom != "" && tsDate != "" && tsDate < dateFrom {
			continue
		}
		if dateTo != "" && tsDate != "" && tsDate > dateTo {
			continue
		}
		l.ContentLen = len(l.Content)
		l.ContentLimit = int(float64(a.cfg.SummaryMaxTokens) * 3.5)
		l.Score = h.score
		l.Content = "" // wie Listenansicht: Content nur auf Wunsch
		links = append(links, *l)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(linkListResponse{Links: links, Total: len(links)})
}

// ─── Map regions (hand-drawn areas) ──────────────────────────────────────────

// ensureMapRegionsTable creates the table for persistent map areas.
func ensureMapRegionsTable(db *sql.DB) error {
	q := `CREATE TABLE IF NOT EXISTS map_regions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		polygon TEXT NOT NULL,
		color TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(q); err != nil {
		return err
	}
	return nil
}

func scanMapRegions(rows *sql.Rows) ([]mapRegion, error) {
	defer rows.Close()
	out := make([]mapRegion, 0, 8)
	for rows.Next() {
		var rg mapRegion
		var poly string
		if err := rows.Scan(&rg.ID, &rg.Name, &poly, &rg.Color); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(poly), &rg.Polygon); err != nil {
			return nil, fmt.Errorf("region %d: polygon: %w", rg.ID, err)
		}
		out = append(out, rg)
	}
	return out, rows.Err()
}

func (a *App) listMapRegions() ([]mapRegion, error) {
	rows, err := a.db.Query(`SELECT "id", "name", "polygon", "color" FROM map_regions ORDER BY "id"`)
	if err != nil {
		return nil, err
	}
	return scanMapRegions(rows)
}

// handleMapRegions serves GET/POST/PUT/DELETE /api/map/regions.
func (a *App) handleMapRegions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	switch r.Method {
	case http.MethodGet:
		regions, err := a.listMapRegions()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"regions": regions})

	case http.MethodPost:
		var in mapRegion
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" || len(in.Polygon) < 3 {
			http.Error(w, "name and ≥3 polygon points required", http.StatusBadRequest)
			return
		}
		polyJSON, _ := json.Marshal(in.Polygon)
		res, err := a.db.Exec(`INSERT INTO map_regions ("name", "polygon", "color") VALUES (?, ?, ?)`,
			in.Name, string(polyJSON), in.Color)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})

	case http.MethodPut:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "missing or invalid id", http.StatusBadRequest)
			return
		}
		var in mapRegion
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		setClauses, args := []string{}, []any{}
		if strings.TrimSpace(in.Name) != "" {
			setClauses = append(setClauses, `"name" = ?`)
			args = append(args, strings.TrimSpace(in.Name))
		}
		if len(in.Polygon) >= 3 {
			polyJSON, _ := json.Marshal(in.Polygon)
			setClauses = append(setClauses, `"polygon" = ?`)
			args = append(args, string(polyJSON))
		}
		if in.Color != "" {
			setClauses = append(setClauses, `"color" = ?`)
			args = append(args, in.Color)
		}
		if len(setClauses) == 0 {
			http.Error(w, "nothing to update", http.StatusBadRequest)
			return
		}
		q := fmt.Sprintf(`UPDATE map_regions SET %s WHERE "id" = ?`, strings.Join(setClauses, ", "))
		args = append(args, id)
		if _, err := a.db.Exec(q, args...); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "missing or invalid id", http.StatusBadRequest)
			return
		}
		if _, err := a.db.Exec(`DELETE FROM map_regions WHERE "id" = ?`, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// normalizeTimestamp converts stored timestamps to RFC3339.
func normalizeTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.ParseInLocation(layout, ts, time.UTC); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ts
}

// POST /api/map/embeddings?force=true → starts the background pipeline.
func (a *App) postMapEmbeddings(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if !a.startMapPipeline(force, false) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"job läuft bereits"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"ok":true}`))
}

// GET /api/map/status → job progress + lightweight vector counts.
func (a *App) getMapStatus(w http.ResponseWriter, r *http.Request) {
	model := a.cfg.ActiveEmbeddingModel()

	var total int
	embedded := 0
	row := a.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN "vec_model" = ? THEN 1 ELSE 0 END), 0) FROM %s`, TableName), model)
	if err := row.Scan(&total, &embedded); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]any{
		"job":    a.mapJob.snapshot(),
		"model":  model,
		"mode":   "hash",
		"counts": map[string]int{"embedded": embedded, "missing": total - embedded, "total": total},
	}
	if model != LocalHashModel {
		resp["mode"] = "api"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

// POST /api/map/query {"query": "..."} → semantic top matches.
func (a *App) postMapQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Query) == "" {
		http.Error(w, "missing query", http.StatusBadRequest)
		return
	}

	model := a.cfg.ActiveEmbeddingModel()
	qvec, err := a.embedText(strings.TrimSpace(body.Query))
	if err != nil {
		http.Error(w, "query embedding failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	stats, err := a.loadAllVectors(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	type scored struct {
		ID    int64   `json:"id"`
		Score float64 `json:"score"`
	}
	out := make([]scored, 0, len(stats.Vectors))
	for id, v := range stats.Vectors {
		if len(v) != len(qvec) {
			continue
		}
		s := cosineF32(qvec, v)
		if s > -0.9 { // keep only meaningful matches (hash mode scores hover near 0)
			out = append(out, scored{ID: id, Score: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 300 {
		out = out[:300]
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"scores": out, "count": len(out)})
}

// cosineF32 computes the cosine similarity of two equal-length vectors.
func cosineF32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
