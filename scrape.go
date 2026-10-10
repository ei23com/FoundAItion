package main

// Scraping von Webseiten: Laden, Titel- und Inhaltsextraktion.
//
// Pipeline (crawler-freundlich, ohne seiten-spezifische Sonderbehandlung):
//
//  1. HTML laden: echter Browser-User-Agent + Browser-Accept-Header.
//     Allgemeiner Netzwerk-Fallback: Schlägt der Versuch fehl (manche Seiten
//     blockieren den HTTP/2-Fingerprint des Go-Stacks, z. B. mit HTTP 419),
//     wird automatisch mit erzwungenem HTTP/1.1 erneut versucht.
//  2. Titel: ausschließlich <head><title> (HTML-Titel, 1:1). SVG-Logos
//     enthalten ebenfalls <title>-Tags – die dürfen nie als Titel herhalten.
//     Fallbacks: og:title-Meta, dann erste H1-Überschrift.
//  3. Inhalt: zuerst Mozilla-Readability (wie die Firefox-Leseansicht –
//     entfernt Cookie-Banner, Navigation, Werbung, Paywall-Rahmen).
//     Fallback: <article>/<main> mit Boilerplate-Bereinigung, zuletzt der
//     bereinigte <body>.
//  4. Kommt dabei kein sauberer Inhalt heraus, wird ein Fehler geliefert und
//     der Inhalt bleibt leer – es wird niemals eine Fehlermeldung als Inhalt
//     gespeichert (der Nutzer füllt den Inhalt dann manuell).

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	stdhtml "html"
	"io"
	"log"
	"net/http"
	nurl "net/url"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

// browserUA ist ein realistischer Firefox-User-Agent. Viele Seiten lehnen
// Bot-User-Agents ab oder zeigen ihnen Cookie-Wände statt Inhalte.
const browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"

// maxFetchBytes begrenzt die Größe einer geladenen Seite (8 MB).
const maxFetchBytes = 8 << 20

// minSubstantial ist die Länge, ab der ein Readability-Ergebnis als echter
// Hauptinhalt gilt. Ist es dünner und die bereinigte Seite deutlich umfangreicher
// (z. B. Diskussions-/Forumseiten), gewinnt die bereinigte Seite.
const minSubstantial = 500

// minCleanLen ist die Mindestlänge eines brauchbaren Inhalts in Zeichen.
// Kürzere Extraktionen gelten als gescheitert (Cookie-Banner o. Ä.) und
// führen zu leerem Inhalt statt zu Müll.
const minCleanLen = 120

// markdownLinkRegexp matches [text](url) patterns (no newlines inside)
var markdownLinkRegexp = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)

// expandLinksInMarkdown takes a [text](url) pattern and, when the link text
// does not already contain the URL itself, appends the bare URL after the
// reference so the actual address is visible in rendered/plain-text output.
func expandLinksInMarkdown(md string) string {
	return markdownLinkRegexp.ReplaceAllStringFunc(md, func(match string) string {
		sub := markdownLinkRegexp.FindStringSubmatch(match)
		text, url := sub[1], sub[2]

		// Wenn der Link-Text schon die URL enthält (z.B. [https://example.com](https://example.com)),
		// belassen wir den Link unverändert.
		if strings.Contains(strings.ToLower(text), strings.ToLower(url)) {
			return match
		}

		// Sonst: [text](url) → [text](url) - `https://...`
		return fmt.Sprintf("%s - `%s`", match, url)
	})
}

// htmlToMarkdown konvertiert HTML-Fragmente zu Markdown.
func htmlToMarkdown(htmlFragment string) string {
	md, err := htmltomarkdown.ConvertString(htmlFragment)
	if err != nil {
		return ""
	}
	return expandLinksInMarkdown(md)
}

