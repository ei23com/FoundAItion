# FoundAItion – Link Collection & AI Summarizer

![Screenshot_List](assets/screenshot_01.webp)
![Screenshot_Detail](assets/screenshot_02.webp)

A **single-binary web application** for collecting, managing, and summarizing links with AI. Built with Go – no npm, no Docker, no external runtime required.

## Features

- **Link management** – Add, browse, search, filter, edit, and delete links
- **Browser-grade web scraping** – Content is extracted as Markdown via [Mozilla Readability](https://github.com/go-shiori/go-readability) (the algorithm behind Firefox's reader view), which removes cookie banners, navigation, ads and paywall frames; the title always comes from the HTML `<head><title>` (SVG logo `<title>` tags are ignored), and blocked sites are retried over HTTP/1.1 – no per-site special cases
- **RSS/Atom feeds** – feeds are recognised by their root element (`<rss>`, `<feed>`, `<rdf:RDF>`) and rendered as a compact, YAML-like item list (date, title, link, author, categories, description/content) instead of an unstructured HTML text blob – see [Feeds (RSS & Atom)](manual_EN.md#feeds-rss--atom)
- **Custom prompts via the note** – Put a template name like `chapters_EN` or `custom_prompt` into an entry's note and that `prompts/*.md` template is used for its summary instead of the default – see [Custom Prompts via the Note](#custom-prompts-via-the-note)
- **Two separate refresh actions** – 🔃 **Reload content** (re-crawls the page or reloads subtitles, corrects the title, keeps the summary) and 🧠 **Regenerate summary** (rewrites the summary from the current content) – as UI buttons and as APIs
- **External content aggregation (optional)** – `POST /linkshare` also accepts a full `content` (and `title`); when content is supplied the page is **not fetched at all**, so external tools can deliver the text themselves – see [Supplying the Content Yourself](#supplying-the-content-yourself-optional)
- **YouTube support** – Automatic thumbnail display, playlist support, subtitle/Whisper transcription (English/German subtitles; human subtitles are preferred, then the original-audio track `<lang>-orig` of auto-dubbed videos, whose plain language codes are translations and often answer with HTTP 429; helper CLI `yt-sub en|de <url>` saves subtitles as `.srt`/`.txt` on disk or writes them into the entry)
- **AI summaries** – Generate summaries via any OpenAI-compatible API (GPT, Claude, local LLMs); slow local model loading is tolerated (`LLM_TIMEOUT`, minimum 240 s). The output size is configurable (`SUMMARY_MAX_OUTPUT_TOKENS`, default 8192) and a response cut off by the output limit is **continued automatically** – if it still doesn't fit, the summary is marked as truncated instead of ending mid-sentence
- **Local LLM support** – Use [LiteLLM](https://github.com/BerriAI/litellm) or [llama.cpp](https://github.com/ggml-org/llama.cpp) server as drop-in replacements – just set `OPENAI_BASE_URL` to your local endpoint
- **Bilingual UI** – Deutsch & English, switchable at runtime
- **Categorization** – Auto-categorize entries using a separate (faster) model, language-aware
- **Smart filtering** – By category, bookmark/favorite/read status, full-text search, URL pattern, note content
- **Read tracking** – Mark articles as read/unread directly from the list view
- **RSS/Atom feed** – `/rss` endpoint with full filter support (category, URL, note, status), action links for automation
- **Interactive topic map** – every entry positioned by semantic similarity on a zoomable 2D map (deterministic UMAP, pure Go), with category colors, date filters, hover previews, and semantic search
- **RAG search API** – `GET/POST /api/search` returns the most relevant entries (title + summary + score) for a natural-language query, ready for AI tool calls
- **SQLite** – Local storage, no database server needed
- **Dark/Light mode** – Switchable theme, respects system preference

## Custom Prompts via the Note

Every entry has a **note** field. If the note names a prompt template from the `prompts/` directory, that template is used whenever a summary is generated for the entry (auto-summary when saving, batch processing, and the 🧠 button) instead of the default summary prompt:

| Note contains | Template used | Produces |
|---|---|---|
| `chapters_EN` / `chapters_DE` | `prompts/chapters_EN.md` / `_DE.md` | video chapters with timestamps |
| `categorize_EN` / `categorize_DE` | `prompts/categorize_*.md` | exactly one category |
| `custom_prompt` | `prompts/custom_prompt.md` | example custom format (key message, bullet points, action item) |
| `summary_EN` / `summary_DE` | `prompts/summary_*.md` | the default summary |
| *empty or unknown* | `summary_EN.md` / `summary_DE.md` by UI language | the default summary |

**How the template is picked**

1. The whole note is checked for a template name, then each single word (words may be separated by spaces, commas, `/`, `|`, `:` …) – spelling may differ in case (`chapters_en` works too).
2. Otherwise keywords apply: `kapitel`/`chapters` → chapters template, `kategorie`/`categorize` → categorize template.
3. Otherwise the default `summary` template; the language variant matching the current UI language (`summary_DE`/`summary_EN`) is preferred, then the generic name.

**Creating your own prompt**

1. Drop a Markdown file into `prompts/` – the filename without `.md` is the value for the note (`my_prompt.md` → note `my_prompt`).
2. Optional language variants `my_prompt_DE.md` / `my_prompt_EN.md` are preferred when the UI is set to German/English.
3. Use `{{CONTENT}}` as the placeholder for the entry's text (`{{CATEGORIES}}` additionally fills the category list in categorize templates).
4. Generate the summary – done.

The detail view shows all available templates as **clickable chips below the note field** (click to insert them into the note), and `GET /api/prompts` lists them for tools. A commented example template lives in [`prompts/custom_prompt.md`](prompts/custom_prompt.md); the manuals describe the mechanism in detail ([DE](manual_DE.md#prompts-eigene-prompts-per-notiz), [EN](manual_EN.md#prompts-custom-prompts-via-the-note)).

## Quick Start

```bash
# Build from source
git clone <your-repo>
cd foundaition
go build -o foundaition

# Configure
cp .env.example .env
# Edit .env with your OpenAI API key

# Run
./foundaition
# → http://localhost:8080
```

## Requirements

- **Go ≥ 1.23** for building
- **OpenAI API Key** (or compatible) for AI summaries
- **yt-dlp** (auto-installed) + **Whisper** (optional) for YouTube transcription

### Local Processing

Instead of OpenAI, you can point FoundAItion to any OpenAI-compatible backend:

- **[LiteLLM](https://github.com/BerriAI/litellm)** – Proxy for 100+ LLM providers (local & cloud)
- **[llama.cpp](https://github.com/ggml-org/llama.cpp)** – Run local GGUF models with its built-in OpenAI-compatible server

Set `OPENAI_BASE_URL` to your local endpoint (e.g. `http://localhost:1234/v1`).

Local models can take a while to load – the HTTP timeout for summary/embedding calls is at least 240 s (`LLM_TIMEOUT` in `.env`, larger values allowed).

### Topic Map / Embeddings

The topic map stores one embedding vector per entry (`vector` BLOB + `vec_model` columns) and projects them to 2D with a self-contained Barnes-Hut t-SNE (pure Go, deterministic). Embeddings come from either:

1. **Local hash embedder (default)** – offline, instant, lexical quality only. Used automatically when no embedding endpoint is configured.
2. **Any OpenAI-compatible `/embeddings` endpoint** – e.g. a llama.cpp server with an embedding GGUF:

   ```bash
   ./llama-server -m Qwen3-Embedding-4B-Q4_K_M.gguf \
     --port 9293 --n-gpu-layers 99 --ctx-size 8192 \
     --embedding --pooling last
   ```

   ```env
   EMBEDDING_BASE_URL=http://localhost:9293/v1
   EMBEDDING_MODEL=Qwen3-Embedding-4B-GGUF
   ```

Vectors are tagged with the model name; switching models invalidates old vectors automatically (they are re-embedded on demand). In API mode embeddings are never triggered implicitly – open the map and press **"Embed now"**, or use `POST /api/map/embeddings`. The 2D layout is cached in the `map_positions` table keyed by a fingerprint of all vectors, so restarts are instant. Small additions are placed **incrementally**: new vectors are embedded and then anchored at the distance-weighted centroid of their 15 nearest already-projected neighbors (existing points keep their exact positions); large batches or `?force=true` trigger a full UMAP re-run.

### Whisper

For YouTube transcription, FoundAItion supports the standard OpenAI Whisper API. A lightweight, ready-to-run proxy is available at:

👉 **[WhisperAPIProxy](https://github.com/ei23fxg/WhisperAPIProxy)** – Docker-based Whisper server with OpenAI-compatible API

## Android: Share Links via HTTP Shortcuts

Use the free **[HTTP Shortcuts](https://github.com/Waboodoo/HTTP-Shortcuts)** app to share links directly from Android to FoundAItion – perfect for quickly saving articles, YouTube videos, or even entire playlists:

1. Create a new shortcut with `POST` to `http://<your-instance>:8080/linkshare`
2. Set **Content-Type** to `application/json`
3. Use this body to grab the shared URL:
   ```json
   {"url": "{{param:clipboard|url}}", "note": "shared"}
   ```
4. Enable **"Share"** in the shortcut – it will appear in Android's share menu
   
See the [manual](manual_EN.md#android-share-links-via-http-shortcuts) for more details.

## Supplying the Content Yourself (optional)

`POST /linkshare` accepts two optional fields next to `url` and `note`:

| Field | Effect |
|---|---|
| `content` | Full text of the entry. If set, the page is **not fetched at all** (no request, no scraping). The text is stored as delivered, only capped to the summary token limit. |
| `title` | Title of the entry. Used whenever no HTML title is available: always when `content` is supplied, otherwise as a fallback when scraping fails. |

```json
{
  "url": "https://example.org/article",
  "note": "chapters",
  "title": "Example article",
  "content": "… the full article text …"
}
```

This enables **external content aggregation**: your own scripts, reader-mode extractors, another LLM or a paid API can deliver the text for pages FoundAItion cannot read cleanly by itself (paywall, cookie wall, login). The entry is then processed as usual – summarized right away when `AUTO_SUMMARIZE` is on, otherwise later via 🧠 or `/process-entries`.

If the URL already exists but its content is still empty (e.g. because scraping failed earlier), the supplied content is added and the response is `"status":"updated"`. Existing content is **never** overwritten – that case keeps answering `"duplicate"`.

See the [manual](manual_EN.md#supplying-the-content-yourself-content-aggregation) for details.

## Documentation

See [manual_DE.md](manual_DE.md) (German) or [manual_EN.md](manual_EN.md) (English) for full documentation.

## License

Copyright (C) 2026 ei23

This program is free software: you can redistribute it and/or modify
it under the terms of the **GNU General Public License** as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
