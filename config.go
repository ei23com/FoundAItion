package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ─── Application Configuration ───────────────────────────────────────────────

// Config holds all configurable application parameters loaded from env / .env.
type Config struct {
	OpenAIKey             string // API key for OpenAI (or compatible API)
	OpenAIModel           string // Currently selected model
	OpenAIBaseURL         string // Base URL of the OpenAI-compatible API
	AvailableModels       string // Comma-separated list of available models
	WhisperURL            string // URL of the local Whisper server
	WhisperModel          string // Whisper model (e.g. "large-v3")
	WhisperAPIKey         string // Optional API key for Whisper auth
	CrawlTimeoutSec       int    // Timeout per crawl request in seconds
	YTDLPPath             string // Path to yt-dlp binary
	YTDLAOutputDir        string // Output directory for downloaded audio files
	YouTubeSubtitleMethod string // "whisper", "yt-dlp", "auto"
	MaxProcessDays        int    // Max days backwards for processing (default: 14)
	SummaryMaxTokens      int    // Maximum tokens for summary (default: 64000)
	AutoSummarize         bool   // Auto-generate summary on add (default: true)
	DBFile                string // SQLite file path (default: ./foundaition.db)
	RssBaseURL            string // Base URL for RSS feed action links
	RssItemCount          int    // Max entries in RSS feed (default: 30)
	RssExtraActionLink    string // Extra action link template (e.g. "http://…/publish?id={id}")
	UILanguage            string // UI language: "de" or "en" (default: "de")
	CategoriesDE          string // German category list for auto-categorization
	CategoriesEN          string // English category list for auto-categorization
	CategorizeModel       string // Separate model for categorization (optional, defaults to OpenAIModel)

	// Vectormap / embeddings
	EmbeddingBaseURL string // Base URL of the OpenAI-compatible /embeddings endpoint (default: OPENAI_BASE_URL)
	EmbeddingModel   string // Embedding model name. Empty → local hash-embedder (offline fallback)
	EmbeddingAPIKey  string // API key for the embedding endpoint (default: OPENAI_API_KEY)
}

// LocalHashModel is the pseudo-model identifier used when no embedding API
// is configured (deterministic feature-hashing embedder).
const LocalHashModel = "local-hash"

// TableName is the database table name, hardcoded to "links".
const TableName = `"links"`