// normalizeSpace fasst Leerzeichen zusammen und trimmt (auch Umbrüche).
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// stripHTMLSpace entfernt überfläßige Whitespace-Textknoten (Gestöber aus
// formatiertem HTML) und gibt das HTML als String zurück.
func stripHTMLSpace(doc *goquery.Document) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t == "" {
				n.Data = ""
			} else {
				n.Data = " " + t + " "
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc.Nodes[0])
}

// ─── Titel-Extraktion ────────────────────────────────────────────────────────

// extractHTMLTitle liest den Titel einer Seite – ausschließlich aus
// <head><title>, niemals aus <svg><title> (Logos!). Fallbacks: og:title,
// erste H1-Überschrift. Der Titel wird 1:1 übernommen (nur getrimmt).
func extractHTMLTitle(doc *goquery.Document) string {
	if t := normalizeSpace(doc.Find("head > title").First().Text()); t != "" {
		return t
	}
	if t, ok := doc.Find(`meta[property="og:title"]`).First().Attr("content"); ok {
		if t = normalizeSpace(t); t != "" {
			return t
		}
	}
	if t := normalizeSpace(doc.Find("h1").First().Text()); t != "" {
		return t
	}
	return ""
}

// ─── Inhalts-Extraktion ──────────────────────────────────────────────────────

// junkSelectors sind Boilerplate-Bereiche, die nie zum Artikelinhalt gehören:
// Navigation, Footer, Werbung, Cookie-/Consent-Banner, Newsletter-Boxen,
// Formulare, Share-Buttons und SVG-Logos (deren <title>-Text stört).
var junkSelectors = []string{
	"script", "style", "noscript", "iframe", "svg", "canvas", "form", "button",
	"input", "select", "textarea", "template", "object", "embed",
	"nav", "footer", "aside",
	"[role=navigation]", "[role=banner]", "[role=contentinfo]", "[role=dialog]", "[role=alertdialog]",
	"[role=menu]", "[role=menubar]", "[role=search]", "[role=tablist]", "[role=tab]",
	"[aria-hidden=true]", "[hidden]",
	"[class*=-cookie-]", "[class*=cookie-]", "[class*=-cookie]", "[class*=cookiebanner]",
	"[id*=cookiebanner]", "[id*=cookie-banner]",
	"[class*=consent]", "[id*=consent]", "[class*=cmp-]", "[id*=cmp-]",
	"[class*=gdpr]", "[id*=gdpr]",
	"[class*=paywall]", "[id*=paywall]", "[class*=piano-]", "[class*=ticker]",
	"[class*=newsletter]", "[id*=newsletter]", "[class*=subscribe]", "[class*=signup]",
	"[class*=advert]", "[class*=ad-]", "[class*=-ad]", "[class*=ads-]",
	"[id*=advert]", "[class*=banner]", "[id*=banner]",
	"[class*=popup]", "[class*=modal]", "[class*=overlay]",
	"[class*=share]", "[class*=social]", "[class*=breadcrumb]", "[class*=pagination]",
	"[class*=related]", "[class*=recommend]",
	"[class*=sidebar]", "[id*=sidebar]", "[class*=widget]", "[class*=teaser]",
	"[class*=tags]", "[class*=meta]", "[class*=author]",
}

// stripBoilerplate entfernt Junk-Elemente aus dem Dokument (destruktiv).
func stripBoilerplate(doc *goquery.Document) {
	doc.Find(strings.Join(junkSelectors, ",")).Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})
}

