package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// srtEntryRe extrahiert SRT-Einträge aus kompaktem oder klassischem Format.
// Passt: "SeqNum HH:MM:SS,mmm --> HH:MM:SS,mmm Text"
// Gruppe 1: Start-Zeit ohne Millisekunden, Gruppe 2: Text bis Zeilenende
// srtHeaderRe passt den Kopf eines SRT-Eintrags ohne Text.
// Gruppe 1: Start-Zeit HH:MM:SS (ohne Millisekunden)
var srtHeaderRe = regexp.MustCompile(`\d+\s+(\d{2}:\d{2}:\d{2}),\d{3}\s+-->\s+\d{2}:\d{2}:\d{2},\d{3}`)

// parseWhisperResponse dekodiert eine Whisper-HTTP-Antwort.
func parseWhisperResponse(raw []byte) (string, string) {
	rawStr := strings.TrimSpace(string(raw))
	if rawStr == "" {
		return "", "empty"
	}

	// Schritt 1: Immer zuerst JSON-Parser versuchen
	var j struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(rawStr), &j); err == nil && strings.TrimSpace(j.Text) != "" {
		content := strings.TrimSpace(j.Text)

		// Schritt 2: Innerhalb des JSON-Werts auf SRT-Format pruefen
		if srtHeaderRe.MatchString(content) {
			simple := convertSRTToSimple(content)
			if simple != "" {
				return simple, "json+srt"
			}
		}

		// Kein SRT → Plain-Text aus JSON
		return content, "json+text"
	}

	// Schritt 3: Kein JSON – vielleicht ist der Body reiner SRT-Text
	if srtHeaderRe.MatchString(rawStr) {
		result := convertSRTToSimple(rawStr)
		if result != "" {
			return result, "srt"
		}
	}

	// Fallback 4: roher Plaintext
	log.Printf("[whisper] unrecognized format (%d bytes), using as plaintext", len(raw))
	return rawStr, "plaintext"
}

// convertSRTToSimple wandelt SRT-Transkripte in "HH:MM:SS Text" um.
// Verwendet FindAllStringSubmatchIndex: Header-Positionen ermitteln,
// dazwischenliegenden Text als Eintragstext extrahieren.
// Funktioniert sowohl mit kompaktem Format (alles auf einer Zeile)
// als auch klassischem Block-Format (Leerzeilen zwischen Eintraegen).
func convertSRTToSimple(srtContent string) string {
	locs := srtHeaderRe.FindAllStringSubmatchIndex(srtContent, -1)
	if len(locs) == 0 {
		return ""
	}

	var lines []string
	for i, loc := range locs {
		// Gruppe 1: HH:MM:SS (ohne Millisekunden)
		startTime := srtContent[loc[2]:loc[3]]

		// Text: nach diesem Header bis zum naechsten Header oder Ende
		textStart := loc[1] // Ende dieses Headers
		textEnd := len(srtContent)
		if i+1 < len(locs) {
			textEnd = locs[i+1][0] // Anfang des naechsten Headers
		}

		text := strings.TrimSpace(srtContent[textStart:textEnd])
		// Ruehrueber aus SRT-SeqNum oder Leerzeichen bereinigen
		text = cleanSRTRemainder(text)
		if text != "" {
			lines = append(lines, startTime+" "+text)
		}
	}

	return strings.Join(lines, "\n")
}

// cleanSRTRemainder entfernt Ruehrueber wie SRT-SeqNum-Zahlen am Anfang.
// z.B. wenn das Format ungueltig ist und eine SeqNum uebrig bleibt.
func cleanSRTRemainder(text string) string {
	text = strings.TrimSpace(text)
	// Wenn mit "Zahl " beginnt, entfernen
	if m := regexp.MustCompile(`^\d+\s+(.*)`).FindStringSubmatch(text); len(m) == 2 && !isTimestampLike(m[1]) {
		text = strings.TrimSpace(m[1])
	}
	return text
}

// isTimestampLike prueft, ob ein String mit HH:MM beginnt.
func isTimestampLike(s string) bool {
	return len(s) >= 5 && s[2] == ':' && s[5] == ':'
}

// ─── OpenAI API – Zusammenfassung generieren ─────────────────────────────────

// ErrTooLong ist der spezielle Fehler, wenn der Inhalt das Token-Limit überschreitet.
const ErrTooLong = "zu langer Text – Inhalt überschreitet konfiguriertes Token-Limit"

// generateSummary sendet Inhalt an die OpenAI-API (oder kompatible API)
// und gibt die generierte Zusammenfassung zurueck.
func (a *App) generateSummary(content, promptName, lang string) (string, error) {
	return a.generateSummaryWithModel(content, promptName, lang, a.cfg.OpenAIModel)
}

