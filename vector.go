package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

// ─── Vector Storage ──────────────────────────────────────────────────────────
//
// Vectors are stored as binary BLOBs of little-endian float32 values.
// The model that produced a vector is stored in "vec_model". Only vectors
// whose vec_model matches the currently active model are used for the map –
// switching embedding models invalidates old positions automatically.

// maxEmbedTextChars limits how much text is sent to the embedder (roughly
// 3500 tokens, comfortably inside a default 4096-token context window).
const maxEmbedTextChars = 12000

// encodeVector serializes a float32 slice as little-endian binary.
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector deserializes a little-endian float32 BLOB.
func decodeVector(b []byte) ([]float32, error) {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil, fmt.Errorf("invalid vector blob (len=%d)", len(b))
	}
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

// embeddingText builds the text that gets embedded for an entry:
// title + summary (like the original Python visualizer did).
func embeddingText(link Link) string {
	var sb strings.Builder
	if link.Title != "" {
		sb.WriteString(link.Title)
		sb.WriteString("\n\n")
	}
	s := link.Summary
	if s == "zu langer Text" {
		s = ""
	}
	sb.WriteString(s)
	text := sb.String()
	runes := []rune(text)
	if len(runes) > maxEmbedTextChars {
		text = string(runes[:maxEmbedTextChars])
	}
	return strings.TrimSpace(text)
}

// ─── DB: read / write vectors ────────────────────────────────────────────────

// saveVector stores the embedding for one entry.
func (a *App) saveVector(id int64, vec []float32, model string) error {
	query := fmt.Sprintf(`UPDATE %s SET "vector" = ?, "vec_model" = ? WHERE "id" = ?`, TableName)
	if _, err := a.db.Exec(query, encodeVector(vec), model, id); err != nil {
		return fmt.Errorf("save vector id=%d: %w", id, err)
	}
	return nil
}

// clearVector removes the embedding of one entry (e.g. after summary/title changed).
func (a *App) clearVector(id int64) error {
	query := fmt.Sprintf(`UPDATE %s SET "vector" = NULL, "vec_model" = NULL WHERE "id" = ?`, TableName)
	if _, err := a.db.Exec(query, id); err != nil {
		return fmt.Errorf("clear vector id=%d: %w", id, err)
	}
	return nil
}

// afterTextChange updates the entry's vector after its title/summary changed:
// hash mode re-embeds immediately (deterministic, free); API mode clears the
// stale vector – re-embedding only happens via the explicit backfill job.
func (a *App) afterTextChange(id int64) {
	if a.cfg.ActiveEmbeddingModel() != LocalHashModel {
		if err := a.clearVector(id); err != nil {
			log.Printf("[map] WARN: clear vector id=%d: %v", id, err)
		}
		return
	}

	var title, summary string
	if err := a.db.QueryRow(fmt.Sprintf(`SELECT "title", "summary" FROM %s WHERE "id" = ?`, TableName), id).Scan(&title, &summary); err != nil {
		log.Printf("[map] WARN: re-embed lookup id=%d: %v", id, err)
		return
	}
	text := strings.TrimSpace(embeddingText(Link{Title: title, Summary: summary}))
	if text == "" {
		a.clearVector(id)
		return
	}
	vec := hashEmbed(text)
	if err := a.saveVector(id, vec, LocalHashModel); err != nil {
		log.Printf("[map] WARN: re-embed id=%d: %v", id, err)
	}
}

// mapVectorStats describes the state of stored vectors for the active model.
type mapVectorStats struct {
	Vectors   map[int64][]float32 // id → vector (only entries matching active model)
	MissingID []int64             // ids without a usable vector for the active model
	Total     int                 // total row count
}

