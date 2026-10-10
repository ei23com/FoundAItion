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

> **Hinweis zu lokalen Modellen:** Lädt der Modellserver (z. B. llama-swap, Ollama) das Modell erst beim
> ersten Aufruf, kann eine Anfrage mehrere Minuten dauern. Das HTTP-Timeout für LLM-Aufrufe liegt deshalb
> bei **mindestens 240 Sekunden** (`LLM_TIMEOUT`, siehe Umgebungsvariablen).

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
- 🔃 **Inhalt neu laden** (im Editierbereich, neben „Zusammenfassung neu erstellen"): crawlt die Webseite erneut bzw. lädt die Untertitel neu (Englisch/Deutsch). Der Inhalt wird ersetzt, die **Zusammenfassung bleibt unverändert** – damit lassen sich alte Einträge korrigieren, deren Inhalt z. B. in der falschen Sprache geladen wurde. Der Titel wird dabei aus dem HTML-Titel korrigiert (siehe „Webseiten-Scraping")
- 🧠 **Zusammenfassung neu erstellen** (ersetzt das frühere „Zusammenfassung zurücksetzen"): erzeugt die Zusammenfassung aus dem aktuellen Inhalt neu – sie wird also nicht nur gelöscht, sondern neu geschrieben. Fehlt der Inhalt, wird er zuvor geladen. Schlägt der LLM-Aufruf fehl, bleibt die vorhandene Zusammenfassung erhalten
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
- Solange Punkte ohne Vektor existieren, steht in der Karten-Toolbar der Button **🚀 N** (N = Anzahl). Klick = fehlende Vektoren einbetten und platzieren; **Shift+Klick** = komplette UMAP über alle Vektoren neu berechnen
- Kleine Nachschübe werden **inkrementell** platziert: nur die neuen Vektoren werden eingebettet, dann als gewichteter Zentroid ihrer 15 nächsten projizierten Nachbarn angehängt (Sekunden statt Minuten). Bestehende Punkte behalten exakt ihre Positionen. Große Batches (mehr als max(25, 2 % der vorhandenen Punkte)) oder `?force=true` berechnen die UMAP vollständig neu
- 2D-Positionen werden in der Tabelle `map_positions` gecacht → Neustarts sind instant

---

## YouTube-Untertitel & Helfer `yt-sub`

**Sprachauswahl:** Transkripte kommen in Englisch oder Deutsch (`YTDLP_SUB_LANGS`, Default `en.*,de.*`).
Bei englischen Videos wird die englische Spur genutzt, bei deutschen die deutsche – erkannt über die im Video
gesprochene Sprache. Innerhalb einer Sprache haben **menschliche** Untertitel Vorrang vor automatischen
(`srt_fix` bereinigt automatische Rollzeilen). Passt keine Spur, wird bewusst kein Transkript erzeugt
(kein fremdsprachiges Material). Anfragen bleiben minimal (`en,en-orig,de,de-orig`); nur wenn das nichts
liefert, wird breiter gesucht – das schont YouTubes Ratelimit (HTTP 429).

**Manueller Helfer**, wenn die Pipeline einmal keine Untertitel findet:

```bash
./yt-sub en "https://www.youtube.com/watch?v=XXXXXXXXXXX"   # Englisch
./yt-sub de "https://youtu.be/XXXXXXXXXXX"                  # Deutsch
./yt-sub en,de "<url>"                                      # beide, Priorität wie oben
./yt-sub --list "<url>"                                     # verfügbare Sprachen anzeigen
```

Das Skript wechselt ins Programmverzeichnis (nutzt `.env`, `foundaition.db`, `yt-dlp_linux` und das Plugin)
und ruft intern `./foundaition yt-sub …` auf. Es speichert im Zielverzeichnis (`YT_SUBS_DIR`, Default `./subs`):

| Datei | Inhalt |
|-------|--------|
| `<videoID>.<sprache>.srt` | heruntergeladene Untertitel (bzw. `.vtt`, wenn nur VTT vorliegt) |
| `<videoID>.<sprache>.txt` | dasselbe im Programmformat `HH:MM:SS Text` (wie Spalte `content`) |

Ist die URL in der Datenbank vorhanden, wird das Transkript direkt in `content` geschrieben – danach können
Zusammenfassung und Themenkarte es nutzen. Optionen: `--out DIR`, `--manual` (nur menschliche Untertitel),
`--auto` (nur automatische), `--no-db`, `--timeout SEK`, `-h`.

---

## Webseiten-Scraping (Titel & Inhalt)

Für Web-Einträge bestimmt FoundAItion Titel und Inhalt in einer festen Pipeline – ganz ohne Sonderbehandlung einzelner Seiten:

1. **Laden** der Seite mit echtem Browser-User-Agent und Browser-Headern. Schlägt der Versuch fehl (manche Seiten blockieren z. B. den HTTP/2-Fingerprint des Go-Stacks mit `HTTP 419`, während Browser/`curl` 200 bekommen), wird **automatisch mit erzwungenem HTTP/1.1** erneut versucht.
2. **Titel = HTML-Titel** (`<head><title>`, 1:1 übernommen). `<title>`-Tags aus SVG-Logos (z. B. „Electrek Logo") werden dabei **ignoriert** – das war früher die häufigste Ursache für falsche Titel. Fallbacks: `og:title`, erste `H1`.
3. **Inhalt = Hauptartikel** per **Mozilla-Readability** (genau der Algorithmus hinter der Firefox-Leseansicht – entfernt Cookie-Banner, Navigation, Werbung, Paywall-Rahmen). Fallbacks: `<article>`/`<main>` mit Boilerplate-Filter, zuletzt der bereinigte `<body>`. Ist das Readability-Ergebnis dünn und die bereinigte Seite deutlich umfangreicher (z. B. Diskussions-/Forumseiten wie Hacker News), gewinnt die bereinigte Seite – so bleibt der Thread erhalten.
4. **Kein sauberer Inhalt?** Dann bleibt der Inhalt **leer** – es wird **nie** eine Fehlermeldung als Inhalt gespeichert. Trage den Inhalt in dem Fall manuell in der Detailansicht ein (Titel und Zusammenfassung lassen sich dort ebenfalls bearbeiten).

Der 🔃-Button „Inhalt neu laden" nutzt dieselbe Pipeline und korrigiert dabei auch falsche Alt-Titel.

RSS-/Atom-Feeds laufen nicht durch diese Pipeline, sondern über einen eigenen Pfad → [Feeds (RSS & Atom)](#feeds-rss--atom).

### Inhalt selbst mitliefern (Content-Aggregation)

`POST /linkshare` nimmt neben `url` und `note` zwei optionale Felder entgegen:

| Feld | Wirkung |
|------|---------|
| `content` | Vollständiger Text des Eintrags. Ist es gesetzt, wird die Seite **gar nicht geladen** – kein HTTP-Request, kein Scraping. Der Text wird 1:1 gespeichert und nur auf das Token-Limit gekürzt. |
| `title` | Titel des Eintrags. Wird immer dann genutzt, wenn kein HTML-Titel ermittelt wird: also immer bei mitgeliefertem `content`, sonst als Fallback, wenn das Scrapen scheitert. |

```json
{
  "url": "https://example.org/artikel",
  "note": "kapitel",
  "title": "Beispielartikel",
  "content": "… vollständiger Artikeltext …"
}
```

Damit lassen sich **externe Content-Aggregatoren** anbinden: eigene Skripte, Reader-Mode-Extraktoren, ein anderes LLM oder eine bezahlte API liefern den Text für Seiten, die FoundAItion selbst nicht sauber auslesen kann (Paywall, Cookie-Wand, Login). Der Eintrag wird danach normal weiterverarbeitet – bei aktivem `AUTO_SUMMARIZE` sofort zusammengefasst, sonst später über 🧠, 🔃 oder `/process-entries`.

Existiert die URL bereits, ist aber ihr Inhalt noch leer (z. B. weil das Scrapen gescheitert ist), wird der mitgelieferte Inhalt ergänzt – die Antwort lautet dann `"status":"updated"`. Vorhandener Inhalt wird **nie** überschrieben; in dem Fall bleibt es bei `"duplicate"`.

## Feeds (RSS & Atom)

Feeds sind XML und laufen **nicht** durch die HTML-/Readability-Pipeline – daraus entstünde ein unstrukturierter Textbrei: CDATA-Reste (`]]&gt;`), riesige Vorschaubild-URLs und keine Trennung zwischen den Einträgen. Bei einem Feed mit 500 Einträgen blieb am Ende praktisch nur der erste übrig.

FoundAItion erkennt Feeds deshalb generisch am Wurzelelement (`<rss>`, `<feed>`, `<rdf:RDF>`) – unabhängig von Host und Content-Type – und wertet sie strukturiert aus:

- **Titel des Eintrags** = Feed-Titel (z. B. `r/LocalLLaMA | FreshRSS`)
- **Pro Feed-Eintrag**: Datum (normalisiert auf `JJJJ-MM-TT hh:mm`), Titel, Link, Autor, Kategorie(n) sowie `description`/`summary` und – falls deutlich umfangreicher – `content`
- **Vorschaubild-URLs, CDATA-Reste und leere Links entfallen**; HTML-Beschreibungen werden zu kompaktem Markdown aufbereitet
- Einträge ganz ohne Text behalten wenigstens ihre Bild-URL (`- image: …`)

Das Format ist bewusst YAML-artig und zugleich gültiges Markdown:

```markdown
feed: r/LocalLLaMA | FreshRSS
link: http://10.1.1.11:2224
description: RSS-Feed von r/LocalLLaMA | FreshRSS
updated: 2026-10-10 17:47
items: 28

## OpenAI's math findings built on stolen user data

- date: 2026-10-09 19:32
- link: https://www.reddit.com/r/LocalLLaMA/comments/1x1rk4o/openais_math_findings_built_on_stolen_user_data/
- author: /u/CuTe_M0nitor
- category: r/LocalLLaMA

submitted by /u/CuTe_M0nitor [link](…) [comments](…)
```

Passt ein Feed nicht komplett ins Token-Limit (`SUMMARY_MAX_TOKENS`), wird an einer Eintragsgrenze abgeschnitten und die Zahl der ausgelassenen Einträge vermerkt (`… N weitere Einträge ausgelassen (Zeichenlimit)`). Der 🔃-Button „Inhalt neu laden" nutzt denselben Feed-Pfad, ebenso die automatische Zusammenfassung.

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
| `SUMMARY_MAX_OUTPUT_TOKENS` | `8192` | Wie viele Token das Modell **pro Antwort** erzeugen darf. Erreicht die Zusammenfassung dieses Limit (`finish_reason: length`), wird sie automatisch nahtlos fortgesetzt (bis zu 3 Runden); bleibt sie danach immer noch unvollständig, wird sie sichtbar als abgeschnitten gekennzeichnet. Bei Denk-Modellen zählt das Reasoning mit ins Limit – zu kleine Werte liefern dann gar keinen Text |
| `LLM_TIMEOUT` | `240` | HTTP-Timeout für LLM-Aufrufe (Zusammenfassung + Embeddings) in Sekunden. **Minimum 240 s**, weil lokale Modellserver ein Modell erst beim ersten Aufruf laden – kleinere Werte werden auf 240 angehoben, größere sind möglich |
| `AUTO_SUMMARIZE` | `true` | Auto-generate summary on link add |
| `WHISPER_URL` | `http://localhost:8081` | Local Whisper server URL |
| `WHISPER_MODEL` | `large-v3` | Whisper model |
| `CRAWL_TIMEOUT` | `60` | Timeout per crawl request (seconds) |
| `YTDLP_PATH` | auto-detect | Path to yt-dlp binary |
| `YTDLP_SUB_LANGS` | `en.*,de.*` | Bevorzugte Untertitelsprachen (yt-dlp-Muster, Prioritätsreihenfolge). Englische Videos nutzen die englische, deutsche die deutsche Spur. Nie `.*-orig` setzen – das lädt jede Original-Spur (bei englischen Videos oft Arabisch). |
| `YT_SUBS_DIR` | `./subs` | Zielverzeichnis des manuellen Helfers `yt-sub` |
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
| POST | `/linkshare` | Link hinzufügen: `{"url":"...","note":"...","title":"...","content":"..."}` – `title`/`content` optional, mit `content` wird nicht gescraped ([Details](#inhalt-selbst-mitliefern-content-aggregation)) |
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
| POST | `/api/links/rescrape?id=N` | Lädt nur den Inhalt eines Eintrags neu (Webseite crawlen bzw. Untertitel holen); die Zusammenfassung bleibt unverändert. Korrigiert den Titel aus dem HTML-Titel |
| POST | `/api/links/resummarize?id=N` | Erstellt die Zusammenfassung aus dem aktuellen Inhalt neu (ersetzt den vorhandenen Text); fehlt der Inhalt, wird er zuvor geladen. Nutzt das Prompt-Template aus der Notiz |
| GET | `/api/prompts` | Liste der verfügbaren Prompt-Templates (Dateinamen ohne `.md`) für die Notiz-Auswahl |
| POST | `/api/map/embeddings` | Startet Embedding fehlender Punkte + Platzierung im Hintergrund (kleine Batches inkrementell; `?force=true` = alle Vektoren neu per voller UMAP) – gibt sofort `202` zurück |
| GET | `/api/map/status` | Fortschritt des Karten-Jobs (`job.phase`, `job.done`, `job.total`, `job.pct`) |
| POST | `/api/map/query` | Semantische Suche über die Kartenvektoren (`{"query":"..."}` → Top-300 mit Kosinus-Scores) |
| GET/POST | `/api/search` | **RAG-Suche für KI-Tools:** `?q=...&limit=5&mode=semantic\|text[&date_from=&date_to=&with_content=true]` – POST mit JSON-Body `{"query","limit","mode","date_from","date_to","with_content"}`. Liefert Titel/Zusammenfassung/URL/Score der besten Treffer |

---

## Prompts (eigene Prompts per Notiz)

Die Prompt-Templates liegen im `prompts/`-Verzeichnis. Der **Dateiname ohne `.md`** ist der
Name des Templates:

```
prompts/
├── summary_DE.md        # Zusammenfassung (Deutsch)      ← Standard bei UI_LANGUAGE=de
├── summary_EN.md        # Summary (English)              ← Standard bei UI_LANGUAGE=en
├── chapters_DE.md       # Kapitel-Übersicht (Deutsch)
├── chapters_EN.md       # Chapter overview (English)
├── categorize_DE.md     # Kategorisierung (Deutsch)
├── categorize_EN.md     # Kategorisierung (English)
└── custom_prompt.md     # Beispiel für einen eigenen Prompt
```

### Auswahl über die Notiz

Steht in der **Notiz** eines Eintrags der Name eines Templates, wird genau dieses Template
für die Zusammenfassung verwendet – z. B. `custom_prompt` oder `chapters_EN`.

| Notiz | Verwendetes Template |
|-------|----------------------|
| `custom_prompt` | `custom_prompt.md` |
| `chapters_EN` | `chapters_EN.md` (englische Kapitel, auch bei deutscher Oberfläche) |
| `Bitte custom_prompt verwenden` | `custom_prompt.md` (Name irgendwo in der Notiz) |
| *(keine Notiz)* | `summary_DE.md` bzw. `summary_EN.md` je nach `UI_LANGUAGE` |
| `unbekannter_name` | `summary_DE.md` bzw. `summary_EN.md` (Fallback) |
| `kapitel` / `chapters` | `chapters_DE.md` bzw. `chapters_EN.md` (bisheriges Schlüsselwort) |

Regeln:

- Groß-/Kleinschreibung ist egal, solange der Name eindeutig ist (`chapters_en` → `chapters_EN`).
- Templates **mit** Sprachsuffix (`…_DE`, `…_EN`) werden unverändert genutzt, unabhängig von `UI_LANGUAGE`.
- Templates **ohne** Sprachsuffix (`custom_prompt`) nutzen `custom_prompt_DE.md` bzw.
  `custom_prompt_EN.md`, falls diese Dateien existieren – sonst `custom_prompt.md`.
- Die **Kategorisierung** verwendet immer `categorize_DE.md` / `categorize_EN.md`.

### Eigenes Template anlegen

1. Neue Datei im Ordner `prompts/` anlegen, z. B. `mein_prompt.md`.
2. Text hineinschreiben; `{{CONTENT}}` wird durch den Inhalt des Eintrags ersetzt
   (`{{CATEGORIES}}` steht in den Kategorisierungs-Prompts für die Kategorieliste).
3. Beim Eintrag in die **Notiz** `mein_prompt` schreiben und speichern.
4. Über **🧠 Zusammenfassung neu erstellen** (Detailansicht) anwenden – oder beim nächsten
   Sammel-Lauf.

Als Vorlage dient `prompts/custom_prompt.md`. In der Detailansicht zeigt die App unter dem
Notizfeld alle verfügbaren Template-Namen als antippbare Vorschläge an
(Endpunkt `GET /api/prompts`); ein Klick übernimmt den Namen in die Notiz.

---

## Projektstruktur

```
├── main.go              # Entry point: DB, API, HTTP server
├── app.go               # App struct, prompt loading, renderPrompt
├── config.go            # Configuration loading + token estimation
├── configapi.go         # Config API schema + handlers
├── handlers.go          # HTTP handlers (CRUD, RSS, Feed, Language)
├── types.go             # Data structures, scanLink, NullBool
├── scrape.go            # Scraping: HTML-Titel + Inhalts-Extraktion (Readability, Fallbacks)
├── openai.go            # OpenAI API + Whisper integration
├── youtube.go           # YouTube subtitle extraction
├── db.go                # SQLite connection + table creation
├── go.mod / go.sum      # Dependencies
├── prompts/
│   ├── summary_DE.md / summary_EN.md
│   ├── chapters_DE.md / chapters_EN.md
│   ├── categorize_DE.md / categorize_EN.md
│   └── custom_prompt.md   # Beispiel für eigene Prompts
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