func (a *App) generateSummaryWithModel(content, promptName, lang, model string) (string, error) {
	// Token-Limit prüfen (nicht für Kategorisierung, die nutzt kurze Summaries)
	if promptName != "categorize" && a.cfg.exceedsMaxTokens(content) {
		return "", fmt.Errorf(ErrTooLong)
	}

	if model == "" {
		model = a.cfg.OpenAIModel
	}

	var systemPrompt string
	if lang == "en" {
		systemPrompt = "You are a precise editor."
		if promptName == "chapters" {
			systemPrompt = "You are an experienced video analyst."
		} else if promptName == "categorize" {
			systemPrompt = "You are a precise categorizer. Respond with exactly one category."
		}
	} else {
		systemPrompt = "Du bist ein praeziser Redakteur."
		if promptName == "chapters" {
			systemPrompt = "Du bist ein erfahrener Video-Analyst."
		} else if promptName == "categorize" {
			systemPrompt = "Du bist ein praeziser Kategorisierer. Antworte mit exakt einer Kategorie."
		}
	}

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": a.renderPrompt(promptName, content, lang)},
	}

	maxOut := a.cfg.summaryMaxOutputTokens()
	// Kategorisierung liefert ein Wort – eine Fortsetzung wäre dort sinnlos.
	maxRounds := maxSummaryContinuations
	if promptName == "categorize" {
		maxRounds = 0
	}

	var full strings.Builder
	truncated := false

	for round := 0; ; round++ {
		answer, finish, err := a.chatCompletion(messages, model, maxOut)
		if err != nil {
			// Bereits erzeugten Text nicht verwerfen.
			if full.Len() == 0 {
				return "", err
			}
			log.Printf("[llm] continuation failed after %d chars: %v", full.Len(), err)
			break // truncated bleibt der Stand der letzten erfolgreichen Runde
		}
		full.WriteString(answer)

		// truncated spiegelt immer die letzte erfolgreiche Antwort wider.
		truncated = finish == finishReasonLength
		if !truncated {
			break
		}

		if strings.TrimSpace(answer) == "" {
			// Nichts, woran sich anknüpfen ließe – z. B. wenn ein Denk-Modell das
			// gesamte Ausgabelimit für Reasoning verbraucht hat.
			log.Printf("[llm] WARN: abgeschnittene Antwort ohne Text – SUMMARY_MAX_OUTPUT_TOKENS=%d zu klein (Prompt %q)", maxOut, promptName)
			break
		}
		if round >= maxRounds {
			log.Printf("[llm] WARN: Antwort weiterhin abgeschnitten – SUMMARY_MAX_OUTPUT_TOKENS=%d (Prompt %q, %d Runden)",
				maxOut, promptName, round+1)
			break
		}

		// Das Modell hat das Ausgabelimit erreicht: nahtlos fortsetzen lassen.
		log.Printf("[llm] Ausgabelimit (%d Token) erreicht – Fortsetzung %d/%d", maxOut, round+1, maxRounds)
		messages = append(messages,
			map[string]string{"role": "assistant", "content": answer},
			map[string]string{"role": "user", "content": continuationPrompt(lang)},
		)
	}

	result := strings.TrimSpace(full.String())
	if result == "" && truncated {
		return "", fmt.Errorf("Modell lieferte keine Ausgabe – Ausgabelimit (%d Token) zu klein? SUMMARY_MAX_OUTPUT_TOKENS erhöhen", maxOut)
	}
	if truncated && promptName != "categorize" {
		result += truncationNotice(lang, maxOut)
	}
	return result, nil
}

// finishReasonLength meldet, dass das Modell das Ausgabelimit erreicht hat und
// die Antwort deshalb unvollständig ist.
const finishReasonLength = "length"

// maxSummaryContinuations begrenzt, wie oft eine abgeschnittene Antwort
// fortgesetzt wird (jede Runde ist ein weiterer LLM-Aufruf).
const maxSummaryContinuations = 3

// continuationPrompt fordert das Modell auf, genau an der Abbruchstelle
// weiterzuschreiben.
func continuationPrompt(lang string) string {
	if lang == "en" {
		return "Your previous answer was cut off by the output token limit. Continue it seamlessly at exactly the point where it ends – no preamble, no repetition, no summary of what came before."
	}
	return "Deine vorherige Antwort wurde durch das Ausgabe-Token-Limit abgeschnitten. Setze sie nahtlos genau an der Abbruchstelle fort – ohne Einleitung, ohne Wiederholung und ohne Zusammenfassung des bisherigen Textes."
}

// truncationNotice kennzeichnet eine Zusammenfassung, die trotz Fortsetzungen
// unvollständig geblieben ist (statt sie stillschweigend abzuschneiden).
func truncationNotice(lang string, maxOut int) string {
	if lang == "en" {
		return fmt.Sprintf("\n\n---\n*⚠️ Summary truncated – output limit reached (%d tokens per response). Increase `SUMMARY_MAX_OUTPUT_TOKENS` and regenerate.*", maxOut)
	}
	return fmt.Sprintf("\n\n---\n*⚠️ Zusammenfassung abgeschnitten – Ausgabelimit erreicht (%d Token pro Antwort). `SUMMARY_MAX_OUTPUT_TOKENS` erhöhen und neu erstellen lassen.*", maxOut)
}

