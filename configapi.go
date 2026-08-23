package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// ─── API: Konfiguration lesen/schreiben ──────────────────────────────────────

// envConfigEntry repräsentiert eine einzelne Umgebungsvariable.
type envConfigEntry struct {
	Key         string   `json:"key"`
	Value       string   `json:"value,omitempty"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Section     string   `json:"section"`
	Secret      bool     `json:"secret"`            // bei GET nur "*" zurückgeben wenn gesetzt
	Type        string   `json:"type"`              // "text", "number", "select"
	Options     []string `json:"options,omitempty"` // für "select"
	Required    bool     `json:"required"`
}

// envConfigResponse gibt die aktuelle Konfiguration mit Metadaten zurück.
type envConfigResponse struct {
	EnvFile    string           `json:"envFile"`    // Pfad der .env-Datei
	Exists     bool             `json:"exists"`     //是否存在 .env
	Configured []envConfigEntry `json:"configured"` // Werte aus Umgebungsvariablen + .env
	Schema     []envConfigEntry `json:"schema"`     // alle verfügbaren Einstellungen
}

// configSchema definiert alle konfigurierbaren Optionen.
var configSchema = []envConfigEntry{
	// ── OpenAI ──
	{Key: "OPENAI_API_KEY", Label: "OpenAI API-Key", Section: "openai", Secret: true, Required: true},
	{Key: "OPENAI_BASE_URL", Label: "OpenAI Base URL", Description: "Basis-URL der OpenAI-kompatiblen API", Section: "openai"},
	{Key: "OPENAI_MODELS", Label: "Verfügbare Modelle", Description: "Kommagetrennte Liste (z.B. gpt-4o-mini,gpt-4o,claude-sonnet-4)", Section: "openai"},
	{Key: "OPENAI_MODEL", Label: "Aktuelles Modell", Description: "Ausgewähltes Modell aus der Liste oben", Section: "openai", Type: "select"},
	{Key: "SUMMARY_MAX_TOKENS", Label: "Max. Token (Zusammenfassung)", Description: "Token-Limit für die Zusammenfassung. Standard 64000.", Section: "openai", Type: "number"},
	{Key: "AUTO_SUMMARIZE", Label: "Auto-Zusammenfassung", Description: "Zusammenfassung direkt beim Hinzufügen erstellen (true/false)", Section: "openai", Type: "select", Options: []string{"true", "false"}},

	// ── Whisper ──
	{Key: "WHISPER_URL", Label: "Whisper Server URL", Description: "URL des lokalen Whisper-Servers", Section: "whisper"},
	{Key: "WHISPER_MODEL", Label: "Whisper Modell", Description: "z.B. large-v3", Section: "whisper"},
	{Key: "WHISPER_API_KEY", Label: "Whisper API-Key", Description: "Optional", Section: "whisper", Secret: true},

	// ── YouTube ──
	{Key: "YTDLP_PATH", Label: "yt-dlp Pfad", Description: "Pfad zu yt-dlp_linux (default: ./yt-dlp_linux im App-Verzeichnis)", Section: "youtube"},
	{Key: "YTDLA_OUTPUT_DIR", Label: "Audio-Verzeichnis", Description: "Temporäres Verzeichnis für Audio-Downloads", Section: "youtube"},
	{Key: "YOUTUBE_SUBTITLE_METHOD", Label: "Untertitel-Methode", Description: "Quelle für YouTube-Transkripte", Section: "youtube",
		Type: "select", Options: []string{"auto", "whisper", "yt-dlp"}},

	// ── Database ──
	{Key: "DB_FILE", Label: "SQLite Database File", Description: "Path to SQLite database file (default: ./foundaition.db)", Section: "database", Required: true},
	{Key: "UI_LANGUAGE", Label: "UI Language / Sprache", Description: "Interface language (de = Deutsch, en = English)", Section: "database", Type: "select", Options: []string{"de", "en"}},

	// ── Allgemein ──
	{Key: "CRAWL_TIMEOUT", Label: "Crawl Timeout", Description: "Timeout in Sekunden pro Webseite", Section: "general", Type: "number"},
	{Key: "LISTEN_PORT", Label: "Server-Port", Description: "HTTP-Port der Anwendung", Section: "general", Type: "number"},
	{Key: "MAX_PROCESS_DAYS", Label: "Verarbeitungsfenster (Tage)", Description: "Max. Tage rückwärts ab heute die verarbeitet werden sollen (bestehende Summaries bleiben erhalten, default: 14)", Section: "general", Type: "number"},

	// ── Categorization ──
	{Key: "CATEGORIES_DE", Label: "Categories (DE)", Description: "Deutsche Kategorien – komma-getrennt (wird bei UI-Sprache DE verwendet)", Section: "categorize"},
	{Key: "CATEGORIES_EN", Label: "Categories (EN)", Description: "English categories – comma-separated (used when UI language is EN)", Section: "categorize"},
	{Key: "CATEGORIZE_MODEL", Label: "Categorize Model", Description: "Optional separate model for categorization (e.g. gpt-4o-mini). Leave empty to use the default model.", Section: "categorize"},

	// ── Vectormap ──
	{Key: "EMBEDDING_BASE_URL", Label: "Embedding Base URL", Description: "Basis-URL eines OpenAI-kompatiblen /embeddings-Endpunkts (z.B. llama.cpp-Server mit Embedding-Modell). Leer = wie OPENAI_BASE_URL.", Section: "vectormap"},
	{Key: "EMBEDDING_MODEL", Label: "Embedding Modell", Description: "Modellname für Text-Embeddings (z.B. Qwen3-Embedding-4B-GGUF). Leer = lokaler Offline-Fallback (Hash-Vektoren, geringere Qualität).", Section: "vectormap"},
	{Key: "EMBEDDING_API_KEY", Label: "Embedding API-Key", Description: "API-Key für den Embedding-Endpunkt. Leer = wie OPENAI_API_KEY.", Section: "vectormap", Secret: true},

	// ── RSS Feed ──
	{Key: "RSS_BASE_URL", Label: "RSS Basis-URL", Description: "Basis-URL für Feed-Links (z.B. bei Reverse Proxy). Leer = automatisch aus Request-Host.", Section: "rss"},
	{Key: "RSS_ITEM_COUNT", Label: "RSS Einträge", Description: "Maximale Anzahl Einträge im Feed (default: 30)", Section: "rss", Type: "number"},
	{Key: "RSS_EXTRA_ACTION_LINK", Label: "RSS Extra Action-Links", Description: "Kommagetrennte Liste: [Name](url) – {id} wird durch die ID ersetzt (z.B. [Veröffentlichen](http://10.1.1.11:1880/publish?id={id}),[Gelesen](http://10.1.1.11:1880/read?id={id}))", Section: "rss"},

	// ── Tools ──
	{Key: "UPDATE_YTDlP", Label: "yt-dlp aktualisieren", Description: "yt-dlp_linux + srt_fix Plugin neu herunterladen (überschreibt vorhandene Dateien)", Section: "tools", Type: "button"},
}

// parseEnvFile liest eine .env-Datei und gibt einen Key→Value Map zurück.
func parseEnvFile(path string) map[string]string {
	result := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			// Anführungszeichen entfernen
			if len(v) >= 2 {
				if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
					v = v[1 : len(v)-1]
				}
			}
			result[k] = v
		}
	}
	return result
}

// getConfig gibt den aktuellen Konfigurationszustand zurück.
func (a *App) getConfig(w http.ResponseWriter, r *http.Request) {
	envPath := ".env"
	_, err := os.Stat(envPath)
	exists := err == nil

	// .env-Datei direkt parsen
	envFileValues := parseEnvFile(envPath)

	// Aktuelle Werte: zuerst Umgebungsvariable, dann .env-Datei
	// OPENAI_MODELS Wert ermitteln (für dynamische Select-Options bei OPENAI_MODEL)
	availableModelsVal := os.Getenv("OPENAI_MODELS")
	if availableModelsVal == "" && exists {
		availableModelsVal = envFileValues["OPENAI_MODELS"]
	}
	// Max-Tokens Wert für Info
	maxTokensVal := os.Getenv("SUMMARY_MAX_TOKENS")
	if maxTokensVal == "" && exists {
		maxTokensVal = envFileValues["SUMMARY_MAX_TOKENS"]
	}

	configured := make([]envConfigEntry, len(configSchema))
	for i, entry := range configSchema {
		val := os.Getenv(entry.Key)
		if val == "" && exists {
			val = envFileValues[entry.Key]
		}
		e := entry
		e.Value = val
		if e.Secret && val != "" {
			e.Value = strings.Repeat("*", len(val))
		}
		// OPENAI_MODEL: dynamische Select-Options aus OPENAI_MODELS
		if entry.Key == "OPENAI_MODEL" && availableModelsVal != "" {
			opts := strings.Split(availableModelsVal, ",")
			for j := range opts {
				opts[j] = strings.TrimSpace(opts[j])
			}
			e.Options = opts
		}
		// SUMMARY_MAX_TOKENS: ergänzende Info (Zeichen) als Description-Anhang
		if entry.Key == "SUMMARY_MAX_TOKENS" && maxTokensVal != "" {
			if t, err := strconv.Atoi(maxTokensVal); err == nil && t > 0 {
				maxChars := int(float64(t) * charsPerToken)
				e.Description += fmt.Sprintf(" (ca. %d Zeichen)", maxChars)
			}
		}
		configured[i] = e
	}

	resp := envConfigResponse{
		EnvFile:    envPath,
		Exists:     exists,
		Configured: configured,
		Schema:     configSchema,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// saveConfig schreibt aktualisierte Werte in die .env-Datei.
func (a *App) saveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		EnvFile string            `json:"envFile"` // optional: neuen Pfad setzen
		Values  map[string]string `json:"values"`  // Key→Value Paare
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	envPath := req.EnvFile
	if envPath == "" {
		envPath = ".env"
	}

	// Falls Datei existiert, aktuelle Werte lesen (nicht angegebene Werte beibehalten)
	existing := make(map[string]string)
	if data, err := os.ReadFile(envPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if idx := strings.Index(line, "="); idx > 0 {
				k := strings.TrimSpace(line[:idx])
				v := strings.TrimSpace(line[idx+1:])
				// Anführungszeichen entfernen
				if len(v) >= 2 {
					if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
						v = v[1 : len(v)-1]
					}
				}
				existing[k] = v
			}
		}
	}

	// Neue Werte überschreiben (leere Werte nicht löschen)
	for k, v := range req.Values {
		if v != "" {
			existing[k] = v
		}
	}

	// Datei schreiben mit Header-Kommentar
	var lines []string
	lines = append(lines, "# ei23 Summarizer - Konfiguration")
	lines = append(lines, "# Diese Datei wird von der Web-UI verwaltet\n")

	// Nach Sektionen sortieren (gleiche Reihenfolge wie Schema)
	sectionOrder := map[string]int{"openai": 0, "whisper": 1, "youtube": 2, "database": 3, "general": 4, "vectormap": 5}
	type kv struct {
		k, v string
		sec  int
	}
	var entries []kv
	for _, s := range configSchema {
		if val, ok := existing[s.Key]; ok && val != "" {
			prio := sectionOrder[s.Section]
			if prio == 0 && s.Section != "openai" {
				prio = 99 // unbekannte Sektion hinten
			}
			entries = append(entries, kv{k: s.Key, v: val, sec: prio})
		}
	}

	// Nach Sektion sortieren
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].sec < entries[i].sec {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	lastSec := ""
	for _, e := range entries {
		// Sektions-Header einfügen
		found := false
		for _, s := range configSchema {
			if s.Key == e.k {
				if lastSec != s.Section && lastSec != "" {
					lines = append(lines, "")
				}
				lastSec = s.Section
				secLabel := map[string]string{
					"openai":     "AI / OpenAI",
					"whisper":    "Whisper",
					"youtube":    "YouTube",
					"database":   "Database",
					"general":    "General",
					"categorize": "Categorization",
					"vectormap":  "Vectormap / Embeddings",
					"rss":        "RSS Feed",
					"tools":      "Tools",
				}[s.Section]
				lines = append(lines, "# -- "+secLabel+" --")
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, "# -- Sonstiges --")
			lastSec = "other"
		}

		// Werte mit Anführungszeichen umschließen wenn Leerzeichen enthalten
		v := e.v
		if strings.ContainsAny(v, " =#'\"") {
			v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
		}
		lines = append(lines, e.k+"="+v)
	}

	data := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.WriteFile(envPath, data, 0644); err != nil {
		log.Printf("[config] write error: %v", err)
		http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[config] saved %d entries to %s", len(entries), envPath)

	// In-Memory Config sofort aktualisieren (kein Neustart nötig)
	for k, v := range existing {
		if v != "" {
			a.applyConfigValue(k, v)
		}
	}
	// Embedding-Endpunkt: Fallbacks auf OpenAI-Einstellungen neu auflösen
	a.cfg.resolveEmbeddingDefaults()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"envFile": envPath,
		"note":    "Gespeichert und aktiviert",
	})
}

// applyConfigValue aktualisiert einen einzelnen Config-Wert im laufenden App-Kontext.
func (a *App) applyConfigValue(key, value string) {
	switch key {
	case "RSS_BASE_URL":
		a.cfg.RssBaseURL = value
	case "RSS_ITEM_COUNT":
		if n, err := strconv.Atoi(value); err == nil {
			a.cfg.RssItemCount = n
		}
	case "RSS_EXTRA_ACTION_LINK":
		a.cfg.RssExtraActionLink = value
	case "OPENAI_API_KEY":
		a.cfg.OpenAIKey = value
	case "OPENAI_MODEL":
		a.cfg.OpenAIModel = value
	case "OPENAI_BASE_URL":
		a.cfg.OpenAIBaseURL = value
	case "SUMMARY_MAX_TOKENS":
		if n, err := strconv.Atoi(value); err == nil {
			a.cfg.SummaryMaxTokens = n
		}
	case "DB_FILE":
		a.cfg.DBFile = value
	case "UI_LANGUAGE":
		a.cfg.UILanguage = value
	case "CATEGORIES_DE":
		a.cfg.CategoriesDE = value
	case "CATEGORIES_EN":
		a.cfg.CategoriesEN = value
	case "CATEGORIZE_MODEL":
		a.cfg.CategorizeModel = value
	case "EMBEDDING_BASE_URL":
		a.cfg.EmbeddingBaseURL = value
	case "EMBEDDING_MODEL":
		a.cfg.EmbeddingModel = value
	case "EMBEDDING_API_KEY":
		a.cfg.EmbeddingAPIKey = value
	}
}
