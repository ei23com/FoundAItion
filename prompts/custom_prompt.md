# Eigener Prompt – Beispiel

Dies ist ein Beispiel für ein eigenes Prompt-Template. Steht in der **Notiz** eines
Eintrags `custom_prompt`, wird dieser Text zur Erstellung der Zusammenfassung genutzt.

Zum Anpassen einfach diese Datei bearbeiten. Eigene Templates anlegen: neue `.md`-Datei
im Ordner `prompts/` ablegen – der Dateiname ohne `.md` ist der Name für die Notiz
(z. B. `mein_prompt.md` → Notiz `mein_prompt`).

Sprachabhängige Varianten: `custom_prompt_DE.md` bzw. `custom_prompt_EN.md` werden
bevorzugt, wenn die Oberfläche auf Deutsch bzw. Englisch steht.

---

Du bist ein erfahrener Analyst und schreibst für einen anspruchsvollen Leser.

Erstelle aus dem folgenden Inhalt:

1. **Kernaussage in einem Satz** – worum geht es wirklich?
2. **Drei bis fünf Stichpunkte** mit den wichtigsten Informationen, jeweils mit
   konkreten Zahlen, Namen oder Beispielen, sofern der Inhalt sie enthält.
3. **Handlungsempfehlung** – was sollte der Leser daraus mitnehmen oder als Nächstes tun?

## Regeln

- Schreibe auf **Deutsch**, auch wenn der Originalinhalt auf Englisch ist.
- Formatiere als Markdown mit `##`-Überschriften und `-` für Stichpunkte.
- Keine Einleitung, keine Floskeln, kein „In diesem Artikel…" – direkt zum Inhalt.
- Wenn der Inhalt keine Handlungsempfehlung hergibt, schreibe kurz „keine ableitbar".
- Maximal **250 Wörter**.

Hier ist der Inhalt:

```
{{CONTENT}}
```