// chatCompletion sendet eine Chat-Anfrage und liefert Antworttext sowie
// finish_reason ("stop", "length", …).
func (a *App) chatCompletion(messages []map[string]string, model string, maxTokens int) (string, string, error) {
	payload := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.3,
		"max_tokens":  maxTokens,
	}

	body, _ := json.Marshal(payload)

	// Mindestens MinLLMTimeoutSec: das Laden eines lokalen Modells kann dauern.
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.llmTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.cfg.OpenAIBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("create openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.OpenAIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("openai returned %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode openai response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", "", fmt.Errorf("empty openai response")
	}
	// Kein TrimSpace: bei einer Fortsetzung muss die Antwort nahtlos an den
	// vorherigen Teil anschließen können (auch mitten im Wort).
	return result.Choices[0].Message.Content, result.Choices[0].FinishReason, nil
}

// ─── Whisper – Audio-Transkription ───────────────────────────────────────────

// transcribeWithWhisper ruft die lokale Whisper-HTTP-API auf,
// um eine Audiodatei zu transkribieren. Request gibt SRT zurueck.
func (a *App) transcribeWithWhisper(audioFile string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Multipart/form-data fuer Datei-Upload erstellen
	body := &bytes.Buffer{}
	writer := multipartFormWriter(body)

	fw, err := writer.CreateFormFile("file", filepath.Base(audioFile))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	f, err := os.Open(audioFile)
	if err != nil {
		return "", fmt.Errorf("open audio file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(fw, f); err != nil {
		return "", fmt.Errorf("copy file: %w", err)
	}

	writer.WriteField("model", a.cfg.WhisperModel)
	writer.WriteField("language", "de")
	// Lokaler Whisper-Server erwartet srt_format="true" als Form-Field
	// (nicht die Standard-OpenAI response_format-Konvention)
	writer.WriteField("srt_format", "true")

	if err = writer.Close(); err != nil {
		return "", fmt.Errorf("close form writer: %w", err)
	}

	url := a.cfg.WhisperURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", fmt.Errorf("create whisper request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if a.cfg.WhisperAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.WhisperAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("whisper returned %d: %s", resp.StatusCode, string(b))
	}

	// Body einlesen
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read whisper response body: %w", err)
	}

	transcript, format := parseWhisperResponse(raw)
	log.Printf("[whisper] parsed via %s (%d chars)", format, len(transcript))
	return transcript, nil
}

// ─── Multipart-Form-Writer (eigenstaendig, ohne net/textproto) ──────────────

// multipartFormWriter erstellt einen einfachen multipart/form-data Writer.
func multipartFormWriter(buf *bytes.Buffer) *multipartWriter {
	return &multipartWriter{
		writer:   bufio.NewWriter(buf),
		boundary: randomBoundary(),
	}
}

// multipartWriter ist ein einfacher multipart/form-data Writer fuer Datei-Uploads.
type multipartWriter struct {
	writer     *bufio.Writer
	boundary   string
	firstField bool
}

// FormDataContentType gibt den Content-Type Header zurueck.
func (w *multipartWriter) FormDataContentType() string {
	return "multipart/form-data; boundary=" + w.boundary
}

// Write implementiert io.Writer – delegiert an den internen bufio.Writer.
func (w *multipartWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

// CreateFormFile erstellt einen neuen Datei-Teil im multipart-Body.
func (w *multipartWriter) CreateFormFile(fieldname, filename string) (io.Writer, error) {
	if !w.firstField {
		w.writer.WriteString("\r\n")
	}
	w.firstField = false
	w.writer.WriteString("--" + w.boundary + "\r\n")
	w.writer.WriteString(fmt.Sprintf(
		"Content-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n", fieldname, filename))
	w.writer.WriteString("Content-Type: application/octet-stream\r\n\r\n")
	return w, nil
}

// WriteField fuegt ein einfaches Formularfeld hinzu.
func (w *multipartWriter) WriteField(fieldname, value string) {
	if !w.firstField {
		w.writer.WriteString("\r\n")
	}
	w.firstField = false
	w.writer.WriteString("--" + w.boundary + "\r\n")
	w.writer.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s", fieldname, value))
}

// Close schreibt den abschliessenden Boundary und spuert den Buffer.
func (w *multipartWriter) Close() error {
	w.writer.WriteString("\r\n--" + w.boundary + "--\r\n")
	return w.writer.Flush()
}

// randomBoundary erzeugt eine zufaellige Boundary-Zeichenkette fuer multipart-Requests.
func randomBoundary() string {
	return fmt.Sprintf("----GoMultipartBoundary%x", time.Now().UnixNano())
}