// extractContent gewinnt den Hauptinhalt einer Seite als Markdown.
// Reihenfolge: Mozilla-Readability (wie Firefox-Leseansicht) → <article>/<main>
// mit Boilerplate-Bereinigung → bereinigter <body>.
func extractContent(doc *goquery.Document, pageURL string) string {
	// Gemeinsame Basis: Junk-Bereiche entfernen (Navigation, Werbung,
	// Cookie-/Consent-Banner, Newsletter, SVG-Logos …).
	clone := goquery.NewDocumentFromNode(cloneNode(doc.Nodes[0]))
	stripBoilerplate(clone)

	// 1. Readability (Mozilla-Algorithmus wie die Firefox-Leseansicht).
	art := checkClean(readabilityArticle(clone, pageURL))

	// 2. <article>/<main> mit Boilerplate-Filter.
	stripHTMLSpace(clone)
	var page string
	for _, sel := range []string{"article", "main", `[role=main]`, ".post-content", ".entry-content", "#content"} {
		if h, err := clone.Find(sel).First().Html(); err == nil && strings.TrimSpace(h) != "" {
			if md := checkClean(htmlToMarkdown(h)); md != "" {
				page = md
				break
			}
		}
	}

	// 3. Der bereinigte <body> als letzter Ausweg.
	if page == "" {
		if h, err := clone.Find("body").Html(); err == nil {
			page = checkClean(htmlToMarkdown(h))
		}
	}

	// Auswahl: Readability ist der beste Hauptinhalt, wenn sie echte Substanz
	// liefert. Ist ihr Ergebnis dünn (z. B. bei Threads: nur der Eröffnungs-
	// beitrag) und die bereinigte Seite deutlich umfangreicher, gewinnt diese.
	switch {
	case art != "" && (len([]rune(art)) >= minSubstantial || page == "" || len([]rune(page)) < 3*len([]rune(art))):
		log.Printf("[scrape] readability: %d bytes markdown", len(art))
		return art
	case page != "":
		log.Printf("[scrape] bereinigte Seite: %d bytes markdown (readability %d)", len(page), len(art))
		return page
	case art != "":
		log.Printf("[scrape] readability: %d bytes markdown", len(art))
		return art
	}
	return ""
}

// readabilityArticle wendet die Mozilla-Readability-Extraktion an und liefert
// den Artikel als Markdown ("" bei Misserfolg).
func readabilityArticle(doc *goquery.Document, pageURL string) string {
	u, err := nurl.Parse(pageURL)
	if err != nil {
		return ""
	}
	// Readability arbeitet destruktiv – deshalb auf einer Kopie.
	clone := cloneNode(doc.Nodes[0])
	art, err := readability.FromDocument(clone, u)
	if err != nil {
		return ""
	}
	return htmlToMarkdown(art.Content)
}

// cloneNode kopiert einen HTML-Knoten tief (für destruktive Verarbeitung).
func cloneNode(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}
	clone := &html.Node{
		Type:      n.Type,
		DataAtom:  n.DataAtom,
		Data:      n.Data,
		Namespace: n.Namespace,
		Attr:      append([]html.Attribute(nil), n.Attr...),
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		clone.AppendChild(cloneNode(c))
	}
	return clone
}

// checkClean gibt den Text bei ausreichender Länge zurück, sonst "".
func checkClean(md string) string {
	md = strings.TrimSpace(md)
	if contentTooLow(md) {
		return ""
	}
	return md
}

// contentTooLow prüft, ob ein Inhalt zu kurz für eine brauchbare Seite ist.
func contentTooLow(content string) bool {
	if strings.TrimSpace(content) == "" {
		return true
	}
	// Unter der Annahme, dass durchschnittliche Wörter <= 24 Bytes lang sind,
	// muss eine Seite mit Inhalt mehr als 5 Wörter enthalten.
	return len([]rune(content)) < minCleanLen
}

// extractPage extrahiert Titel und Inhalt aus rohem HTML oder XML.
func extractPage(rawHTML []byte, pageURL string) (string, string, error) {
	return extractPageLimit(rawHTML, pageURL, 0)
}

