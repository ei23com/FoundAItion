# ei23-FoundAItion – Manual

A small, fast web interface for **managing**, **searching** and **sharing** links with AI summaries (OpenAI). Single binary, dark mode by default, **SQLite** storage (local, no server needed).

---

## Features

- **SQLite database** – Local, no external dependencies
- **Bilingual UI** – German and English, switchable via the UI
- Reads from the `links` table (fixed, not configurable)
- Compact list view with title, date, status dots (Included, Marked, Read)
- Detail view with rendered Markdown (Goldmark GFM: tables, code blocks, lists)
- **Search** with real-time filter across title, URL, summary
- **Category filter** and **Include/Marked/Read toggles**
- **Status dots** in list view: Included (green), Marked (amber), Read (blue)
- **"Read" flag**: Mark entries as read/unread directly in the list
- **Edit notes** in the detail view
- **Delete entries** with confirmation dialog
- YouTube video detection + thumbnail display
- YouTube playlists: All videos added individually
- **AI summary** (OpenAI / compatible API) – automatic on add or via batch
- **RSS/Atom feed** (`/rss`) with action links (Mark, Include, Delete Summary)
- **Whisper integration** for YouTube transcription
- **Filter words** in the note field: `chapters` → chapter overview, `skip` → skip processing

---

## Android: Share Links via HTTP Shortcuts