// loadAllVectors reads all vectors from the DB. Only rows whose vec_model
// equals wantModel are considered usable; everything else is "missing" and
// will be (re-)embedded by the next embed run.
func (a *App) loadAllVectors(wantModel string) (*mapVectorStats, error) {
	query := fmt.Sprintf(`SELECT "id", "vector", "vec_model" FROM %s ORDER BY "id"`, TableName)
	rows, err := a.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("load vectors: %w", err)
	}
	defer rows.Close()

	stats := &mapVectorStats{Vectors: make(map[int64][]float32)}
	var modelCol sql.NullString
	for rows.Next() {
		var id int64
		var blob sql.RawBytes
		if err := rows.Scan(&id, &blob, &modelCol); err != nil {
			return nil, fmt.Errorf("scan vector row: %w", err)
		}
		stats.Total++

		usable := false
		if len(blob) > 0 && modelCol.Valid && modelCol.String == wantModel {
			if v, derr := decodeVector([]byte(blob)); derr == nil && len(v) > 0 {
				stats.Vectors[id] = v
				usable = true
			} else {
				log.Printf("[map] WARN: corrupt vector blob for id=%d, will re-embed", id)
			}
		}
		if !usable {
			stats.MissingID = append(stats.MissingID, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vector rows: %w", err)
	}
	log.Printf("[map] vectors: %d usable (model=%s), %d missing, %d total", len(stats.Vectors), wantModel, len(stats.MissingID), stats.Total)
	return stats, nil
}

// ─── Embedding Client ────────────────────────────────────────────────────────

// embedText produces an embedding for the given text.
//   - With EMBEDDING_MODEL set: OpenAI-compatible POST {base}/embeddings
//   - Without: deterministic local feature-hashing embedder (offline)
func (a *App) embedText(text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text")
	}

	model := a.cfg.ActiveEmbeddingModel()
	if model == LocalHashModel {
		return hashEmbed(text), nil
	}
	return a.embedViaAPI(text, model)
}

// embedViaAPI calls the OpenAI-compatible /embeddings endpoint.
func (a *App) embedViaAPI(text, model string) ([]float32, error) {
	payload, err := json.Marshal(map[string]any{
		"model":  model,
		"input":  text,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(a.cfg.EmbeddingBaseURL, "/")+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.EmbeddingAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.EmbeddingAPIKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request %s: %w", a.cfg.EmbeddingBaseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API %d: %.300s", resp.StatusCode, string(body))
	}

	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding API returned no data")
	}

	vec := make([]float32, len(out.Data[0].Embedding))
	for i, f := range out.Data[0].Embedding {
		vec[i] = float32(f)
	}
	return vec, nil
}

// ─── Local Fallback Embedder ─────────────────────────────────────────────────
//
// Deterministic signed feature hashing over word unigrams + bigrams.
// Not as good as a trained embedding model, but works offline and still
// clusters topically (shared vocabulary ⇒ similar vectors).

// hashDim is the dimensionality of local hash vectors.
const hashDim = 512

func hashEmbed(text string) []float32 {
	v := make([]float32, hashDim)
	words := tokenizeWords(strings.ToLower(text))

	for i, w := range words {
		if w == "" {
			continue
		}
		addHashFeature(v, w, 1.0)
		if i+1 < len(words) && words[i+1] != "" {
			addHashFeature(v, w+" "+words[i+1], 0.5)
		}
	}

	// L2-normalize (zero vector stays zero)
	norm := 0.0
	for _, f := range v {
		norm += float64(f) * float64(f)
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range v {
			v[i] = float32(float64(v[i]) / norm)
		}
	}
	return v
}

// tokenizeWords splits text into word-like tokens (runs of letters/digits,
// Unicode-aware via a simple rune check).
func tokenizeWords(s string) []string {
	var words []string
	start := -1
	for i, r := range s {
		isWordChar := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127
		if isWordChar {
			if start < 0 {
				start = i
			}
		} else {
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
		}
	}
	if start >= 0 {
		words = append(words, s[start:])
	}
	return words
}

// addHashFeature adds ±weight to a bucket selected by FNV-1a hash.
func addHashFeature(v []float32, key string, weight float32) {
	h := fnv64(key)
	idx := int(h % uint64(len(v)))
	sign := float32(1)
	if h&0x8000000000000000 != 0 {
		sign = -1
	}
	v[idx] += sign * weight
}

// fnv64 is FNV-1a (64 bit), inlined to avoid an extra import for one use.
func fnv64(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}
