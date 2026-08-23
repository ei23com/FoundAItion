package main

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ─── Application Core ────────────────────────────────────────────────────────

// ProcessingState holds current progress data for UI polling.
type ProcessingState struct {
	mu      sync.Mutex
	Phase   int    `json:"phase"`   // 0=idle, 1=transcription, 2=summary
	Status  string `json:"status"`  // "idle" / "running" / "done"
	Message string `json:"message"` // human-readable status text
	Current int    `json:"current"` // current step (1-based)
	Total   int    `json:"total"`   // total number of steps
}

// App holds the full application state.
type App struct {
	db       *sql.DB
	cfg      Config
	prompts  map[string]string // template name → content (summary_EN, summary_DE, chapters_DE, …)
	progress ProcessingState
	mu       sync.Mutex   // protects receiveLink from parallel duplicates
	mapJob   mapJobState // vectormap background job (embedding + projection)
}

// newApp creates a new App instance with the given DB and config.
// Prompt templates are loaded from the prompts/ directory.
func newApp(db *sql.DB, cfg Config) *App {
	prompts := loadPrompts("prompts")
	return &App{
		db:      db,
		cfg:     cfg,
		prompts: prompts,
	}
}

// ─── Prompt Management ───────────────────────────────────────────────────────

// loadPrompts reads all *.md files from the given directory and returns
// a map of template name → content. The filename (without .md) is used as key.
func loadPrompts(dir string) map[string]string {
	prompts := make(map[string]string)

	// Try multiple paths: given dir, binary dir, and CWD
	dirsToTry := []string{
		dir,
		filepath.Join(getBinaryDir(), dir),
	}

	var found bool
	for _, tryDir := range dirsToTry {
		entries, err := os.ReadDir(tryDir)
		if err != nil {
			continue
		}
		found = true
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			key := strings.TrimSuffix(e.Name(), ".md")
			data, err := os.ReadFile(filepath.Join(tryDir, e.Name()))
			if err != nil {
				log.Printf("WARN: cannot read prompt '%s': %v", e.Name(), err)
				continue
			}
			prompts[key] = strings.TrimSpace(string(data))
		}
		if len(prompts) > 0 {
			log.Printf("Loaded %d prompt templates from %s", len(prompts), tryDir)
			for k := range prompts {
				log.Printf("  prompt: %s", k)
			}
			return prompts
		}
	}

	if !found {
		log.Printf("WARN: prompts dir '%s' not found at any path", dir)
	} else {
		log.Printf("WARN: no .md files found in prompts dir")
	}
	return prompts
}

// renderPrompt replaces the {{CONTENT}} placeholder in a named template.
// Falls back to language-aware default if the template is not found.
func (a *App) renderPrompt(name, content, lang string) string {
	// Try language-specific prompt first: summary_DE, summary_EN
	// Filenames use uppercase language codes (DE, EN), config uses lowercase (de, en)
	preferred := name + "_" + strings.ToUpper(lang)
	if t, ok := a.prompts[preferred]; ok {
		log.Printf("[prompt] using %s", preferred)
		result := strings.ReplaceAll(t, "{{CONTENT}}", content)
		// Language-appropriate category list
		cats := a.cfg.CategoriesEN
		if strings.ToUpper(lang) == "DE" {
			cats = a.cfg.CategoriesDE
		}
		result = strings.ReplaceAll(result, "{{CATEGORIES}}", cats)
		return result
	}
	// Fallback: generic name
	if t, ok := a.prompts[name]; ok {
		log.Printf("[prompt] fallback to generic %s", name)
		return strings.ReplaceAll(t, "{{CONTENT}}", content)
	}
	// Final fallback
	log.Printf("WARN: prompt '%s' not found, using raw content", name)
	defaultText := "Please summarize the following content in {{LANG}}.\n\n{{CONTENT}}"
	if lang == "de" {
		defaultText = "Bitte fasse den folgenden Inhalt auf Deutsch zusammen.\n\n{{CONTENT}}"
	}
	return strings.ReplaceAll(defaultText, "{{CONTENT}}", content)
}
