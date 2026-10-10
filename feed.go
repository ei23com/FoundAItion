package main

// RSS-/Atom-Feeds: Erkennung und strukturierte Auswertung.
//
// Feeds sind XML, kein HTML. Werden sie durch den HTML-/Readability-Pfad
// geschickt, entsteht ein unstrukturierter Textbrei: die CDATA-Blöcke landen
// mit `]]&gt;`-Resten im Text, Bild-URLs (Vorschaubilder) blähen den Inhalt auf
// und die Grenzen zwischen den Einträgen verschwinden – mit `nb=500` bleibt am
// Ende kaum mehr als der erste Eintrag übrig.
//
// Deshalb ein eigener, generischer Feed-Pfad (keine seiten-spezifische
// Sonderbehandlung, nur Format-Erkennung):
//
//  1. Erkennung über das Wurzelelement: <rss> (RSS 2.0), <feed> (Atom),
//     <rdf:RDF> (RSS 1.0) – unabhängig von Content-Type und Host.
//  2. Auswertung pro Eintrag: Datum, Titel, Link, Autor, Kategorie sowie
//     description/summary und – falls deutlich umfangreicher – content.
//  3. Ausgabe als kompaktes, YAML-artiges Markdown (Überschrift je Eintrag,
//     `- schlüssel: wert`-Zeilen, darunter der Text). Das spart gegenüber dem
//     HTML-Pfad den größten Teil der Zeichen und bleibt trotzdem "roh".

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"
)

// feedRootNames sind die möglichen Wurzelelemente eines Feeds.
var feedRootNames = map[string]bool{"rss": true, "feed": true, "rdf": true}

// feedSniffBytes begrenzt die Vorprüfung auf den Dokumentanfang – ein Feed hat
// sein Wurzelelement immer am Anfang, HTML-Seiten werden so gar nicht erst
// XML-dekodiert.
const feedSniffBytes = 4096

// htmlTagRegexp erkennt, ob ein Feed-Textfeld HTML enthält (oder nur Text ist).
// Verlangt ein vollständiges Tag, damit reiner Text mit "<" nicht als HTML
// missverstanden wird.
var htmlTagRegexp = regexp.MustCompile(`(?is)<\s*/?\s*(a|p|div|span|br|table|tr|td|img|ul|ol|li|h[1-6]|blockquote|strong|em|b|i|code|pre|figure|font|small)(\s[^<>]*)?/?>`)

// feedJunkSelectors sind Elemente, die in Feed-Beschreibungen nur Ballast sind:
// Bilder/Vorschaubilder (riesige URLs), Videos, Skripte, Formulare.
const feedJunkSelectors = "script,style,noscript,iframe,svg,canvas,video,audio,picture,source,img,form,button,input,select,textarea,object,embed"

// feedExtraSelectors sind zusätzliche Elemente, deren Attribute die Bild-URL
// enthalten (Media-RSS, Enclosures) – nur für den Notfall, wenn ein Eintrag
// keinerlei Text hat.
var feedExtraSelectors = []string{"thumbnail", "content", "enclosure", "group"}

// ─── Erkennung ───────────────────────────────────────────────────────────────

// feedRootElement liefert das Wurzelelement des Dokuments ("rss", "feed",
// "rdf") oder "" wenn es kein Feed ist.
func feedRootElement(raw []byte) string {
	head := raw
	if len(head) > feedSniffBytes {
		head = head[:feedSniffBytes]
	}
	low := strings.ToLower(string(head))
	if !strings.Contains(low, "<rss") && !strings.Contains(low, "<feed") && !strings.Contains(low, "<rdf") {
		return ""
	}

	dec := newFeedDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			name := strings.ToLower(se.Name.Local)
			if feedRootNames[name] {
				return name
			}
			return ""
		}
	}
}

// newFeedDecoder baut einen toleranten XML-Decoder: Feeds sind oft nicht
// wohlgeformt (unmaskierte &-Zeichen) und tragen manchmal einen anderen
// Zeichensatz als UTF-8 (z. B. ISO-8859-1).
func newFeedDecoder(r io.Reader) *xml.Decoder {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(strings.TrimSpace(label), "utf-8") {
			return input, nil
		}
		return charset.NewReaderLabel(label, input)
	}
	return dec
}

// ─── XML-Baum ────────────────────────────────────────────────────────────────