// loadConfig reads the full application configuration from environment variables.
// Missing values use sensible defaults.
func loadConfig() Config {
	maxTokens := envIntOrDefault("SUMMARY_MAX_TOKENS", 64000)
	if maxTokens < 1 {
		maxTokens = 1
	}

	autoSummarizeStr := envOrDefault("AUTO_SUMMARIZE", "true")
	autoSummarize := autoSummarizeStr != "false" && autoSummarizeStr != "0" && autoSummarizeStr != "no"

	lang := envOrDefault("UI_LANGUAGE", "de")
	if lang != "en" {
		lang = "de"
	}

	return Config{
		OpenAIKey:             envOrDefault("OPENAI_API_KEY", ""),
		OpenAIModel:           envOrDefault("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIBaseURL:         envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		AvailableModels:       envOrDefault("OPENAI_MODELS", "gpt-4o-mini,gpt-4o"),
		WhisperURL:            envOrDefault("WHISPER_URL", "http://localhost:8081"),
		WhisperModel:          envOrDefault("WHISPER_MODEL", "large-v3"),
		WhisperAPIKey:         envOrDefault("WHISPER_API_KEY", ""),
		CrawlTimeoutSec:       envIntOrDefault("CRAWL_TIMEOUT", 60),
		YTDLPPath:             envOrDefault("YTDLP_PATH", getYTDLPPath()),
		YTDLAOutputDir:        envOrDefault("YTDLA_OUTPUT_DIR", filepath.Join(getBinaryDir(), "audio_tmp")),
		YouTubeSubtitleMethod: envOrDefault("YOUTUBE_SUBTITLE_METHOD", "auto"),
		MaxProcessDays:        envIntOrDefault("MAX_PROCESS_DAYS", 14),
		SummaryMaxTokens:      maxTokens,
		AutoSummarize:         autoSummarize,
		DBFile:                envOrDefault("DB_FILE", "./foundaition.db"),
		RssBaseURL:            envOrDefault("RSS_BASE_URL", ""),
		RssItemCount:          envIntOrDefault("RSS_ITEM_COUNT", 30),
		RssExtraActionLink:    envOrDefault("RSS_EXTRA_ACTION_LINK", ""),
		UILanguage:            lang,
		CategoriesDE:          envOrDefault("CATEGORIES_DE", "Künstliche Intelligenz,Social Media Marketing,Persönlichkeitsentwicklung & Produktivität,Philosophie & Psychologie,Politik,Wissenschaft,Wirtschaft & Finanzen,Technologie & Software,Unsortiert"),
		CategoriesEN:          envOrDefault("CATEGORIES_EN", "Artificial Intelligence,Social Media Marketing,Personal Development & Productivity,Philosophy & Psychology,Politics,Science,Economy & Finance,Technology & Software,Uncategorized"),
		CategorizeModel:       envOrDefault("CATEGORIZE_MODEL", ""),

		EmbeddingBaseURL: envOrDefault("EMBEDDING_BASE_URL", ""), // "" → fall back to OPENAI_BASE_URL below
		EmbeddingModel:   envOrDefault("EMBEDDING_MODEL", ""),
		EmbeddingAPIKey:  envOrDefault("EMBEDDING_API_KEY", ""), // "" → fall back to OPENAI_API_KEY below
	}
}

// resolveEmbeddingDefaults fills the embedding endpoint settings from the
// general OpenAI settings when they are not explicitly configured.
func (c *Config) resolveEmbeddingDefaults() {
	if c.EmbeddingBaseURL == "" {
		c.EmbeddingBaseURL = c.OpenAIBaseURL
	}
	if c.EmbeddingAPIKey == "" {
		c.EmbeddingAPIKey = c.OpenAIKey
	}
}

// ActiveEmbeddingModel returns the model identifier used for all vectors in
// the current map: the configured API model or the local hash fallback.
func (c *Config) ActiveEmbeddingModel() string {
	if c.EmbeddingModel != "" {
		return c.EmbeddingModel
	}
	return LocalHashModel
}

// ─── Environment Variable Helpers ────────────────────────────────────────────

// envOrDefault returns the env var value or the fallback.
// Checks environment first, then .env file.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	data, err := os.ReadFile(".env")
	if err == nil {
		for _, line := range splitLines(string(data)) {
			line = trim(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if idx := strings.Index(line, "="); idx > 0 {
				k := trim(line[:idx])
				v := trim(line[idx+1:])
				last := len(v) - 1
				if len(v) >= 2 && ((v[0] == '"' && v[last] == '"') || (v[0] == byte(39) && v[last] == byte(39))) {
					v = v[1 : len(v)-1]
				}
				if k == key {
					return v
				}
			}
		}
	}
	return fallback
}

// envIntOrDefault returns the env var as int or the fallback.
func envIntOrDefault(key string, fallback int) int {
	s := envOrDefault(key, "")
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return fallback
}

// ─── String Helpers ──────────────────────────────────────────────────────────

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// ─── Token Estimation (Heuristic) ─────────────────────────────────────────────

// charsPerToken heuristic: ~3.5 Unicode runes per token for mixed DE/EN content.
const charsPerToken = 3.5

// estimateTokens estimates the approximate token count for a text.
func estimateTokens(content string) int {
	return int(float64(len([]rune(content))) / charsPerToken)
}

// exceedsMaxTokens checks whether the content exceeds the configured token limit.
func (c Config) exceedsMaxTokens(content string) bool {
	if c.SummaryMaxTokens <= 0 {
		return false
	}
	return estimateTokens(content) > c.SummaryMaxTokens
}

// MaxCharsForCurrentTokenLimit returns the character count equivalent to the
// current token limit (for UI display).
func (c Config) MaxCharsForCurrentTokenLimit() int {
	return int(float64(c.SummaryMaxTokens) * charsPerToken)
}
