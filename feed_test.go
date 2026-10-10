package main

import (
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("Fixture %s lesen: %v", name, err)
	}
	return raw
}

// Feeds werden am Wurzelelement erkannt – HTML-Seiten nicht.
func TestFeedDetection(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"rss", `<?xml version="1.0"?><rss version="2.0"><channel><title>X</title></channel></rss>`, "rss"},
		{"rss mit BOM/Whitespace", "\n  <?xml version=\"1.0\"?>\n<!-- c -->\n<rss><channel/></rss>", "rss"},
		{"atom", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>X</title></feed>`, "feed"},
		{"rdf (RSS 1.0)", `<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><channel/></rdf:RDF>`, "rdf"},
		{"html", `<!doctype html><html><head><title>Seite</title></head><body><p>Hallo</p></body></html>`, ""},
		{"html ohne doctype", `<html><body>nur text</body></html>`, ""},
		{"leer", ``, ""},
		{"xml ohne feed-wurzel", `<?xml version="1.0"?><sitemapindex><sitemap><loc>x</loc></sitemap></sitemapindex>`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedRootElement([]byte(tc.raw)); got != tc.want {
				t.Fatalf("feedRootElement = %q, erwartet %q", got, tc.want)
			}
		})
	}
}

// Der Beispiel-Feed (FreshRSS/Reddit): alle Einträge, aufgeräumt, ohne
// Bild-URLs und ohne CDATA-Reste.
func TestExtractFeedPageRSS(t *testing.T) {
	raw := readFixture(t, "feed_rss_reddit.xml")

	title, content, err := extractPage(raw, "http://10.1.1.11:2224/i/?a=rss&get=f_80")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if title != "r/LocalLLaMA | FreshRSS" {
		t.Errorf("Titel = %q, erwartet den Feed-Titel", title)
	}

	// Beide Einträge müssen enthalten sein (früher blieb nur der erste übrig).
	for _, want := range []string{
		"## OpenAI's math findings built on stolen user data",
		"## Qwen/Qwen-Image-2.1-Turbo · Hugging Face",
		"- date: 2026-10-09 19:32",
		"- date: 2026-10-09 15:28",
		"- link: https://www.reddit.com/r/LocalLLaMA/comments/1x1rk4o/",
		"- author: /u/CuTe_M0nitor",
		"- category: r/LocalLLaMA",
		"items: 2",
		"feed: r/LocalLLaMA | FreshRSS",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("Inhalt enthält %q nicht:\n%s", want, content)
		}
	}

	// Ballast darf nicht mehr auftauchen.
	for _, unwanted := range []string{
		"external-preview.redd.it", // Vorschaubild-URLs
		"]]>", "]]&gt;",            // CDATA-Reste
		"media:thumbnail",
		"submitted by   <a", // rohes HTML
	} {
		if strings.Contains(content, unwanted) {
			t.Errorf("Inhalt enthält weiterhin %q:\n%s", unwanted, content)
		}
	}

	// Deutlich kompakter als der HTML-Pfad (vorher ~2,3 kB für 2 Einträge).
	if len(content) > 1500 {
		t.Errorf("Inhalt = %d Zeichen, erwartet deutlich kompakter", len(content))
	}
}

// Atom-Feeds: Link über href, published-Datum, Autor, Kategorien, Textfelder.
func TestExtractFeedPageAtom(t *testing.T) {
	raw := readFixture(t, "feed_atom.xml")

	title, content, err := extractPage(raw, "https://example.org/feed.atom")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if title != "Beispiel-Blog" {
		t.Errorf("Titel = %q, erwartet \"Beispiel-Blog\"", title)
	}

	for _, want := range []string{
		"## Warum UMAP statt t-SNE",
		"- date: 2026-10-08 09:30",
		"- link: https://example.org/umap", // rel=alternate, nicht der self-Link
		"- author: Anna Beispiel",
		"- category: Datenvisualisierung, Go",
		"description:",
		"content:",
		"[Detailartikel](https://example.org/details)",
		"## Kurzer Nachtrag",
		"- date: 2026-10-09 07:15",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("Inhalt enthält %q nicht:\n%s", want, content)
		}
	}
	// Der Feed-Selbstlink darf nie als Eintrags-Link auftauchen.
	if strings.Contains(content, "- link: https://example.org/feed.atom") {
		t.Errorf("Eintrags-Link zeigt auf den Feed selbst:\n%s", content)
	}
}

// Ist content:encoded nur eine Kopie der description, wird sie nicht doppelt
// ausgegeben.
func TestExtractFeedSkipsDuplicateContent(t *testing.T) {
	text := strings.Repeat("Ein und derselbe Text in beiden Feldern. ", 5)
	raw := []byte(`<?xml version="1.0"?><rss version="2.0"><channel>
		<title>Feed</title>
		<item>
			<title>Eintrag</title>
			<link>https://example.org/a</link>
			<description><![CDATA[<p>` + text + `</p>]]></description>
			<content:encoded xmlns:content="http://purl.org/rss/1.0/modules/content/"><![CDATA[<p>` + text + `</p>]]></content:encoded>
		</item>
	</channel></rss>`)

	_, content, err := extractPage(raw, "https://example.org/feed")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if n := strings.Count(content, strings.TrimSpace(text)); n != 1 {
		t.Errorf("Textblock kommt %d mal vor, erwartet 1:\n%s", n, content)
	}
	if strings.Contains(content, "content:") {
		t.Errorf("doppelter Inhalt wurde als content: mitgeschrieben:\n%s", content)
	}
}

// Reiner Text ohne HTML bleibt roh (keine Markdown-Escapes).
func TestExtractFeedPlainTextDescription(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title>
		<item><title>Titel</title><link>https://example.org/a</link>
		<description>Preis: 3 &lt; 5 &amp; mehr_text mit [Klammern]</description></item>
	</channel></rss>`)

	_, content, err := extractPage(raw, "https://example.org/feed")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if !strings.Contains(content, "Preis: 3 < 5 & mehr_text mit [Klammern]") {
		t.Errorf("Text wurde verändert:\n%s", content)
	}
}

