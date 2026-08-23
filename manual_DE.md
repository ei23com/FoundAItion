# 🔗 ei23-FoundAItion – Manual / Anleitung

Kleine, schnelle Web-Oberfläche zum **Verwalten**, **Suchen** und **Teilen** von Links mit KI-Zusammenfassung (OpenAI). Eine einzige Binärdatei, Dark-Mode by default, Datenspeicher in **SQLite** (lokal, kein Server nötig).

---

## Features

- **SQLite-Datenbank** – Lokal, keine externen Dependencies
- **Bilingual UI** – Deutsch und English, umschaltbar über die UI
- Links aus der Tabelle `links` lesen (feste Tabelle, nicht konfigurierbar)
- Kompakte Listenansicht mit Titel, Datum, Status-Dots (Included, Marked, Read)
- Detailansicht mit gerendertem Markdown (Goldmark GFM: Tabellen, Code-Blöcke, Listen)
- **Suchfunktion** mit Echtzeit-Filter über Titel, URL, Zusammenfassung
- **Kategorie-Filter** und **Include/Marked/Read-Filter** als Toggle
- **Status-Dots** in der Liste: Included (grün), Marked (gelb), Read (blau)
- **"Read"-Flag**: Einträge als gelesen/ungelesen markieren, direkt in der Liste
- **Notizen editieren** in der Detailansicht
- **Link löschen** mit Bestätigungsdialog
- YouTube-Videos erkennen + Thumbnail anzeigen
- YouTube-Playlists: Alle Videos automatisch einzeln hinzufügen
- **KI-Zusammenfassung** (OpenAI / kompatible API) – automatisch beim Hinzufügen oder per Batch
- **RSS/Atom-Feed** (`/rss`) mit Action-Links (Mark, Include, Delete Summary)
- **Whisper-Integration** für YouTube-Transkription
- **Filterwörter** im Notiz-Feld: `kapitel`/`chapters` → Kapitel-Übersicht, `skip`/`überspringen` → Verarbeitung überspringen

---

## Android: Links per HTTP Shortcuts teilen

