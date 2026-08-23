# FoundAItion – Link Collection & AI Summarizer

![Screenshot_List](assets/screenshot_01.webp)
![Screenshot_Detail](assets/screenshot_02.webp)

A **single-binary web application** for collecting, managing, and summarizing links with AI. Built with Go – no npm, no Docker, no external runtime required.

## Features

- **Link management** – Add, browse, search, filter, edit, and delete links
- **Web crawling** – Built-in crawler (Colly) extracts page content as Markdown
- **YouTube support** – Automatic thumbnail display, playlist support, subtitle/Whisper transcription
- **AI summaries** – Generate summaries via any OpenAI-compatible API (GPT, Claude, local LLMs)
- **Local LLM support** – Use [LiteLLM](https://github.com/BerriAI/litellm) or [llama.cpp](https://github.com/ggml-org/llama.cpp) server as drop-in replacements – just set `OPENAI_BASE_URL` to your local endpoint
- **Bilingual UI** – Deutsch & English, switchable at runtime
- **Categorization** – Auto-categorize entries using a separate (faster) model, language-aware
- **Smart filtering** – By category, bookmark/favorite/read status, full-text search, URL pattern, note content
- **Read tracking** – Mark articles as read/unread directly from the list view
- **RSS/Atom feed** – `/rss` endpoint with full filter support (category, URL, note, status), action links for automation
- **Interactive topic map** – every entry positioned by semantic similarity on a zoomable 2D map (t-SNE), with category colors, date filters, hover previews, and semantic search
- **RAG search API** – `GET/POST /api/search` returns the most relevant entries (title + summary + score) for a natural-language query, ready for AI tool calls
- **SQLite** – Local storage, no database server needed
- **Dark/Light mode** – Switchable theme, respects system preference

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

Vectors are tagged with the model name; switching models invalidates old vectors automatically (they are re-embedded on demand). In API mode embeddings are never triggered implicitly – open the map and press **"Embed now"**, or use `POST /api/map/embeddings`. The 2D layout is cached in the `map_positions` table keyed by a fingerprint of all vectors, so restarts are instant.

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