// maxChars begrenzt die Ausgabe an einer Eintragsgrenze – nicht mitten im
// Eintrag – und zählt die ausgelassenen Einträge.
func TestExtractFeedRespectsMaxChars(t *testing.T) {
	_, full, err := extractPage(readFixture(t, "feed_rss_reddit.xml"), "https://example.org/feed")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	_, limited, err := extractPageLimit(readFixture(t, "feed_rss_reddit.xml"), "https://example.org/feed", 700)
	if err != nil {
		t.Fatalf("extractPageLimit: %v", err)
	}

	if len(limited) >= len(full) {
		t.Fatalf("Limit hat nicht gegriffen: %d >= %d", len(limited), len(full))
	}
	if !strings.Contains(limited, "## OpenAI's math findings") {
		t.Errorf("erster Eintrag fehlt:\n%s", limited)
	}
	if strings.Contains(limited, "## Qwen/Qwen-Image-2.1-Turbo") && !strings.Contains(limited, "weitere Einträge ausgelassen") {
		t.Errorf("zweiter Eintrag ohne Hinweis auf Auslassungen:\n%s", limited)
	}
	if !strings.Contains(limited, "1 weitere Einträge ausgelassen") {
		t.Errorf("Hinweis auf ausgelassene Einträge fehlt:\n%s", limited)
	}
	if !strings.HasSuffix(limited, "(Zeichenlimit)") {
		t.Errorf("Ausgabe endet nicht am Hinweis:\n%s", limited)
	}
}

// Feed ohne Einträge: Titel bleibt, Inhalt leer, Fehler – nie Müll als Inhalt.
func TestExtractFeedWithoutItems(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Leerer Feed</title></channel></rss>`)

	title, content, err := extractPage(raw, "https://example.org/feed")
	if err == nil {
		t.Fatalf("erwartet Fehler für Feed ohne Einträge")
	}
	if title != "Leerer Feed" {
		t.Errorf("Titel = %q, erwartet \"Leerer Feed\"", title)
	}
	if content != "" {
		t.Errorf("Inhalt = %q, erwartet leer", content)
	}
}

// Auch abgeschnittene/nicht wohlgeformte Feeds werden ausgewertet.
func TestExtractFeedToleratesBrokenXML(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed & Feed</title>
		<item><title>Erster & zweiter</title><link>https://example.org/a</link>
		<description>Text mit unmaskiertem & Zeichen und ohne Ende`)

	title, content, err := extractPage(raw, "https://example.org/feed")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if !strings.Contains(title, "Feed") {
		t.Errorf("Titel = %q", title)
	}
	if !strings.Contains(content, "## Erster") {
		t.Errorf("Eintrag fehlt:\n%s", content)
	}
}

func TestFormatFeedDate(t *testing.T) {
	tests := map[string]string{
		"Fri, 09 Oct 2026 19:32:13 +0200": "2026-10-09 19:32",
		"Sat, 10 Oct 2026 18:51:53 +0200": "2026-10-10 18:51",
		"Fri, 09 Oct 2026 15:28:14 GMT":   "2026-10-09 15:28",
		"2026-10-08T09:30:00Z":            "2026-10-08 09:30",
		"2026-10-09T07:15:00+02:00":       "2026-10-09 07:15",
		"2026-10-09 07:15:00":             "2026-10-09 07:15",
		"unbekanntes Format":              "unbekanntes Format",
		"":                                "",
	}
	for in, want := range tests {
		if got := formatFeedDate(in); got != want {
			t.Errorf("formatFeedDate(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// Bild-URLs werden verworfen; hat ein Eintrag keinen Text, bleibt wenigstens
// die Bild-URL übrig.
func TestExtractFeedImageOnlyItem(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel>
		<title>Bilder-Feed</title>
		<item><title>Nur ein Bild</title><link>https://example.org/bild</link>
		<media:content url="https://example.org/bild.jpg" medium="image"/></item>
	</channel></rss>`)

	_, content, err := extractPage(raw, "https://example.org/feed")
	if err != nil {
		t.Fatalf("extractPage: %v", err)
	}
	if !strings.Contains(content, "- image: https://example.org/bild.jpg") {
		t.Errorf("Bild-URL fehlt:\n%s", content)
	}
}