// feedNode ist ein generischer XML-Knoten. Namespace-Präfixe werden verworfen,
// verglichen wird über den lokalen Namen in Kleinschreibung.
type feedNode struct {
	Name     string
	Attrs    []xml.Attr
	Text     string
	Children []*feedNode
}

// parseFeedTree liest das XML in einen Baum. Bricht das Dokument mittendrin ab
// (abgeschnittene Feeds), wird der bereits gelesene Teil verwendet.
func parseFeedTree(raw []byte) (*feedNode, error) {
	dec := newFeedDecoder(bytes.NewReader(raw))
	var root *feedNode
	var stack []*feedNode

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if root != nil {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &feedNode{Name: t.Name.Local, Attrs: t.Attr}
			if len(stack) == 0 {
				if root == nil {
					root = n
				}
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}

	if root == nil {
		return nil, fmt.Errorf("kein XML-Wurzelelement gefunden")
	}
	return root, nil
}

// children liefert alle direkten Kindknoten mit diesem lokalen Namen.
func (n *feedNode) children(name string) []*feedNode {
	if n == nil {
		return nil
	}
	var out []*feedNode
	for _, c := range n.Children {
		if strings.EqualFold(c.Name, name) {
			out = append(out, c)
		}
	}
	return out
}

// child liefert den ersten direkten Kindknoten mit diesem lokalen Namen.
func (n *feedNode) child(name string) *feedNode {
	if kids := n.children(name); len(kids) > 0 {
		return kids[0]
	}
	return nil
}

// firstChild liefert den ersten Treffer aus mehreren Namen (Reihenfolge zählt).
func (n *feedNode) firstChild(names ...string) *feedNode {
	for _, name := range names {
		if c := n.child(name); c != nil {
			return c
		}
	}
	return nil
}

// firstChildText liefert den Text des ersten passenden Kindknotens.
func (n *feedNode) firstChildText(names ...string) string {
	for _, name := range names {
		if c := n.child(name); c != nil {
			if t := c.text(); t != "" {
				return t
			}
		}
	}
	return ""
}

// attr liefert ein Attribut (Vergleich über den lokalen Namen).
func (n *feedNode) attr(name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// text liefert den Text eines Knotens rekursiv, Whitespace zusammengezogen.
func (n *feedNode) text() string {
	if n == nil {
		return ""
	}
	if len(n.Children) == 0 {
		return normalizeSpace(n.Text)
	}
	parts := make([]string, 0, len(n.Children)+1)
	if t := normalizeSpace(n.Text); t != "" {
		parts = append(parts, t)
	}
	for _, c := range n.Children {
		if t := c.text(); t != "" {
			parts = append(parts, t)
		}
	}
	return normalizeSpace(strings.Join(parts, " "))
}

// descendants liefert alle Knoten mit diesem Namen, unabhängig von der Tiefe.
func (n *feedNode) descendants(name string) []*feedNode {
	var out []*feedNode
	var walk func(*feedNode)
	walk = func(cur *feedNode) {
		for _, c := range cur.Children {
			if strings.EqualFold(c.Name, name) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

// ─── Auswertung ──────────────────────────────────────────────────────────────

// feedItem ist ein ausgelesener Eintrag.
type feedItem struct {
	Title      string
	Link       string
	Date       string
	Author     string
	Categories []string
	Summary    string
	Content    string
	Image      string
}

// feedData ist ein ausgelesener Feed.
type feedData struct {
	Title       string
	Link        string
	Description string
	Updated     string
	Items       []feedItem
}

// extractFeed wertet ein Feed-Dokument aus.
func extractFeed(raw []byte, feedURL string) (*feedData, error) {
	root, err := parseFeedTree(raw)
	if err != nil {
		return nil, err
	}

	meta := root
	if !strings.EqualFold(root.Name, "feed") {
		if ch := root.child("channel"); ch != nil {
			meta = ch
		}
	}

	data := &feedData{
		Title:       meta.firstChildText("title"),
		Link:        feedLink(meta),
		Description: meta.firstChildText("description", "subtitle", "info"),
		Updated:     meta.firstChildText("lastBuildDate", "pubDate", "updated", "date", "modified"),
	}
	// Einträge: <item> (RSS 1.0/2.0) bzw. <entry> (Atom), egal in welcher Tiefe.
	var nodes []*feedNode
	var collect func(*feedNode)
	collect = func(cur *feedNode) {
		for _, c := range cur.Children {
			if strings.EqualFold(c.Name, "item") || strings.EqualFold(c.Name, "entry") {
				nodes = append(nodes, c)
				continue // nicht in Einträge hineinspringen
			}
			collect(c)
		}
	}
	collect(root)

	for _, n := range nodes {
		item := feedItem{
			Title:      n.firstChildText("title"),
			Link:       feedLink(n),
			Date:       formatFeedDate(n.firstChildText("pubDate", "published", "updated", "date", "issued", "created")),
			Author:     n.firstChildText("creator", "author", "credit"),
			Categories: feedCategories(n),
		}
		summary := feedHTMLText(n.firstChildText("description", "summary"))
		content := feedHTMLText(n.firstChildText("encoded", "content"))
		switch {
		case summary == "":
			summary, content = content, ""
		case content != "" && feedTextDuplicate(summary, content):
			// content:encoded liefert oft nur denselben Text nochmal.
			content = ""
		}
		item.Summary, item.Content = summary, content
		if summary == "" && content == "" {
			// Eintrag ohne Text (z. B. reiner Bild-Post): wenigstens die URL.
			item.Image = feedImage(n)
		}
		data.Items = append(data.Items, item)
	}

	if data.Link == "" {
		data.Link = feedURL
	}
	return data, nil
}

// feedLink liest den Link eines Knotens: <link>Text</link> (RSS) oder
// <link href="..."/> (Atom). Selbst-/Enclosure-Links werden übersprungen.
func feedLink(n *feedNode) string {
	if n == nil {
		return ""
	}
	for _, c := range n.children("link") {
		rel := strings.ToLower(c.attr("rel"))
		if rel != "" && rel != "alternate" {
			continue
		}
		if t := c.text(); t != "" {
			return t
		}
		if h := c.attr("href"); h != "" {
			return h
		}
	}
	// Fallback: erster Link egal welcher Art.
	for _, c := range n.children("link") {
		if t := c.text(); t != "" {
			return t
		}
		if h := c.attr("href"); h != "" {
			return h
		}
	}
	if id := n.child("id"); id != nil && strings.HasPrefix(id.text(), "http") {
		return id.text()
	}
	return ""
}

// feedCategories sammelt alle Kategorien (Text bei RSS, term/label bei Atom).
func feedCategories(n *feedNode) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range n.children("category") {
		value := c.text()
		if value == "" {
			value = c.attr("term")
		}
		if value == "" {
			value = c.attr("label")
		}
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// feedImage sucht eine Bild-URL (Media-RSS-Thumbnail, Enclosure o. Ä.).
func feedImage(n *feedNode) string {
	for _, name := range feedExtraSelectors {
		for _, c := range n.descendants(name) {
			kind := strings.ToLower(c.attr("type") + " " + c.attr("medium"))
			if kind != "" && !strings.Contains(kind, "image") {
				continue
			}
			if u := c.attr("url"); u != "" {
				return u
			}
		}
	}
	return ""
}

// feedTextDuplicate meldet, ob zwei Textfelder im Wesentlichen denselben
// Inhalt haben: identisch oder das kürzere steckt (zu ≥ 80 %) im längeren.
func feedTextDuplicate(a, b string) bool {
	na := normalizeSpace(strings.ToLower(a))
	nb := normalizeSpace(strings.ToLower(b))
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	if len(na) < len(nb) {
		na, nb = nb, na
	}
	return strings.Contains(na, nb) && len(nb)*10 > len(na)*8
}

// feedDateLayouts sind die üblichen Datumsformate in Feeds (RFC 822/1123 in
// verschiedenen Schreibweisen sowie ISO-8601).
var feedDateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700", "2 Jan 2006 15:04:05 -0700",
	time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05", "2006-01-02",
}

// formatFeedDate normalisiert ein Feed-Datum auf "2006-01-02 15:04".
// Unbekannte Formate bleiben unverändert (roh) erhalten.
func formatFeedDate(raw string) string {
	raw = normalizeSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range feedDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	return raw
}

// compactMarkdown entfernt Leerzeilen und Einrückungen aus konvertiertem
// Markdown – Zeichen sparen, Struktur (Listen, Links) bleibt erhalten.
func compactMarkdown(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// feedHTMLText bereitet ein Textfeld eines Eintrags auf: enthält es HTML, wird
// es zu kompaktem Markdown konvertiert (ohne Bilder, ohne doppelte Link-URLs),
// reiner Text bleibt roh.
func feedHTMLText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !htmlTagRegexp.MatchString(s) {
		return normalizeSpace(s)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(s))
	if err != nil {
		return normalizeSpace(s)
	}
	doc.Find(feedJunkSelectors).Remove()
	// Leere Links (z. B. ein Link, der nur ein Vorschaubild enthielt) entfernen.
	doc.Find("a").FilterFunction(func(_ int, sel *goquery.Selection) bool {
		return strings.TrimSpace(sel.Text()) == ""
	}).Remove()

	var body string
	if sel := doc.Find("body"); sel.Length() > 0 {
		body, _ = sel.Html()
	}
	md, err := htmltomarkdown.ConvertString(body)
	if err != nil {
		return normalizeSpace(s)
	}
	return compactMarkdown(md)
}

// ─── Ausgabe ─────────────────────────────────────────────────────────────────

// renderFeedItem gibt einen Eintrag als kompakten Markdown-Block aus:
// Überschrift mit dem Titel, darunter `- schlüssel: wert`-Zeilen (YAML-artig)
// und anschließend der Text des Eintrags.
func renderFeedItem(it feedItem) string {
	var b strings.Builder

	title := it.Title
	if title == "" {
		title = "(ohne Titel)"
	}
	b.WriteString("## " + title + "\n\n")

	if it.Date != "" {
		b.WriteString("- date: " + it.Date + "\n")
	}
	if it.Link != "" {
		b.WriteString("- link: " + it.Link + "\n")
	}
	if it.Author != "" {
		b.WriteString("- author: " + it.Author + "\n")
	}
	if len(it.Categories) > 0 {
		b.WriteString("- category: " + strings.Join(it.Categories, ", ") + "\n")
	}
	if it.Image != "" {
		b.WriteString("- image: " + it.Image + "\n")
	}

	label := it.Summary != "" && it.Content != ""
	if it.Summary != "" {
		b.WriteString("\n")
		if label {
			b.WriteString("description:\n")
		}
		b.WriteString(it.Summary + "\n")
	}
	if it.Content != "" {
		b.WriteString("\n")
		if label {
			b.WriteString("content:\n")
		}
		b.WriteString(it.Content + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderFeed baut den Gesamtinhalt eines Feeds. maxChars begrenzt die Ausgabe
// (0 = unbegrenzt): weitere Einträge werden ausgelassen und gezählt, damit die
// Ausgabe nicht mitten in einem Eintrag abgeschnitten wird.
func renderFeed(f *feedData, maxChars int) string {
	var b strings.Builder

	if f.Title != "" {
		b.WriteString("feed: " + f.Title + "\n")
	}
	if f.Link != "" {
		b.WriteString("link: " + f.Link + "\n")
	}
	if f.Description != "" {
		b.WriteString("description: " + f.Description + "\n")
	}
	if f.Updated != "" {
		b.WriteString("updated: " + formatFeedDate(f.Updated) + "\n")
	}
	fmt.Fprintf(&b, "items: %d\n\n", len(f.Items))

	for i, it := range f.Items {
		block := renderFeedItem(it)
		if maxChars > 0 && i > 0 && b.Len()+len(block) > maxChars {
			fmt.Fprintf(&b, "… %d weitere Einträge ausgelassen (Zeichenlimit)\n", len(f.Items)-i)
			break
		}
		b.WriteString(block)
	}
	return strings.TrimSpace(b.String())
}

// extractFeedPage liefert Titel (Feed-Titel) und strukturierten Inhalt eines
// Feeds. Fehler nur, wenn gar keine Einträge auswertbar sind – dann bleibt der
// Inhalt leer (nie eine Fehlermeldung als Inhalt).
func extractFeedPage(raw []byte, feedURL string, maxChars int) (string, string, error) {
	data, err := extractFeed(raw, feedURL)
	if err != nil {
		return "", "", fmt.Errorf("Feed konnte nicht gelesen werden: %w", err)
	}
	if len(data.Items) == 0 {
		return data.Title, "", fmt.Errorf("Feed enthält keine Einträge")
	}

	content := renderFeed(data, maxChars)
	log.Printf("[scrape] feed: %d Einträge, %d Zeichen", len(data.Items), len(content))
	return data.Title, content, nil
}