The free **[HTTP Shortcuts](https://github.com/Waboodoo/HTTP-Shortcuts)** app (highly recommended!) lets you send links directly from the Android share menu to FoundAItion:

1. Install **HTTP Shortcuts** and create a new shortcut
2. Method: `POST` · URL: `http://<your-instance>:8080/linkshare`
3. **Content-Type**: `application/json`
4. **Body** (JSON):
   ```json
   {"url": "{{param:clipboard|url}}", "note": "shared"}
   ```
5. Enable **"Share"** in the shortcut settings so it appears in Android's share dialog
6. Feel free to add more shortcuts – e.g. one for YouTube playlists:
   ```json
   {"url": "{{param:clipboard|url}}", "note": "chapters"}
   ```
   (thanks to the `chapters` filter word, chapter overviews will be created)

Articles, videos, or entire playlists land in FoundAItion with a single tap and get summarized automatically.

---

## Requirements

| What | Why |
|-----|-------|
| **Go ≥ 1.21** | Compile the binary |
| **SQLite** | Database (local, pure Go via modernc.org/sqlite, no CGO) |

### Summarizer (optional)
| What | Why |
|-----|-------|
| **OpenAI API Key** | AI summaries (GPT-4o-mini or compatible) |
| **yt-dlp** | YouTube audio download |
| **Whisper Server** | Local self-hosted Whisper for audio transcription |
| **Colly + html-to-markdown** | Pure Go – web crawling, HTML → Markdown |

> **Note on local models:** if the model server (e.g. llama-swap, Ollama) loads the model only on the
> first request, a single call can take several minutes. The HTTP timeout for LLM calls is therefore
> **at least 240 seconds** (`LLM_TIMEOUT`, see environment variables).

The `links` table is **auto-created** on first run (`CREATE TABLE IF NOT EXISTS`).

### Schema (17 columns)

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `id` | INTEGER AUTOINCREMENT | – | Primary key |
| `timestamp` | TEXT | – | Creation timestamp (ISO-8601) |
| `url` | TEXT | – | The link URL |
| `title` | TEXT | – | Page/video title |
| `note` | TEXT | – | Notes + filter keywords (`chapters`, `skip`) |
| `summary` | TEXT | – | AI-generated summary (Markdown) |
| `content` | TEXT | – | Crawled content / transcript |
| `category` | TEXT | – | Category for filtering |
| `included` | INTEGER | `0` | Include in export/view |
| `marked` | INTEGER | `0` | Marked flag |
| `read` | INTEGER | `0` | Read/Unread flag |
| `vector` | BLOB | NULL | Embedding vector (little-endian float32) used by the topic map |
| `vec_model` | TEXT | NULL | Name of the model that produced the vector – switching models invalidates old vectors automatically |

FoundAItion also maintains the **`map_positions`** table (`id`, `x`, `y`, `model`, `fingerprint`) holding the 2D coordinates of the topic map. The `fingerprint` hashes all vector IDs + model name: when entries change, the map is recomputed on next open, otherwise it loads instantly from cache.

---

## Quick Start

```bash
# Create .env:
cat > .env << 'EOF'
LISTEN_PORT=8080
OPENAI_API_KEY=sk-...
YTDLP_PATH=/usr/local/bin/yt-dlp
EOF

go mod tidy && go build -o foundaition
./foundaition
# SQLite file: ./foundaition.db (auto-created)
```

Open `http://localhost:8080` in your browser. Change port via `LISTEN_PORT=9000`.

---

## Usage

### List view (start page)

- **"+ Add Link"**: Expandable form to insert new URLs
- **"⚙️ Process"**: Batch process all entries without summary
- Numbered list of all links, newest first (paginated: 20/page)
- **YouTube links** show a thumbnail
- Status dots: Included (green), Marked (amber), Read (blue) – clickable
- Category badges on the right
- Toggle filters for Included/Marked/Read (On/Off)
- **Search field**: Real-time search across title, URL, summary

### Detail view (`#link/123`)

- Title + metadata (URL, date, note)
- **Markdown summary** rendered via Goldmark (GFM)
- **Edit area**: Edit note, toggle Included/Marked/Read
- **Actions:**
  - 📋 Copy URL
  - 📝 Copy summary as Markdown
  - ↗️ Open in browser
- 🔃 **Reload content** (in the edit area, next to "Regenerate summary"): re-crawls the web page or reloads the subtitles (English/German). The content is replaced, the **summary stays unchanged** – this fixes older entries whose content was loaded in the wrong language. The title is corrected from the HTML title as well (see "Web scraping")
- 🧠 **Regenerate summary** (replaces the former "Reset summary"): regenerates the summary from the current content, so it is not merely deleted but rewritten. If the content is missing it is fetched first. Should the LLM call fail, the existing summary stays in place
- 🗑️ **Delete entry** (with confirmation dialog)

### Language switching

- In the burger menu (top left) → select **Deutsch** or **English**
- The UI switches all texts immediately
- Prompt templates are loaded in the matching language
- Setting is saved to `.env` (`UI_LANGUAGE=de` / `UI_LANGUAGE=en`)

### Topic map (`#map`, 🗺️ button)

Every entry is positioned on an interactive 2D map by its embedding vector – semantically similar articles sit close together (Barnes-Hut t-SNE implemented in pure Go, deterministic).

**Usage:**
- **Scroll / pinch** = zoom, **drag** = pan
- **Hover** shows title, category and date; **click** opens the article
- **Category legend**: click a chip to hide/show that category
- **Date filters**: "From"/"To" restrict the visible time range
- **Shared search bar at the top** (drives list and map): title substring filter; with the **✨ button** active it searches embedding vectors semantically (top hits render larger, 🎯 zooms to them); date filters "From/To" sit next to it

**Embeddings:**
- Without a configured model, a local hash embedder runs (offline, instant, lexical quality only)
- With `EMBEDDING_BASE_URL` + `EMBEDDING_MODEL` set (e.g. llama.cpp serving a Qwen3-Embedding GGUF), real semantic vectors are produced. The backfill never starts implicitly – the map overlay shows an **"Embed now"** button, or call `POST /api/map/embeddings`
- Progress is shown as an overlay; the map reloads automatically afterwards
- 2D positions are cached in the `map_positions` table → restarts are instant

---

## YouTube subtitles & the `yt-sub` helper

**Language selection:** transcripts are English or German (`YTDLP_SUB_LANGS`, default `en.*,de.*`). English
videos use the English track, German videos the German one – detected from the language spoken in the video.
Within a language, **human** subtitles win over automatic ones (`srt_fix` cleans up automatic rolling
captions). If no matching track exists, no transcript is produced on purpose (never foreign-language
material). Requests stay minimal (`en,en-orig,de,de-orig`); only when that yields nothing is a broader search
issued – this keeps YouTube's rate limit (HTTP 429) happy.

**Manual helper** for when the pipeline finds no subtitles:

```bash
./yt-sub en "https://www.youtube.com/watch?v=XXXXXXXXXXX"   # English
./yt-sub de "https://youtu.be/XXXXXXXXXXX"                  # German
./yt-sub en,de "<url>"                                      # both, priority as above
./yt-sub --list "<url>"                                     # show available languages
```

The script switches into the program directory (so `.env`, `foundaition.db`, `yt-dlp_linux` and the plugin are
found) and calls `./foundaition yt-sub …`. It writes into the target directory (`YT_SUBS_DIR`, default `./subs`):

| File | Content |
|------|---------|
| `<videoID>.<lang>.srt` | downloaded subtitles (or `.vtt` when only VTT exists) |
| `<videoID>.<lang>.txt` | same content in the program format `HH:MM:SS Text` (like the `content` column) |

If the URL exists in the database, the transcript is written straight into `content` – summaries and the topic
map can then use it. Options: `--out DIR`, `--manual` (human subtitles only), `--auto` (automatic only),
`--no-db`, `--timeout SEC`, `-h`.

---

## Web scraping (title & content)

For web entries, FoundAItion determines title and content through a fixed pipeline – with no per-site special cases:

1. **Fetching** the page with a real browser user agent and browser headers. Should the attempt fail (some sites block e.g. the Go stack's HTTP/2 fingerprint with `HTTP 419` while browsers/`curl` get 200), it **automatically retries with HTTP/1.1 forced**.
2. **Title = HTML title** (`<head><title>`, taken 1:1). `<title>` tags inside SVG logos (e.g. "Electrek Logo") are **ignored** – historically the most common cause of wrong titles. Fallbacks: `og:title`, first `H1`.
3. **Content = main article** via **Mozilla Readability** (the exact algorithm behind Firefox's reader view – removes cookie banners, navigation, ads, paywall frames). Fallbacks: `<article>`/`<main>` with boilerplate filtering, finally the cleaned `<body>`. If the Readability result is thin and the cleaned page is much richer (e.g. discussion/forum threads like Hacker News), the cleaned page wins – keeping the thread intact.
4. **No clean content?** Then the content stays **empty** – an error message is **never** stored as content. Enter the content manually in the detail view in that case (title and summary can be edited there as well).

The 🔃 "Reload content" button uses the same pipeline and also corrects wrong legacy titles.

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_PORT` | `8080` | HTTP server port |
| `DB_FILE` | `./foundaition.db` | SQLite database file path |
| `UI_LANGUAGE` | `de` | Interface language (`de` or `en`) |
| `OPENAI_API_KEY` | *(empty)* | OpenAI API key |
| `OPENAI_MODEL` | `gpt-4o-mini` | AI model for summaries |
| `OPENAI_MODELS` | `gpt-4o-mini,gpt-4o` | Comma-separated available models |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible API endpoint |
| `SUMMARY_MAX_TOKENS` | `64000` | Max tokens for summary content (×3.5 chars/token) |
| `LLM_TIMEOUT` | `240` | HTTP timeout for LLM calls (summary + embeddings) in seconds. **Minimum 240 s**, because local model servers load a model on first call – lower values are raised to 240, higher values are allowed |
| `AUTO_SUMMARIZE` | `true` | Auto-generate summary on link add |
| `WHISPER_URL` | `http://localhost:8081` | Local Whisper server URL |
| `WHISPER_MODEL` | `large-v3` | Whisper model |
| `CRAWL_TIMEOUT` | `60` | Timeout per crawl request (seconds) |
| `YTDLP_PATH` | auto-detect | Path to yt-dlp binary |
| `YTDLP_SUB_LANGS` | `en.*,de.*` | Preferred subtitle languages (yt-dlp patterns, priority order). English videos use the English track, German videos the German one. Never set `.*-orig` – it downloads every original track (often Arabic on English videos). |
| `YT_SUBS_DIR` | `./subs` | Output directory of the manual helper `yt-sub` |
| `MAX_PROCESS_DAYS` | `14` | Max days backwards for batch processing |
| `RSS_BASE_URL` | *(empty)* | Base URL for feed links (e.g. behind reverse proxy). Empty = auto-detected from request host. |
| `RSS_ITEM_COUNT` | `30` | Max entries in Atom feed |
| `RSS_EXTRA_ACTION_LINK` | *(empty)* | Comma-separated list: `[Name](url)` – `{id}` is replaced (e.g. `[Publish](http://10.1.1.11:1880/publish?id={id}),[Mark read](http://10.1.1.11:1880/read?id={id})`) |
| `EMBEDDING_BASE_URL` | *(empty)* | OpenAI-compatible `/embeddings` endpoint for the topic map. Empty = local hash embedder (offline) |
| `EMBEDDING_MODEL` | *(empty)* | Model name for embeddings (e.g. `Qwen3-Embedding-4B-GGUF`). Switching invalidates old vectors |
| `EMBEDDING_API_KEY` | *(empty)* | API key for the embedding endpoint (not needed for llama.cpp). Falls back to `OPENAI_API_KEY` |

---

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Index page (HTML) |
| GET | `/api/links` | List links – Filters: `?page=N`, `?q=...`, `?url_like=...`, `?category=...`, `?note=...`, `?included=true`, `?marked=true`, `?read=true/false` · Single link: `?id=123` · Include content: `?content=true` (omitted by default) |
| – | – | **Semantic search:** `?q=...&semantic=true` ranks all entries by embedding similarity (top 60, `score` field per hit) |
| PATCH | `/api/links?id=X` | Update fields (`?note=...&included=true/false&marked=true/false&read=true/false`) |
| DELETE | `/api/links?id=X` | Delete link |
| GET | `/api/categories` | List categories with counts |
| GET/POST | `/api/share` | Get link(s) by ID |
| POST | `/linkshare` | Add link (`{"url":"...","note":"..."}`) |
| GET | `/process-entries` | Process pending entries (batch). **Auto-categorizes** all newly summarized entries afterwards |
| GET | `/api/manual` | Get this manual (raw Markdown, `?lang=de` or `?lang=en`) |
| GET | `/api/config` | Get configuration |
| POST | `/api/config` | Save configuration |
| GET/POST | `/api/language` | Get/set UI language (`{"language":"de"}`) |
| GET | `/rss` | Atom 1.0 feed (`?limit=N&q=...&url_like=...&category=...&note=...&included=true&marked=true&read=true/false`) |
| GET | `/api/feed/mark?id=X` | Set marked=1 |
| GET | `/api/feed/include?id=X` | Set included=1 |
| GET | `/api/feed/read?id=X` | Set read=1 (mark as read) |
| GET | `/api/feed/unread?id=X` | Set read=0 (mark as unread) |
| GET | `/api/feed/delete-summary?id=X` | Clear summary + content |
| GET | `/api/map` | Topic map: status, counters and all 2D points (`status`: ready/embedding/projecting/waiting/needs-embeddings) |
| POST | `/api/links/rescrape?id=N` | Reloads only an entry's content (crawl the page / fetch subtitles); the summary stays unchanged. Corrects the title from the HTML title |
| POST | `/api/links/resummarize?id=N` | Regenerates the summary from the current content (replaces the existing text); fetches the content first if it is missing. Uses the prompt template named in the note |
| GET | `/api/prompts` | List of available prompt templates (filenames without `.md`) for the note-based selection |
| POST | `/api/map/embeddings` | Start background embedding+projection (`?force=true` = recompute all vectors) – returns `202` immediately |
| GET | `/api/map/status` | Progress of the map job (`job.phase`, `job.done`, `job.total`, `job.pct`) |
| POST | `/api/map/query` | Semantic search over the map vectors (`{"query":"..."}` → top 300 with cosine scores) |
| GET/POST | `/api/search` | **RAG search for AI tools:** `?q=...&limit=5&mode=semantic\|text[&date_from=&date_to=&with_content=true]` – POST JSON `{"query","limit","mode","date_from","date_to","with_content"}`. Returns title/summary/url/score of the best matches |

---

## Prompts (custom prompts via the note)

Prompt templates live in the `prompts/` directory. The **filename without `.md`** is the
template name:

```
prompts/
├── summary_DE.md        # Summary (German)      ← default with UI_LANGUAGE=de
├── summary_EN.md        # Summary (English)     ← default with UI_LANGUAGE=en
├── chapters_DE.md       # Chapters (German)
├── chapters_EN.md       # Chapters (English)
├── categorize_DE.md     # Categorization (German)
├── categorize_EN.md     # Categorization (English)
└── custom_prompt.md     # example custom prompt
```

### Selection via the note

If an entry's **note** contains the name of a template, that template is used for the
summary – e.g. `custom_prompt` or `chapters_EN`.

| Note | Template used |
|------|---------------|
| `custom_prompt` | `custom_prompt.md` |
| `chapters_EN` | `chapters_EN.md` (English chapters even with a German UI) |
| `please use custom_prompt` | `custom_prompt.md` (name anywhere in the note) |
| *(no note)* | `summary_DE.md` or `summary_EN.md` depending on `UI_LANGUAGE` |
| `unknown_name` | `summary_DE.md` / `summary_EN.md` (fallback) |
| `kapitel` / `chapters` | `chapters_DE.md` / `chapters_EN.md` (legacy keyword) |

Rules:

- Casing does not matter as long as the name is unambiguous (`chapters_en` → `chapters_EN`).
- Templates **with** a language suffix (`…_DE`, `…_EN`) are used as given, regardless of `UI_LANGUAGE`.
- Templates **without** a suffix (`custom_prompt`) prefer `custom_prompt_DE.md` or
  `custom_prompt_EN.md` if those files exist – otherwise `custom_prompt.md`.
- **Categorization** always uses `categorize_DE.md` / `categorize_EN.md`.

### Creating your own template

1. Add a new file to `prompts/`, e.g. `my_prompt.md`.
2. Write the prompt; `{{CONTENT}}` is replaced with the entry's content
   (`{{CATEGORIES}}` in the categorization prompts expands to the category list).
3. Put `my_prompt` in the entry's **note** and save.
4. Apply it via **🧠 Regenerate summary** (detail view) – or on the next batch run.

Use `prompts/custom_prompt.md` as a starting point. In the detail view the app lists all
available template names below the note field as clickable suggestions
(endpoint `GET /api/prompts`); a click inserts the name into the note.

---

## Project Structure

```
├── main.go              # Entry point: DB, API, HTTP server
├── app.go               # App struct, prompt loading, renderPrompt
├── config.go            # Configuration loading + token estimation
├── configapi.go         # Config API schema + handlers
├── handlers.go          # HTTP handlers (CRUD, RSS, Feed, Language)
├── types.go             # Data structures, scanLink, NullBool
├── scrape.go            # Scraping: HTML title + content extraction (readability, fallbacks)
├── openai.go            # OpenAI API + Whisper integration
├── youtube.go           # YouTube subtitle extraction
├── db.go                # SQLite connection + table creation
├── go.mod / go.sum      # Dependencies
├── prompts/
│   ├── summary_DE.md / summary_EN.md
│   ├── chapters_DE.md / chapters_EN.md
│   ├── categorize_DE.md / categorize_EN.md
│   └── custom_prompt.md   # example custom prompt
├── templates/
│   └── index.html       # Frontend: HTML/CSS/JS (bilingual)
├── .env                 # Configuration (auto-created)
├── .gitignore
├── manual_DE.md         # Manual (German)
├── manual_EN.md         # Manual (English)
├── README.md            # GitHub project description
└── agent.md             # For AI assistants
```

---

## License

Internal / private. No claim.
