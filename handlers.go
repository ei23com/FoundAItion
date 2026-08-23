package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ─── HTTP Router ─────────────────────────────────────────────────────────────

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/links":
		switch r.Method {
		case http.MethodGet:
			a.getLinks(w, r)
		case http.MethodPatch:
			a.updateLink(w, r)
		case http.MethodDelete:
			a.deleteLink(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/categories":
		a.getCategories(w, r)
	case "/api/share":
		a.share(w, r)
	case "/linkshare":
		if r.Method == http.MethodPost {
			a.receiveLink(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/process-entries":
		if r.Method == http.MethodGet {
			a.processEntries(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/manual":
		lang := r.URL.Query().Get("lang")
		raw := manualDE
		if lang == "en" {
			raw = manualEN
		}
		var buf strings.Builder
		_ = mdRenderer.Convert([]byte(raw), &buf)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, buf.String())
	case "/api/config":
		switch r.Method {
		case http.MethodGet:
			a.getConfig(w, r)
		case http.MethodPost:
			a.saveConfig(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/progress":
		if r.Method == http.MethodGet {
			a.getProgress(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/language":
		if r.Method == http.MethodGet {
			a.getLanguage(w, r)
		} else if r.Method == http.MethodPost {
			a.setLanguage(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/categorize":
		if r.Method == http.MethodGet {
			a.categorizeEntries(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/map":
		if r.Method == http.MethodGet {
			a.getMap(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/map/embeddings":
		if r.Method == http.MethodPost {
			a.postMapEmbeddings(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/map/status":
		if r.Method == http.MethodGet {
			a.getMapStatus(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/map/query":
		if r.Method == http.MethodPost {
			a.postMapQuery(w, r)
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "/api/map/regions":
		a.handleMapRegions(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ─── List Links – GET /api/links ─────────────────────────────────────────────

func (a *App) getLinks(w http.ResponseWriter, r *http.Request) {
	wantContent := r.URL.Query().Get("content") == "true"

	// ── Single link by ID ───────────────────────────────────────────────────
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		l, err := a.fetchLinkByID(id, wantContent)
		if err == sql.ErrNoRows {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		} else if err != nil {
			log.Printf("ERROR: fetch link %d: %v", id, err)
			http.Error(w, "query error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(linkListResponse{Links: []Link{*l}, Total: 1})
		return
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	offset := (page - 1) * pageSizeDB

	catFilter := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")

	// ── Semantic search (RAG-style) ─────────────────────────────────────────
	// ?q=...&semantic=true ranks all entries by embedding cosine similarity.
	if r.URL.Query().Get("semantic") == "true" && strings.TrimSpace(q) != "" {
		a.semanticLinks(w, r, q)
		return
	}

	urlLike := r.URL.Query().Get("url_like")
	noteFilter := r.URL.Query().Get("note")
	includeFilter := r.URL.Query().Get("included")
	markedFilter := r.URL.Query().Get("marked") // "true" | "false"
	readFilter := r.URL.Query().Get("read")     // "true" | "false"
	var whereClauses []string
	var args []any

	if catFilter != "" {
		whereClauses = append(whereClauses, `"category" = ?`)
		args = append(args, catFilter)
	}
	if includeFilter == "true" {
		whereClauses = append(whereClauses, `"included" = 1`)
	}
	if markedFilter == "true" {
		whereClauses = append(whereClauses, `"marked" = 1`)
	}
	if readFilter == "true" {
		whereClauses = append(whereClauses, `"read" = 1`)
	} else if readFilter == "false" {
		whereClauses = append(whereClauses, `("read" = 0 OR "read" IS NULL)`)
	}
	if urlLike != "" {
		pattern := "%" + urlLike + "%"
		whereClauses = append(whereClauses, `"url" LIKE ?`)
		args = append(args, pattern)
	}
	if q != "" {
		pattern := "%" + q + "%"
		whereClauses = append(whereClauses,
			`("title" LIKE ? OR "url" LIKE ? OR "summary" LIKE ? OR "category" LIKE ?)`,
		)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if noteFilter != "" {
		pattern := "%" + noteFilter + "%"
		whereClauses = append(whereClauses, `"note" LIKE ?`)
		args = append(args, pattern)
	}
	if dateFrom := r.URL.Query().Get("date_from"); dateFrom != "" {
		// Timestamps sind "YYYY-MM-DD HH:MM:SS" → Datumspräfixvergleich genügt.
		whereClauses = append(whereClauses, `substr("timestamp",1,10) >= ?`)
		args = append(args, dateFrom)
	}
	if dateTo := r.URL.Query().Get("date_to"); dateTo != "" {
		whereClauses = append(whereClauses, `substr("timestamp",1,10) <= ?`)
		args = append(args, dateTo)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count for pagination
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", TableName, whereSQL)
	var total int
	if err := a.db.QueryRowContext(r.Context(), countSQL, countArgs...).Scan(&total); err != nil {
		log.Printf("WARN: count query failed: %v", err)
	}

	// Fetch data
	dataSQL := fmt.Sprintf(`SELECT "id", "timestamp", "url", "title", "summary", "content", "category", "note", "included", "marked", "read"
	FROM %s%s
	ORDER BY "id" DESC
	LIMIT ? OFFSET ?`, TableName, whereSQL)
	dataArgs := append(append([]any{}, args...), pageSizeDB, offset)

	rows, err := a.db.QueryContext(r.Context(), dataSQL, dataArgs...)
	if err != nil {
		log.Printf("ERROR: links query failed: %v", err)
		http.Error(w, "query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			log.Printf("WARN: scan error: %v", err)
			continue
		}
		l.ContentLen = len(l.Content)
		l.ContentLimit = int(float64(a.cfg.SummaryMaxTokens) * 3.5)
		links = append(links, *l)
	}

	// Content only im JSON mitsenden wenn explizit angefordert
	if !wantContent {
		for i := range links {
			links[i].Content = ""
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(linkListResponse{Links: links, Total: total})
}

// ─── Update Link – PATCH /api/links?id=X&note=...&included=...&marked=...&read=...

func (a *App) updateLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "missing or invalid id", http.StatusBadRequest)
		return
	}

	var setClauses []string
	var args []any

	// Update title
	if t := r.URL.Query().Get("title"); t != "" {
		setClauses = append(setClauses, `"title" = ?`)
		args = append(args, t)
	}

	// Update raw content (URL-encoded)
	if c := r.URL.Query().Get("content"); c != "" {
		setClauses = append(setClauses, `"content" = ?`)
		args = append(args, c)
	}

	// Update note
	if n := r.URL.Query().Get("note"); n != "" {
		setClauses = append(setClauses, `"note" = ?`)
		args = append(args, n)
	}

	// Update included ("true" | "false")
	if inc := r.URL.Query().Get("included"); inc == "true" {
		setClauses = append(setClauses, `"included" = ?`)
		args = append(args, 1)
	} else if inc == "false" {
		setClauses = append(setClauses, `"included" = ?`)
		args = append(args, 0)
	}

	// Update marked ("true" | "false")
	if m := r.URL.Query().Get("marked"); m == "true" {
		setClauses = append(setClauses, `"marked" = ?`)
		args = append(args, 1)
	} else if m == "false" {
		setClauses = append(setClauses, `"marked" = ?`)
		args = append(args, 0)
	}

	// Update read ("true" | "false")
	if rd := r.URL.Query().Get("read"); rd == "true" {
		setClauses = append(setClauses, `"read" = ?`)
		args = append(args, 1)
	} else if rd == "false" {
		setClauses = append(setClauses, `"read" = ?`)
		args = append(args, 0)
	}

	// Update category
	if cat := r.URL.Query().Get("category"); cat != "" {
		setClauses = append(setClauses, `"category" = ?`)
		args = append(args, cat)
	}

	// Clear summary ("true")
	if r.URL.Query().Get("clear_summary") == "true" {
		now := time.Now().UTC().Format(time.RFC3339)
		setClauses = append(setClauses, `"summary" = ''`)
		setClauses = append(setClauses, `"timestamp" = ?`)
		args = append(args, now)
	}

	if len(setClauses) == 0 {
		http.Error(w, "no fields to update", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("UPDATE %s SET %s WHERE \"id\" = ?", TableName, strings.Join(setClauses, ", "))
	args = append(args, id)

	_, err = a.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		log.Printf("ERROR: update link %d: %v", id, err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}

	// title/summary changed → vector must follow (hash mode re-embeds, API mode clears)
	if r.URL.Query().Get("title") != "" || r.URL.Query().Get("clear_summary") == "true" {
		a.afterTextChange(id)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// ─── Delete Link – DELETE /api/links?id=X ────────────────────────────────────

func (a *App) deleteLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "missing or invalid id", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE \"id\" = ?", TableName)
	result, err := a.db.ExecContext(r.Context(), query, id)
	if err != nil {
		log.Printf("ERROR: delete link %d: %v", id, err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}

	rows, _ := result.RowsAffected()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"deleted":` + strconv.FormatBool(rows > 0) + `}`))
}

// ─── Categories – GET /api/categories ────────────────────────────────────────

func (a *App) getCategories(w http.ResponseWriter, r *http.Request) {
	q := fmt.Sprintf(`
		SELECT "category", COUNT(*)
		FROM %s
		WHERE "category" IS NOT NULL AND "category" != ''
		GROUP BY "category"
		ORDER BY COUNT(*) DESC`, TableName)
	rows, err := a.db.QueryContext(r.Context(), q)
	if err != nil {
		log.Printf("WARN: category query failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]any{})
		return
	}
	defer rows.Close()

	type CatEntry struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	var cats []CatEntry
	for rows.Next() {
		var e CatEntry
		if err := rows.Scan(&e.Name, &e.Count); err != nil {
			log.Printf("WARN: category scan error: %v", err)
			continue
		}
		cats = append(cats, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cats)
}

// ─── Share – GET/POST /api/share ─────────────────────────────────────────────

func (a *App) share(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		l, err := a.fetchLinkByID(id, true)
		if err == sql.ErrNoRows {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "query error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(l)

	case http.MethodPost:
		var req ShareRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		var result []Link
		for _, id := range req.IDs {
			l, err := a.fetchLinkByID(id, true)
			if err != nil {
				continue
			}
			result = append(result, *l)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ─── Language API – GET/POST /api/language ───────────────────────────────────

func (a *App) getLanguage(w http.ResponseWriter, r *http.Request) {
	lang := a.cfg.UILanguage
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"language":"` + lang + `"}`))
}

func (a *App) setLanguage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	lang := req.Language
	if lang != "en" {
		lang = "de"
	}

	// Update in-memory config
	a.cfg.UILanguage = lang

	// Persist to .env
	if data, err := os.ReadFile(".env"); err == nil {
		lines := strings.Split(string(data), "\n")
		found := false
		for i, line := range lines {
			if strings.HasPrefix(line, "UI_LANGUAGE=") {
				lines[i] = "UI_LANGUAGE=" + lang
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, "UI_LANGUAGE="+lang)
		}
		_ = os.WriteFile(".env", []byte(strings.Join(lines, "\n")), 0644)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true,"language":"` + lang + `"}`))
}

// ─── Receive Link – POST /linkshare ──────────────────────────────────────────

func (a *App) receiveLink(w http.ResponseWriter, r *http.Request) {
	var req LinkAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	urlStr := strings.TrimSpace(req.URL)
	if urlStr == "" {
		http.Error(w, `"url" is required`, http.StatusBadRequest)
		return
	}

	log.Printf("[linkshare] received URL: %s", urlStr)

	// YouTube playlist: check first, no crawling needed
	if isYouTubePlaylist(urlStr) {
		a.handlePlaylistSubmission(w, urlStr, req.Note)
		return
	}

	// YouTube URL sanitizer: canonical URL
	if isYouTubeURL(urlStr) {
		sanitized, err := sanitizeYouTubeURL(urlStr, a.cfg.YTDLPPath)
		if err != nil {
			log.Printf("[linkshare] YouTube sanitize warning: %v", err)
		} else if sanitized != "" {
			urlStr = sanitized
		}
	}

	// Duplicate check
	existingID, err := a.findDuplicateURL(urlStr)
	if err != nil {
		log.Printf("[linkshare] duplicate check error: %v", err)
	}
	if existingID > 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(LinkAddResponse{
			ID:      existingID,
			Status:  "duplicate",
			Message: fmt.Sprintf("Link %d already exists.", existingID),
		})
		return
	}

	// Fetch content
	var title string
	var content string

	if isYouTubeURL(urlStr) {
		title = truncateTitleMax(a.fetchYouTubeTitle(urlStr), 150)
		if title == "" {
			crawledTitle, err := extractYouTubePageTitle(urlStr)
			if err == nil && crawledTitle != "" {
				title = truncateTitleMax(crawledTitle, 150)
			}
		}
		content = "" // content comes later from transcription
		log.Printf("[linkshare] YouTube URL detected, title=%q", title)
	} else {
		crawled, err := a.crawlPage(urlStr)
		if err != nil {
			log.Printf("[linkshare] crawl error: %v", err)
			title = ""
		} else {
			title = truncateTitleMax(extractTitleFromMarkdown(crawled), 150)
			content = crawled[:minLen(len(crawled), int(float64(a.cfg.SummaryMaxTokens)*3.5))]
		}
	}

	// Insert into DB
	id, err := a.insertLink(urlStr, title, content, req.Note)
	if err != nil {
		log.Printf("[linkshare] DB insert error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Auto-summary in background (web pages only)
	if a.cfg.AutoSummarize && a.cfg.OpenAIKey != "" && !isYouTubeURL(urlStr) && strings.TrimSpace(content) != "" {
		go func(linkID int64, c string) {
			a.llmMu.Lock()
			defer a.llmMu.Unlock()
			lang := a.cfg.UILanguage
			promptName := resolvePrompt(req.Note)
			summary, err := a.generateSummary(c, promptName, lang)
			if err != nil {
				if strings.Contains(err.Error(), ErrTooLong) {
					updateQ := fmt.Sprintf("UPDATE %s SET \"summary\" = ? WHERE \"id\" = ?", TableName)
					_, _ = a.db.Exec(updateQ, "zu langer Text", linkID)
					log.Printf("[auto-summary] id=%d content too long", linkID)
					return
				}
				log.Printf("[auto-summary] id=%d error: %v", linkID, err)
				return
			}
			updateQ := fmt.Sprintf("UPDATE %s SET \"summary\" = ? WHERE \"id\" = ?", TableName)
			if _, err := a.db.Exec(updateQ, summary, linkID); err != nil {
				log.Printf("[auto-summary] id=%d update error: %v", linkID, err)
			} else {
				log.Printf("[auto-summary] id=%d summarized (%d chars)", linkID, len(summary))
				a.afterTextChange(linkID)
			}
		}(id, content)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LinkAddResponse{
		ID:      id,
		Status:  "ok",
		Message: fmt.Sprintf("Link %d saved – waiting for processing.", id),
	})
}

// handlePlaylistSubmission processes a YouTube playlist submission.
func (a *App) handlePlaylistSubmission(w http.ResponseWriter, playlistURL, note string) {
	videos, err := extractPlaylistVideos(playlistURL, a.cfg.YTDLPPath)
	if err != nil {
		log.Printf("[linkshare] playlist extraction error: %v", err)
		http.Error(w, "playlist extraction failed", http.StatusBadRequest)
		return
	}
	if len(videos) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(LinkAddResponse{
			Status:  "error",
			Message: "no videos found in playlist",
		})
		return
	}

	var ids []int64
	for _, videoURL := range videos {
		// Duplikatprüfung pro Video
		existingID, _ := a.findDuplicateURL(videoURL)
		if existingID > 0 {
			ids = append(ids, existingID)
			continue
		}

		videoTitle := truncateTitleMax(a.fetchYouTubeTitle(videoURL), 150)
		if videoTitle == "" {
			crawledTitle, err := extractYouTubePageTitle(videoURL)
			if err == nil && crawledTitle != "" {
				videoTitle = truncateTitleMax(crawledTitle, 150)
			}
		}
		id, err := a.insertLink(videoURL, videoTitle, "", note)
		if err != nil {
			log.Printf("[linkshare] playlist insert error for %s: %v", videoURL, err)
			continue
		}
		ids = append(ids, id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LinkAddResponse{
		ID:      0,
		Status:  "ok",
		Message: fmt.Sprintf("%d videos added from playlist.", len(ids)),
	})
}

// ─── Process Entries – GET /process-entries ──────────────────────────────────
// Two-phase approach:
//   Phase 1: YouTube videos → yt-dlp + Whisper → transcript in "content" column
//   Phase 2: ALL entries → AI summary from "content" column

func (a *App) processEntries(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if a.cfg.OpenAIKey == "" {
		http.Error(w, "OPENAI_API_KEY not configured", http.StatusBadGateway)
		return
	}

	// LLM-Jobs laufen immer nacheinander (lokale Modellserver können meist nur
	// ein Modell geladen halten – parallele Jobs würden ständig swappen).
	a.llmMu.Lock()
	defer a.llmMu.Unlock()

	// Load entries without summary within the configured time window
	maxDays := a.cfg.MaxProcessDays
	if maxDays <= 0 {
		maxDays = 14
	}
	cutoff := startTime.AddDate(0, 0, -maxDays)

	query := fmt.Sprintf(`
		SELECT "id", "timestamp", "url", "title", "summary", "content", "category", "note", "included", "marked", "read"
		FROM %s
		WHERE ("summary" IS NULL OR "summary" = '')
		AND "timestamp" >= ?
		ORDER BY "timestamp" DESC`, TableName)
	rows, err := a.db.Query(query, cutoff)
	if err != nil {
		log.Printf("[process] query error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	var pending []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err == nil {
			pending = append(pending, *l)
		}
	}
	rows.Close()

	log.Printf("[process] %d pending entries loaded (last %d days)", len(pending), maxDays)

	a.progress.mu.Lock()
	a.progress.Status = "running"
	a.progress.Phase = 1
	a.progress.Message = fmt.Sprintf("Processing %d entries…", len(pending))
	a.progress.Total = len(pending)
	a.progress.Current = 0
	a.progress.mu.Unlock()

	phase1Errs := a.processPhase1Transcribe(pending, &a.progress)

	// After phase 1: reload fresh data (transcripts now in "content")
	pending, err = a.reloadPendingLinks(pending)
	if err != nil {
		log.Printf("[process] reload error after phase 1: %v", err)
	}

	phase2Stats := a.processPhase2Summarize(pending, &a.progress)

	allErrors := append(phase1Errs, phase2Stats.Errors...)

	// ── Auto-categorization ─────────────────────────────────────────────────
	// Newly summarized entries are categorized right away so the user no
	// longer has to trigger it manually.
	categorized := 0
	if phase2Stats.Processed > 0 && a.cfg.OpenAIKey != "" {
		a.progress.mu.Lock()
		a.progress.Phase = 2
		a.progress.Message = "Auto-Kategorisierung…"
		a.progress.mu.Unlock()
		log.Printf("[process] auto-categorizing after %d new summaries", phase2Stats.Processed)
		uncategorized, err := a.loadUncategorizedLinks()
		if err != nil {
			log.Printf("[process] auto-categorize query error: %v", err)
			allErrors = append(allErrors, fmt.Sprintf("[auto-categorize] query: %v", err))
		} else if len(uncategorized) > 0 {
			catStats := a.categorizeLinks(uncategorized)
			categorized = catStats.Processed
			allErrors = append(allErrors, catStats.Errors...)
		}
	}

	duration := time.Since(startTime).Round(time.Millisecond)

	a.progress.mu.Lock()
	a.progress.Status = "done"
	a.progress.Phase = 0
	msg := fmt.Sprintf("Done: %d created, %d skipped, %d errors", phase2Stats.Processed, phase2Stats.Skipped, len(allErrors))
	if categorized > 0 {
		msg += fmt.Sprintf(", %d categorized", categorized)
	}
	a.progress.Message = msg
	a.progress.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProcessResponse{
		Processed:   phase2Stats.Processed,
		Skipped:     phase2Stats.Skipped,
		Errors:      allErrors,
		Duration:    duration.String(),
		Categorized: categorized,
	})
}

// getProgress returns the current processing progress.
func (a *App) getProgress(w http.ResponseWriter, r *http.Request) {
	a.progress.mu.Lock()
	defer a.progress.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a.progress)
}

// ── Phase 1: YouTube Transcription ──────────────────────────────────────────

func (a *App) processPhase1Transcribe(pending []Link, prog *ProcessingState) []string {
	var youtubeLinks []Link
	for _, l := range pending {
		if isYouTubeURL(l.URL) {
			youtubeLinks = append(youtubeLinks, l)
		}
	}

	if len(youtubeLinks) == 0 {
		log.Println("[phase1] No YouTube videos to transcribe")
		prog.mu.Lock()
		prog.Phase = 2
		prog.Message = "Phase 2: Creating summaries…"
		prog.Current = 0
		prog.Total = len(pending)
		prog.mu.Unlock()
		return nil
	}

	log.Printf("[phase1] Transcribing %d YouTube video(s)…", len(youtubeLinks))
	var errors []string

	for i, link := range youtubeLinks {
		prog.mu.Lock()
		prog.Phase = 1
		prog.Current = i + 1
		prog.Total = len(youtubeLinks)
		prog.Message = fmt.Sprintf("Phase 1: Transcription %d/%d – %s", i+1, len(youtubeLinks), truncateUrl(link.URL))
		prog.mu.Unlock()

		if strings.TrimSpace(link.Content) != "" {
			log.Printf("[phase1] id=%d already has content (%d chars), skipping", link.ID, len(link.Content))
			continue
		}

		log.Printf("[phase1] [%d/%d] id=%d url=%s", i+1, len(youtubeLinks), link.ID, link.URL)

		transcript, err := a.resolveYouTubeTranscript(link.URL)
		if err != nil {
			msg := fmt.Sprintf("[phase1] id=%d transcribe: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			errors = append(errors, msg)
			continue
		}

		if transcript == "" {
			msg := fmt.Sprintf("[phase1] id=%d empty transcript", link.ID)
			log.Printf("WARN: %s", msg)
			errors = append(errors, msg)
			continue
		}

		if err := a.saveContent(link.ID, transcript); err != nil {
			msg := fmt.Sprintf("[phase1] id=%d save: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			errors = append(errors, msg)
			continue
		}

		log.Printf("[phase1] id=%d transcript saved (%d chars)", link.ID, len(transcript))
	}

	log.Printf("[phase1] Done – %d/%d transcribed, %d errors",
		len(youtubeLinks)-len(errors), len(youtubeLinks), len(errors))
	return errors
}

// resolveYouTubeTranscript determines the transcript for a YouTube video.
func (a *App) resolveYouTubeTranscript(url string) (string, error) {
	method := a.cfg.YouTubeSubtitleMethod
	switch method {
	case "whisper":
		return a.transcribeViaWhisper(url)
	case "yt-dlp":
		subs, err := a.extractYouTubeSubtitles(url)
		if err != nil {
			return "", fmt.Errorf("yt-dlp subtitles: %w", err)
		}
		if subs == "" {
			return "", fmt.Errorf("no subtitles available")
		}
		return subs, nil
	case "auto":
		subs, err := a.extractYouTubeSubtitles(url)
		if err == nil && subs != "" {
			log.Printf("[yt] auto mode: using yt-dlp subtitles (%d chars)", len(subs))
			return subs, nil
		}
		log.Printf("[yt] auto mode: no subtitles, falling back to whisper")
		return a.transcribeViaWhisper(url)
	default:
		return a.transcribeViaWhisper(url)
	}
}

// transcribeViaWhisper downloads audio and transcribes with Whisper.
func (a *App) transcribeViaWhisper(url string) (string, error) {
	audioFile, err := a.downloadYouTubeAudio(url)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer func() {
		if err := os.Remove(audioFile); err != nil {
			log.Printf("[yt] cleanup warning: %v", err)
		}
	}()

	transcript, err := a.transcribeWithWhisper(audioFile)
	if err != nil {
		return "", fmt.Errorf("whisper: %w", err)
	}

	return transcript, nil
}

// saveContent writes text into the "content" column.
func (a *App) saveContent(id int64, content string) error {
	q := fmt.Sprintf("UPDATE %s SET \"content\" = ? WHERE \"id\" = ?", TableName)
	_, err := a.db.Exec(q, content, id)
	return err
}

// ── Phase 2: AI Summary ────────────────────────────────────────────────────

type phase2Stats struct {
	Processed, Skipped int
	Errors             []string
}

func (a *App) processPhase2Summarize(pending []Link, prog *ProcessingState) phase2Stats {
	log.Printf("[phase2] Starting summary generation for %d entries…", len(pending))
	stats := phase2Stats{}
	lang := a.cfg.UILanguage

	prog.mu.Lock()
	prog.Phase = 2
	prog.Current = 0
	prog.Total = len(pending)
	prog.Message = "Phase 2: Creating summaries…"
	prog.mu.Unlock()

	for i, link := range pending {
		prog.mu.Lock()
		prog.Phase = 2
		prog.Current = i + 1
		prog.Total = len(pending)
		prog.Message = fmt.Sprintf("Phase 2: LLM Summary %d/%d – %s", i+1, len(pending), truncateTitle(link.Title))
		prog.mu.Unlock()

		if shouldSkipProcessing(link.Note) {
			log.Printf("[phase2] id=%d skipping (note has skip keyword)", link.ID)
			stats.Skipped++
			continue
		}

		if strings.TrimSpace(link.Summary) != "" {
			log.Printf("[phase2] id=%d already summarized (%d chars), skipping", link.ID, len(link.Summary))
			stats.Skipped++
			continue
		}

		log.Printf("[phase2] [%d/%d] id=%d url=%s", i+1, len(pending), link.ID, link.URL)

		content := a.resolveContent(link)
		if content == "" {
			msg := fmt.Sprintf("[phase2] id=%d: no content available", link.ID)
			log.Printf("WARN: %s", msg)
			stats.Errors = append(stats.Errors, msg)
			continue
		}

		promptName := resolvePrompt(link.Note)
		summary, err := a.generateSummary(content, promptName, lang)
		if err != nil {
			if strings.Contains(err.Error(), ErrTooLong) {
				updateQ := fmt.Sprintf("UPDATE %s SET \"summary\" = ? WHERE \"id\" = ?", TableName)
				if _, dbErr := a.db.Exec(updateQ, "zu langer Text", link.ID); dbErr != nil {
					stats.Errors = append(stats.Errors, fmt.Sprintf("[phase2] id=%d update: %v", link.ID, dbErr))
				} else {
					stats.Processed++
					log.Printf("[phase2] id=%d content too long (> %d tokens), stored placeholder", link.ID, a.cfg.SummaryMaxTokens)
				}
				continue
			}
			msg := fmt.Sprintf("[phase2] id=%d summary: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			stats.Errors = append(stats.Errors, msg)
			continue
		}

		updateQ := fmt.Sprintf("UPDATE %s SET \"summary\" = ? WHERE \"id\" = ?", TableName)
		if _, err := a.db.Exec(updateQ, summary, link.ID); err != nil {
			msg := fmt.Sprintf("[phase2] id=%d update: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			stats.Errors = append(stats.Errors, msg)
		} else {
			stats.Processed++
			log.Printf("[phase2] id=%d summarized (%d chars)", link.ID, len(summary))
			a.afterTextChange(link.ID)
		}
	}

	log.Printf("[phase2] Done – %d summarized, %d skipped, %d errors",
		stats.Processed, stats.Skipped, len(stats.Errors))
	return stats
}

// resolveContent determines the content to summarize for an entry.
func (a *App) resolveContent(link Link) string {
	if link.Content != "" {
		return link.Content
	}

	if !isYouTubeURL(link.URL) {
		crawled, err := a.crawlPage(link.URL)
		if err != nil {
			log.Printf("[phase2] crawl failed for id=%d: %v", link.ID, err)
		} else {
			if err := a.saveContent(link.ID, crawled); err != nil {
				log.Printf("[phase2] save crawl failed for id=%d: %v", link.ID, err)
			}
			return crawled
		}
	}

	log.Printf("[phase2] id=%d no content (YouTube without transcript)", link.ID)
	return ""
}

// ─── Prompt Resolution ──────────────────────────────────────────────────────

func resolvePrompt(note string) string {
	lower := strings.ToLower(note)
	if strings.Contains(lower, "kapitel") || strings.Contains(lower, "chapters") || strings.Contains(lower, "chapter") {
		return "chapters"
	}
	if strings.Contains(lower, "kategorie") || strings.Contains(lower, "categorize") || strings.Contains(lower, "kategorisieren") {
		return "categorize"
	}
	return "summary"
}

func shouldSkipProcessing(note string) bool {
	lower := strings.ToLower(note)
	return strings.Contains(lower, "skip") || strings.Contains(lower, "überspringen")
}

// truncateUrl shortens a URL to max 60 chars with ellipsis.
func truncateUrl(u string) string {
	if len(u) > 60 {
		return u[:57] + "…"
	}
	return u
}

// truncateTitle shortens a title to max 50 chars with ellipsis.
func truncateTitle(t string) string {
	if len(t) > 50 {
		return t[:47] + "…"
	}
	return t
}

// truncateTitleMax truncates to maxRune runes with "…" suffix.
func truncateTitleMax(t string, maxRune int) string {
	runes := []rune(t)
	if len(runes) > maxRune {
		return string(runes[:maxRune-3]) + "…"
	}
	return t
}

// reloadPendingLinks reloads fresh data for the given links from DB.
func (a *App) reloadPendingLinks(pending []Link) ([]Link, error) {
	if len(pending) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(pending))
	for i, l := range pending {
		ids[i] = l.ID
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	q := fmt.Sprintf(`
		SELECT "id", "timestamp", "url", "title", "summary", "content", "category", "note", "included", "marked", "read"
		FROM %s WHERE "id" IN (%s)
		ORDER BY "id" ASC`, TableName, strings.Join(placeholders, ", "))

	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("reload links: %w", err)
	}
	defer rows.Close()

	var result []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err == nil {
			result = append(result, *l)
		}
	}

	log.Printf("[phase1→2] Reloaded %d fresh entries from DB", len(result))
	return result, nil
}

// ─── DB Write Operations ─────────────────────────────────────────────────────

// insertLink adds a new entry to the database and returns its ID.
// The crawled content goes into the "content" column,
// summary stays NULL (filled later by processEntries).
func (a *App) insertLink(url, title, content, note string) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	query := fmt.Sprintf(`
		INSERT INTO %s ("timestamp", "url", "title", "content", "note")
		VALUES (?, ?, ?, ?, ?)`, TableName)
	result, err := a.db.Exec(query, now, url, title, content, note)
	if err != nil {
		return 0, fmt.Errorf("insert link: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get last insert id: %w", err)
	}
	return id, nil
}

// findDuplicateURL checks if a URL already exists in the database.
func (a *App) findDuplicateURL(url string) (int64, error) {
	query := fmt.Sprintf(`SELECT "id" FROM %s WHERE "url" = ? LIMIT 1`, TableName)
	var id int64
	err := a.db.QueryRow(query, url).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// fetchLinkByID loads a single link by its ID.
func (a *App) fetchLinkByID(id int64, wantContent bool) (*Link, error) {
	var l Link
	var cat sql.NullString
	var summary sql.NullString
	var content sql.NullString
	var note sql.NullString
	var ts dbTime
	var inc NullBool
	var markedVal NullBool
	var readVal NullBool

	query := fmt.Sprintf(`
		SELECT "id", "timestamp", "url", "title", "summary", "content", "category", "note", "included", "marked", "read"
		FROM %s WHERE "id" = ?`, TableName)
	err := a.db.QueryRow(query, id).Scan(&l.ID, &ts, &l.URL, &l.Title, &summary, &content, &cat, &note, &inc, &markedVal, &readVal)
	if err != nil {
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
	if inc.Valid && inc.Bool > 0 {
		v := true
		l.Included = &v
	} else if !inc.Valid || inc.Bool == 0 {
		v := false
		l.Included = &v
	}
	if markedVal.Valid && markedVal.Bool > 0 {
		l.Marked = true
	}
	if readVal.Valid && readVal.Bool > 0 {
		l.Read = true
	}

	l.ContentLen = len(l.Content)
	l.ContentLimit = int(float64(a.cfg.SummaryMaxTokens) * 3.5)

	var buf strings.Builder
	if l.Summary != "" {
		_ = mdRenderer.Convert([]byte(l.Summary), &buf)
		l.SummaryHTML = buf.String()
	}
	if !wantContent {
		l.Content = ""
	}
	return &l, nil
}

// ─── Utility ─────────────────────────────────────────────────────────────────

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Categorize – GET /api/categorize ────────────────────────────────────────

func (a *App) categorizeEntries(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if a.cfg.OpenAIKey == "" {
		http.Error(w, "OPENAI_API_KEY not configured", http.StatusBadGateway)
		return
	}

	a.llmMu.Lock()
	defer a.llmMu.Unlock()

	// Select entries that have a summary but no category
	pending, err := a.loadUncategorizedLinks()
	if err != nil {
		log.Printf("[categorize] query error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if len(pending) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ProcessResponse{
			Processed: 0,
			Skipped:   0,
			Duration:  time.Since(startTime).Round(time.Millisecond).String(),
		})
		return
	}

	stats := a.categorizeLinks(pending)
	stats.Duration = time.Since(startTime).Round(time.Millisecond).String()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// loadUncategorizedLinks returns entries that have a summary but no category.
func (a *App) loadUncategorizedLinks() ([]Link, error) {
	query := fmt.Sprintf(`
		SELECT "id", "timestamp", "url", "title", "summary", "content", "category", "note", "included", "marked", "read"
		FROM %s
		WHERE ("category" IS NULL OR "category" = '')
		AND ("summary" IS NOT NULL AND "summary" != '' AND "summary" != 'zu langer Text')
		ORDER BY "id" DESC`, TableName)

	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pending []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err == nil {
			pending = append(pending, *l)
		}
	}
	return pending, rows.Err()
}

// categorizeLinks assigns a category to every entry in pending using the
// configured categorize model. Shared by the manual endpoint and the
// automatic run after the summarize phase.
func (a *App) categorizeLinks(pending []Link) ProcessResponse {
	lang := a.cfg.UILanguage

	log.Printf("[categorize] %d entries without category", len(pending))

	// Build language-appropriate category list
	catConfig := a.cfg.CategoriesEN
	if strings.ToUpper(lang) == "DE" {
		catConfig = a.cfg.CategoriesDE
	}
	catList := strings.Split(catConfig, ",")
	for i := range catList {
		catList[i] = strings.TrimSpace(catList[i])
	}
	catMap := make(map[string]bool)
	for _, c := range catList {
		catMap[c] = true
	}

	var stats ProcessResponse
	stats.Processed = 0
	stats.Skipped = 0

	for _, link := range pending {
		// Use summary for categorization
		text := link.Summary
		if text == "" {
			stats.Skipped++
			continue
		}

		category, err := a.generateSummaryWithModel(text, "categorize", lang, a.cfg.CategorizeModel)
		if err != nil {
			msg := fmt.Sprintf("[categorize] id=%d: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			stats.Errors = append(stats.Errors, msg)
			continue
		}

		// Validate: response must be exactly one of the valid categories
		category = strings.TrimSpace(category)
		if !catMap[category] {
			log.Printf("[categorize] id=%d invalid category %q, using Unsortiert/Uncategorized", link.ID, category)
			// Use the last category as fallback (Unsortiert)
			category = catList[len(catList)-1]
		}

		// Update the category in DB
		updateQ := fmt.Sprintf("UPDATE %s SET \"category\" = ? WHERE \"id\" = ?", TableName)
		if _, err := a.db.Exec(updateQ, category, link.ID); err != nil {
			msg := fmt.Sprintf("[categorize] id=%d update: %v", link.ID, err)
			log.Printf("ERROR: %s", msg)
			stats.Errors = append(stats.Errors, msg)
		} else {
			stats.Processed++
			log.Printf("[categorize] id=%d → %s", link.ID, category)
		}
	}

	log.Printf("[categorize] Done – %d categorized, %d skipped, %d errors",
		stats.Processed, stats.Skipped, len(stats.Errors))
	return stats
}

// ─── RSS Feed – GET /rss ─────────────────────────────────────────────────────

func (a *App) handleRSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := a.cfg.RssItemCount
	if limit <= 0 {
		limit = 30
	}
	// URL-Parameter ?limit=N überschreibt die Konfiguration
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}

	catFilter := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")
	urlLike := r.URL.Query().Get("url_like")
	noteFilter := r.URL.Query().Get("note")
	includeFilter := r.URL.Query().Get("included")
	markedFilter := r.URL.Query().Get("marked")
	readFilter := r.URL.Query().Get("read")

	var whereClauses []string
	var args []any

	if catFilter != "" {
		whereClauses = append(whereClauses, `"category" = ?`)
		args = append(args, catFilter)
	}
	if includeFilter == "true" {
		whereClauses = append(whereClauses, `"included" = 1`)
	}
	if markedFilter == "true" {
		whereClauses = append(whereClauses, `"marked" = 1`)
	}
	if readFilter == "true" {
		whereClauses = append(whereClauses, `"read" = 1`)
	} else if readFilter == "false" {
		whereClauses = append(whereClauses, `("read" = 0 OR "read" IS NULL)`)
	}
	if q != "" {
		pattern := "%" + q + "%"
		whereClauses = append(whereClauses,
			`("title" LIKE ? OR "url" LIKE ? OR "summary" LIKE ? OR "category" LIKE ?)`,
		)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if urlLike != "" {
		pattern := "%" + urlLike + "%"
		whereClauses = append(whereClauses, `"url" LIKE ?`)
		args = append(args, pattern)
	}
	if noteFilter != "" {
		pattern := "%" + noteFilter + "%"
		whereClauses = append(whereClauses, `"note" LIKE ?`)
		args = append(args, pattern)
	}

	// Nur Einträge mit Zusammenfassung in den RSS-Feed
	whereClauses = append(whereClauses, `"summary" IS NOT NULL AND "summary" != ''`)

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`SELECT "id", "timestamp", "url", "title", "summary"
	FROM %s%s ORDER BY "id" DESC LIMIT ?`, TableName, whereSQL)
	dataArgs := append(append([]any{}, args...), limit)

	rows, err := a.db.QueryContext(r.Context(), query, dataArgs...)
	if err != nil {
		log.Printf("ERROR: RSS query failed: %v", err)
		http.Error(w, "query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type rssEntry struct {
		ID      int64
		Title   string
		URL     string
		Summary string
		Ts      dbTime
	}
	var entries []rssEntry
	for rows.Next() {
		var e rssEntry
		var ts dbTime
		var summary sql.NullString
		if err := rows.Scan(&e.ID, &ts, &e.URL, &e.Title, &summary); err != nil {
			continue
		}
		e.Ts = ts
		if summary.Valid {
			var htmlBuf strings.Builder
			_ = mdRenderer.Convert([]byte(summary.String), &htmlBuf)
			e.Summary = htmlBuf.String()
		}
		entries = append(entries, e)
	}

	base := a.cfg.RssBaseURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Extra Action-Links parsen: [Name](url),[Name](url)
	type extraLink struct {
		Name string
		URL  string
	}
	var extraLinks []extraLink
	if a.cfg.RssExtraActionLink != "" {
		log.Printf("[rss] RssExtraActionLink=%q", a.cfg.RssExtraActionLink)
		parts := strings.Split(a.cfg.RssExtraActionLink, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if len(p) < 4 || p[0] != '[' {
				continue
			}
			closeB := strings.IndexByte(p, ']')
			if closeB < 1 || closeB+1 >= len(p) || p[closeB+1] != '(' {
				continue
			}
			closeP := strings.LastIndexByte(p, ')')
			if closeP <= closeB+1 {
				continue
			}
			extraLinks = append(extraLinks, extraLink{
				Name: p[1:closeB],
				URL:  p[closeB+2 : closeP],
			})
		}
		log.Printf("[rss] %d extra action links parsed", len(extraLinks))
	}

	buf := &strings.Builder{}
	xmlHeader := `<?xml version="1.0" encoding="UTF-8"?>`
	buf.WriteString(xmlHeader + "\n")
	buf.WriteString("<feed xmlns=\"http://www.w3.org/2005/Atom\">\n")
	buf.WriteString("  <title>ei23-FoundAItion</title>\n")
	buf.WriteString("  <subtitle>ei23-FoundAItion</subtitle>\n")
	if base != "" {
		buf.WriteString(fmt.Sprintf("  <link href=\"%s\" rel=\"alternate\" type=\"text/html\"/>\n", escXmlAttr(base)))
		buf.WriteString(fmt.Sprintf("  <link href=\"%s/rss\" rel=\"self\" type=\"application/atom+xml\"/>\n", escXmlAttr(base)))
	}
	buf.WriteString(fmt.Sprintf("  <updated>%s</updated>\n", escXml(now)))

	for _, e := range entries {
		// Use current time as updated when a summary exists (could have been
		// regenerated at any time). Otherwise use the original creation timestamp.
		tsStr := now
		if e.Summary == "" {
			tsStr = e.Ts.Format(time.RFC3339)
			if tsStr == "" {
				tsStr = now
			}
		}

		buf.WriteString("  <entry>\n")
		buf.WriteString(fmt.Sprintf("    <title>%s</title>\n", escXml(e.Title)))
		yearStr := e.Ts.Format("2006")
		buf.WriteString(fmt.Sprintf("    <id>tag:ei23,%s:link=%d</id>\n", yearStr, e.ID))
		if e.URL != "" {
			buf.WriteString(fmt.Sprintf("    <link href=\"%s\"/>\n", escXmlAttr(e.URL)))
		} else if base != "" {
			buf.WriteString(fmt.Sprintf("    <link href=\"%s/api/links?id=%d\"/>\n", escXmlAttr(base), e.ID))
		}
		buf.WriteString(fmt.Sprintf("    <updated>%s</updated>\n", escXml(tsStr)))

		buf.WriteString("    <content type=\"html\">\n")
		buf.WriteString("      <![CDATA[")

		// Thumbnail (ganz oben)
		ytID := extractYtVideoId(e.URL)
		if ytID != "" {
			buf.WriteString(fmt.Sprintf("<img src=\"https://i.ytimg.com/vi/%s/hqdefault.jpg\" alt=\"Thumbnail\"/><br/>", escXmlAttr(ytID)))
		}

		// Summary / article body
		if e.Summary != "" {
			buf.WriteString("<article>")
			buf.WriteString(e.Summary)
			buf.WriteString("</article>")
		}

		// Extra Action-Links (ganz unten)
		if len(extraLinks) > 0 {
			buf.WriteString("<br/><p><small>")
			for i, el := range extraLinks {
				if i > 0 {
					buf.WriteString(" &middot; ")
				}
				url := strings.ReplaceAll(el.URL, "{id}", strconv.FormatInt(e.ID, 10))
				buf.WriteString(fmt.Sprintf("<a href=\"%s\">%s</a>", escXmlAttr(url), escXml(el.Name)))
			}
			buf.WriteString("</small></p>")
		}



		buf.WriteString("]]>\n")
		buf.WriteString("    </content>\n")

		buf.WriteString("  </entry>\n")
	}

	buf.WriteString("</feed>")

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(buf.String()))
}

// ─── Feed API – Action Endpoints ────────────────────────────────────────────

func (a *App) handleFeedAPI(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "missing or invalid id", http.StatusBadRequest)
		return
	}

	path := r.URL.Path
	var query string
	switch {
	case strings.HasSuffix(path, "/mark"):
		query = fmt.Sprintf(`UPDATE %s SET "marked" = 1 WHERE "id" = ?`, TableName)
	case strings.HasSuffix(path, "/include"):
		query = fmt.Sprintf(`UPDATE %s SET "included" = 1 WHERE "id" = ?`, TableName)
	case strings.HasSuffix(path, "/read"):
		query = fmt.Sprintf(`UPDATE %s SET "read" = 1 WHERE "id" = ?`, TableName)
	case strings.HasSuffix(path, "/unread"):
		query = fmt.Sprintf(`UPDATE %s SET "read" = 0 WHERE "id" = ?`, TableName)
	case strings.HasSuffix(path, "/delete-summary"):
		now := time.Now().UTC().Format(time.RFC3339)
		query := fmt.Sprintf(`UPDATE %s SET "summary" = '', "content" = '', "timestamp" = ? WHERE "id" = ?`, TableName)
		res, err := a.db.ExecContext(r.Context(), query, now, id)
		if err != nil {
			log.Printf("ERROR: feed API path=%s id=%d: %v", path, id, err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		a.afterTextChange(id) // summary cleared → vector stale
		rows, _ := res.RowsAffected()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":       true,
			"path":     path,
			"id":       id,
			"affected": rows,
		})
		return
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	result, err := a.db.ExecContext(r.Context(), query, id)
	if err != nil {
		log.Printf("ERROR: feed API path=%s id=%d: %v", path, id, err)
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}

	rows, _ := result.RowsAffected()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"path":     path,
		"id":       id,
		"affected": rows,
	})
}

// ─── XML Escape Helpers ──────────────────────────────────────────────────────

func escXml(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

func escXmlAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(s)
}

// extractYtVideoId extracts the YouTube video ID from a URL.
func extractYtVideoId(url string) string {
	if idx := strings.Index(url, "youtube.com/watch?v="); idx >= 0 {
		sub := url[idx+20:]
		if amp := strings.Index(sub, "&"); amp >= 0 {
			sub = sub[:amp]
		}
		return strings.TrimSpace(sub)
	}
	if idx := strings.Index(url, "youtu.be/"); idx >= 0 {
		sub := url[idx+9:]
		if q := strings.Index(sub, "?"); q >= 0 {
			sub = sub[:q]
		}
		return strings.TrimSpace(sub)
	}
	return ""
}