// extractPageLimit wie extractPage, begrenzt den Inhalt aber auf maxChars
// (0 = unbegrenzt). RSS-/Atom-Feeds werden strukturiert ausgewertet – HTML-
// Parsing bzw. Readability würde daraus einen unstrukturierten Textbrei machen,
// aus dem bei vielen Einträgen praktisch nur der erste übrig bleibt.
func extractPageLimit(rawHTML []byte, pageURL string, maxChars int) (string, string, error) {
	if feedRootElement(rawHTML) != "" {
		return extractFeedPage(rawHTML, pageURL, maxChars)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(rawHTML))
	if err != nil {
		return "", "", fmt.Errorf("HTML konnte nicht gelesen werden: %w", err)
	}

	title := extractHTMLTitle(doc)
	content := extractContent(doc, pageURL)
	if content == "" {
		// Kein sauberer Inhalt → bewusst leer (keine Fehlermeldung als Inhalt).
		return title, "", fmt.Errorf("kein sauberer Inhalt extrahierbar (Cookie-Banner/Paywall/leere Seite)")
	}
	return title, content, nil
}

// scrapeURL lädt eine Webseite oder einen Feed und liefert Titel (HTML-/Feed-
// Titel, 1:1) und Inhalt als Markdown. Bei Fehlern bleibt der Inhalt leer –
// niemals wird eine Fehlermeldung als Inhalt geliefert.
func (a *App) scrapeURL(rawURL string) (string, string, error) {
	rawHTML, err := a.fetchHTML(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("Seite konnte nicht geladen werden: %w", err)
	}
	return extractPageLimit(rawHTML, rawURL, a.contentLimitChars())
}

// extractYouTubePageTitle holt den Originaltitel einer YouTube-Seite
// durch einfaches Parsen des HTML <title>-Tags. Als Fallback wenn yt-dlp versagt.
func extractYouTubePageTitle(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; FoundAItionBot/1.0)")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// <title>…</title> im HTML extrahieren
	start := strings.Index(string(body), "<title>")
	end := strings.Index(string(body), "</title>")
	if start == -1 || end == -1 || end <= start {
		return "", fmt.Errorf("no <title> tag found")
	}

	title := string(body[start+7 : end])
	// HTML-Entitäten dekodieren (&quot; → ", &amp; → & etc.)
	title = stdhtml.UnescapeString(title)
	// YouTube hängt oft " - YouTube" an, das entfernen
	title = strings.TrimSuffix(title, " - YouTube")
	title = strings.TrimSpace(title)
	log.Printf("[yt-title] extracted from HTML: %q", title)
	return title, nil
}

// ─── Laden ───────────────────────────────────────────────────────────────────

// fetchHTML lädt die Roh-HTML-Seite und prüft den Statuscode.
//
// Allgemeiner Fallback: Schlägt der erste Versuch fehl (Netzwerkfehler oder
// HTTP-Fehlercode), wird mit erzwungenem HTTP/1.1 erneut versucht. Manche
// Seiten blockieren den HTTP/2-Fingerprint des Go-Stacks (z. B. HTTP 419),
// akzeptieren aber HTTP/1.1 – ganz ohne seiten-spezifische Sonderbehandlung.
func (a *App) fetchHTML(rawURL string) ([]byte, error) {
	u, err := nurl.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ungültige URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("nur http/https wird unterstützt")
	}

	body, status, err := a.httpGetHTML(rawURL, false)
	if err == nil && status < 400 {
		return body, nil
	}

	// Zweiter Versuch mit erzwungenem HTTP/1.1 (allgemeiner Netzwerk-Fallback).
	body1, status1, err1 := a.httpGetHTML(rawURL, true)
	if err1 == nil && status1 < 400 {
		return body1, nil
	}

	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("HTTP %d", status)
}

// httpGetHTML führt einen GET mit Browser-Headern aus.
// Mit forceHTTP1 wird HTTP/2 deaktiviert (TLSNextProto leer).
func (a *App) httpGetHTML(rawURL string, forceHTTP1 bool) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.cfg.CrawlTimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "de,en;q=0.7")

	tr := &http.Transport{ForceAttemptHTTP2: !forceHTTP1}
	if forceHTTP1 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}

	resp, err := (&http.Client{
		Timeout:   time.Duration(a.cfg.CrawlTimeoutSec) * time.Second,
		Transport: tr,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("zu viele Umleitungen")
			}
			return nil
		},
	}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	return body, resp.StatusCode, err
}
