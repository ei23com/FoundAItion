package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed manual_DE.md
var manualDE string

//go:embed manual_EN.md
var manualEN string

// mdRenderer is the markdown rendering engine with GFM extensions.
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// pageSizeDB is the default page size for paginated DB queries.
const pageSizeDB = 20

func main() {
	cfg := loadConfig()
	cfg.resolveEmbeddingDefaults()

	listenPort := 8080
	if p, err := strconv.Atoi(envOrDefault("LISTEN_PORT", "")); err == nil && p > 0 {
		listenPort = p
	}

	// ─── Database Connection ──────────────────────────
	db, err := openDatabase(cfg)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer db.Close()

	// Ensure the table exists
	if err := ensureTableExists(db, TableName); err != nil {
		log.Fatalf("Table missing or unreachable: %v", err)
	}

	// Migrate schema for the vectormap feature (idempotent)
	if err := ensureVectorColumns(db, TableName); err != nil {
		log.Fatalf("Vectormap migration failed: %v", err)
	}
	if err := ensureMapPositionsTable(db); err != nil {
		log.Fatalf("Vectormap position cache migration failed: %v", err)
	}
	if err := ensureMapRegionsTable(db); err != nil {
		log.Fatalf("Vectormap regions migration failed: %v", err)
	}

	// Startup diagnostics
	logStartupDiagnostics(cfg)

	// Auto-install yt-dlp if missing
	if needsYtDlpInstall(getBinaryDir()) {
		log.Println("yt-dlp not found – downloading...")
		if err := installYtDlp(getBinaryDir()); err != nil {
			log.Printf("WARN: auto-install yt-dlp failed: %v", err)
		} else {
			log.Println("yt-dlp installed successfully")
		}
	}

	app := newApp(db, cfg)
	server := setupHTTPServer(app, listenPort, cfg)

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}

// openDatabase opens the SQLite database connection.
func openDatabase(cfg Config) (*sql.DB, error) {
	return openSQLite(cfg.DBFile)
}

// logStartupDiagnostics prints configuration info at startup.
func logStartupDiagnostics(cfg Config) {
	if cfg.OpenAIKey != "" {
		log.Printf("Summarizer: OpenAI (%s) configured", cfg.OpenAIModel)
	} else {
		log.Println("WARN: OPENAI_API_KEY not set – summarizer disabled")
	}
	if _, err := exec.LookPath(cfg.YTDLPPath); err != nil {
		log.Printf("WARN: yt-dlp not found at %s – YouTube processing disabled", cfg.YTDLPPath)
	}
	log.Printf("YouTube subtitle method: %s", cfg.YouTubeSubtitleMethod)
	if cfg.WhisperURL != "" {
		log.Printf("Whisper Server: %s (%s)", cfg.WhisperURL, cfg.WhisperModel)
	}
	if model := cfg.ActiveEmbeddingModel(); model == LocalHashModel {
		log.Println("Vectormap: no embedding model configured – using local hash-embedder (offline fallback)")
	} else {
		log.Printf("Vectormap: embeddings via %s (%s)", cfg.EmbeddingBaseURL, model)
	}
}

// setupHTTPServer configures and starts the HTTP server.
func setupHTTPServer(app *App, listenPort int, cfg Config) *http.Server {
	mux := http.NewServeMux()

	// Static files (templates)
	fileServer := http.FileServer(http.FS(templateFS))
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// API endpoints
	mux.HandleFunc("/api/links", app.handleAPI)
	mux.HandleFunc("/api/categories", app.handleAPI)
	mux.HandleFunc("/api/share", app.handleAPI)
	mux.HandleFunc("/linkshare", app.handleAPI)
	mux.HandleFunc("/process-entries", app.handleAPI)
	mux.HandleFunc("/api/manual", app.handleAPI)
	mux.HandleFunc("/api/config", app.handleAPI)
	mux.HandleFunc("/api/language", app.handleAPI)
	mux.HandleFunc("/api/categorize", app.handleAPI)
	mux.HandleFunc("/api/map", app.handleAPI)
	mux.HandleFunc("/api/map/embeddings", app.handleAPI)
	mux.HandleFunc("/api/map/status", app.handleAPI)
	mux.HandleFunc("/api/map/query", app.handleAPI)
	mux.HandleFunc("/api/map/regions", app.handleAPI)
	mux.HandleFunc("/api/update-ytdlp", app.handleUpdateYtDlp)
	mux.HandleFunc("/rss", app.handleRSS)
	mux.HandleFunc("/api/feed/mark", app.handleFeedAPI)
	mux.HandleFunc("/api/feed/include", app.handleFeedAPI)
	mux.HandleFunc("/api/feed/read", app.handleFeedAPI)
	mux.HandleFunc("/api/feed/unread", app.handleFeedAPI)
	mux.HandleFunc("/api/feed/delete-summary", app.handleFeedAPI)

	// Main page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, templateFS, "templates/index.html")
	})

	addr := fmt.Sprintf(":%d", listenPort)

	log.Printf("DB: SQLite → %s", cfg.DBFile)
	log.Printf("Starting ei23-FoundAItion on http://localhost%s", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 300 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	return server
}
