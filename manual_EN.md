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
| `AUTO_SUMMARIZE` | `true` | Auto-generate summary on link add |
| `WHISPER_URL` | `http://localhost:8081` | Local Whisper server URL |
| `WHISPER_MODEL` | `large-v3` | Whisper model |
| `CRAWL_TIMEOUT` | `60` | Timeout per crawl request (seconds) |
| `YTDLP_PATH` | auto-detect | Path to yt-dlp binary |
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
| POST | `/api/map/embeddings` | Start background embedding+projection (`?force=true` = recompute all vectors) – returns `202` immediately |
| GET | `/api/map/status` | Progress of the map job (`job.phase`, `job.done`, `job.total`, `job.pct`) |
| POST | `/api/map/query` | Semantic search over the map vectors (`{"query":"..."}` → top 300 with cosine scores) |

---

## Prompts

Prompt templates are stored in the `prompts/` directory and loaded by language:

```
prompts/
├── summary_DE.md        # Summary (German)
├── summary_EN.md        # Summary (English)
├── chapters_DE.md       # Chapters (German)
└── chapters_EN.md       # Chapters (English)
```

The app selects the template based on the current `UI_LANGUAGE`.

---

## Project Structure

```
├── main.go              # Entry point: DB, API, HTTP server
├── app.go               # App struct, prompt loading, renderPrompt
├── config.go            # Configuration loading + token estimation
├── configapi.go         # Config API schema + handlers
├── handlers.go          # HTTP handlers (CRUD, RSS, Feed, Language)
├── types.go             # Data structures, scanLink, NullBool
├── crawl.go             # Web crawling + markdown conversion
├── openai.go            # OpenAI API + Whisper integration
├── youtube.go           # YouTube subtitle extraction
├── db.go                # SQLite connection + table creation
├── go.mod / go.sum      # Dependencies
├── prompts/
│   ├── summary_DE.md
│   ├── summary_EN.md
│   ├── chapters_DE.md
│   └── chapters_EN.md
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