Mit der kostenlosen App **[HTTP Shortcuts](https://github.com/Waboodoo/HTTP-Shortcuts)** (sehr empfehlenswert!) kannst du Links direkt aus dem Android-Teilen-Menü an FoundAItion senden:

1. **HTTP Shortcuts** installieren und einen neuen Shortcut anlegen
2. Methode: `POST` · URL: `http://<deine-instanz>:8080/linkshare`
3. **Content-Type**: `application/json`
4. **Body** (JSON):
   ```json
   {"url": "{{param:clipboard|url}}", "note": "geteilt"}
   ```
5. Im Shortcut **„Teilen“ aktivieren**, damit er im Android-Teilen-Dialog erscheint
6. Beliebig oft erweitern – z. B. mit einem zweiten Shortcut für YouTube-Playlists:
   ```json
   {"url": "{{param:clipboard|url}}", "note": "kapitel"}
   ```
   (dank des Filterworts `kapitel` werden Kapitelüberschriften erstellt)

So landen Artikel, Videos oder ganze Playlists mit einem Klick in FoundAItion und werden automatisch zusammengefasst.

---

## Anforderungen

| Was | Warum |
|-----|-------|
| **Go ≥ 1.21** | Kompilieren der Binary |
| **SQLite** | Datenbank (lokal, pure Go via modernc.org/sqlite, no CGO) |

### Summarizer (optional)
| Was | Warum |
|-----|-------|
| **OpenAI API Key** | KI-Zusammenfassungen (GPT-4o-mini oder kompatibel) |
| **yt-dlp** | YouTube-Audio herunterladen |
| **Whisper Server** | Lokaler Self-hosted Whisper für Audio-Transkription |
| **Colly + html-to-markdown** | Reines Go – Webseiten crawlen, HTML → Markdown |

Die Tabelle `links` wird beim ersten Start **automatisch angelegt** (`CREATE TABLE IF NOT EXISTS`).

### Tabellenschema (17 Spalten)

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
| `vector` | BLOB | NULL | Embedding-Vektor (Little-Endian float32) für die Themenkarte |
| `vec_model` | TEXT | NULL | Modellname des Vektors – ein Modellwechsel invalidiert alte Vektoren automatisch |

Zusätzlich legt FoundAItion automatisch die Tabelle **`map_positions`** an (`id`, `x`, `y`, `model`, `fingerprint`) mit den 2D-Koordinaten der Themenkarte. Der `fingerprint` ist ein Hash über alle Vektor-IDs + Modellnamen: ändern sich Einträge, wird die Karte beim nächsten Öffnen neu berechnet, sonst sofort aus dem Cache geladen.

---

## Schnellstart

```bash
# .env erstellen:
cat > .env << 'EOF'
LISTEN_PORT=8080
OPENAI_API_KEY=sk-...
YTDLP_PATH=/usr/local/bin/yt-dlp
EOF

go mod tidy && go build -o foundaition
./foundaition
# SQLite-Datei: ./foundaition.db (auto-created)
```

Dann `http://localhost:8080` im Browser öffnen. Port über `LISTEN_PORT=9000` änderbar.

---

## Bedienung

### Listenansicht (Startseite)

- **„+ Link hinzufügen"**: Ausklappbares Formular zum Einfügen neuer URLs
- **„⚙️ Prozess starten"**: Batch-Verarbeitung aller Einträge ohne Zusammenfassung
- Nummerierte Liste aller Links, neuester zuerst (paginiert: 20/Seite)
- **YouTube-Links** zeigen ein Thumbnail
- Status-Dots: Included (grün), Marked (gelb), Read (blau) – klickbar
- Kategorie-Badges rechts
- Filter-Toggles für Included/Marked/Read (An/Aus)
- **Suchfeld oben**: Echtzeit-Suche über Titel, URL, Zusammenfassung

### Detailansicht (`#link/123`)

- Titel + Metadaten (URL, Datum, Notiz)
- **Markdown-Zusammenfassung** gerendert via Goldmark (GFM)
- **Editierbereich**: Notiz bearbeiten, Included/Marked/Read togglen
- **Aktionen:**
  - 📋 URL kopieren
  - 📝 Zusammenfassung als Markdown kopieren
  - ↗️ Im Browser öffnen
- 🗑️ **Eintrag löschen** (mit Bestätigungsdialog)

### Sprachumschaltung

- Im Burger-Menü (oben links) → Sprache wählen: **Deutsch** oder **English**
- Die UI wechselt sofort alle Texte
- Prompt-Templates werden automatisch in der passenden Sprache geladen
- Einstellung wird in `.env` gespeichert (`UI_LANGUAGE=de` / `UI_LANGUAGE=en`)

### Themenkarte (`#map`, 🗺️-Button)

Jeder Eintrag wird per Embedding-Vektor auf einer interaktiven 2D-Karte positioniert – inhaltlich ähnliche Artikel liegen nah beieinander (Barnes-Hut-t-SNE, in Go implementiert, deterministisch).

**Bedienung:**
- **Scrollen / Pinchen** = Zoom, **Ziehen** = Verschieben
- **Hover** zeigt Titel, Kategorie und Datum; **Klick** öffnet den Artikel
- **Kategorien-Legende**: Farbchip anklicken blendet die Kategorie ein/aus
- **Datumsfilter**: „Von"/„Bis" grenzt den Zeitraum ein
- **Gemeinsame Suchzeile oben** (gilt für Liste und Karte): Titel-Teilfilter; mit aktivem **✨-Button** semantische Suche über die Embedding-Vektoren (Top-Treffer größer dargestellt, 🎯 zoomt zu den Treffern); daneben Datumsfilter „Von/Bis“

**Einbettungen:**
- Ohne konfiguriertes Modell läuft ein lokaler Hash-Embedder (offline, sofort, rein lexikalische Qualität)
- Mit `EMBEDDING_BASE_URL` + `EMBEDDING_MODEL` (z.B. llama.cpp mit Qwen3-Embedding-GGUF) entstehen echte semantische Vektoren. Der Backfill startet nie automatisch – im Karten-Overlay erscheint **„Jetzt einbetten"**, oder `POST /api/map/embeddings`
- Fortschritt erscheint als Progress-Overlay; die Karte lädt danach automatisch neu
- 2D-Positionen werden in der Tabelle `map_positions` gecacht → Neustarts sind instant

---

## Umgebungsvariablen

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
| `RSS_BASE_URL` | *(leer)* | Basis-URL für Feed-Links (z.B. bei Reverse Proxy). Leer = automatisch aus dem Request-Host. |
| `RSS_ITEM_COUNT` | `30` | Maximale Einträge im Atom-Feed |
| `RSS_EXTRA_ACTION_LINK` | *(leer)* | Kommagetrennte Liste von Extra-Links: `[Name](url)` – `{id}` wird ersetzt (z.B. `[Veröffentlichen](http://10.1.1.11:1880/publish?id={id}),[Als gelesen](http://10.1.1.11:1880/read?id={id})`) |
| `EMBEDDING_BASE_URL` | *(leer)* | OpenAI-kompatibler `/embeddings`-Endpunkt für die Themenkarte. Leer = lokaler Hash-Embedder (offline) |
| `EMBEDDING_MODEL` | *(leer)* | Modellname für Embeddings (z.B. `Qwen3-Embedding-4B-GGUF`). Wechsel invalidiert alte Vektoren |
| `EMBEDDING_API_KEY` | *(leer)* | API-Key für den Embedding-Endpunkt (llama.cpp: nicht nötig). Fällt auf `OPENAI_API_KEY` zurück |

---

## API-Endpunkte

| Methode | Pfad | Beschreibung |
|---------|------|-------------|
| GET | `/` | Index page (HTML) |
| GET | `/api/links` | Links auflisten – Filter: `?page=N`, `?q=...`, `?url_like=...`, `?category=...`, `?note=...`, `?included=true`, `?marked=true`, `?read=true/false` · Einzellink: `?id=123` · Inhalt mitsenden: `?content=true` (sonst ohne content-Spalte) |
| – | – | **Semantische Suche:** `?q=...&semantic=true` rankt alle Einträge per Embedding-Ähnlichkeit (Top 60, Feld `score` je Treffer) |
| PATCH | `/api/links?id=X` | Felder aktualisieren (`?note=...&included=true/false&marked=true/false&read=true/false`) |
| DELETE | `/api/links?id=X` | Delete link |
| GET | `/api/categories` | List categories with counts |
| GET/POST | `/api/share` | Get link(s) by ID |
| POST | `/linkshare` | Add link (`{"url":"...","note":"..."}`) |
| GET | `/process-entries` | Verarbeitet anstehende Einträge (Batch). **Kategorisiert anschließend automatisch** alle neu zusammengefassten Einträge |
| GET | `/api/manual` | Get this manual (raw Markdown) |
| GET | `/api/config` | Get configuration |
| POST | `/api/config` | Save configuration |
| GET/POST | `/api/language` | Get/set UI language (`{"language":"de"}`) |
| GET | `/rss` | Atom 1.0 feed (`?limit=N&q=...&url_like=...&category=...&note=...&included=true&marked=true&read=true/false`) |
| GET | `/api/feed/mark?id=X` | Set marked=1 |
| GET | `/api/feed/include?id=X` | Set included=1 |
| GET | `/api/feed/read?id=X` | Set read=1 (als gelesen markieren) |
| GET | `/api/feed/unread?id=X` | Set read=0 (als ungelesen markieren) |
| GET | `/api/feed/delete-summary?id=X` | Clear summary + content |
| GET | `/api/map` | Themenkarte: Status, Zähler und alle 2D-Punkte (`status`: ready/embedding/projecting/waiting/needs-embeddings) |
| POST | `/api/map/embeddings` | Startet Embedding+Projektion im Hintergrund (`?force=true` = alle Vektoren neu) – gibt sofort `202` zurück |
| GET | `/api/map/status` | Fortschritt des Karten-Jobs (`job.phase`, `job.done`, `job.total`, `job.pct`) |
| POST | `/api/map/query` | Semantische Suche über die Kartenvektoren (`{"query":"..."}` → Top-300 mit Kosinus-Scores) |
| GET/POST | `/api/search` | **RAG-Suche für KI-Tools:** `?q=...&limit=5&mode=semantic\|text[&date_from=&date_to=&with_content=true]` – POST mit JSON-Body `{"query","limit","mode","date_from","date_to","with_content"}`. Liefert Titel/Zusammenfassung/URL/Score der besten Treffer |

---

## Prompts

Die Prompt-Templates liegen im `prompts/`-Verzeichnis und werden sprachspezifisch geladen:

```
prompts/
├── summary_DE.md        # Zusammenfassung (Deutsch)
├── summary_EN.md        # Summary (English)
├── chapters_DE.md       # Kapitel-Übersicht (Deutsch)
└── chapters_EN.md       # Chapter overview (English)
```

Die App wählt das Template basierend auf der aktuellen `UI_LANGUAGE` aus.

---

## Projektstruktur

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
├── manual.md            # ← this file
├── README.md            # GitHub project description
└── agent.md             # For AI assistants
```

---

## Lizenz

Intern / privat.
