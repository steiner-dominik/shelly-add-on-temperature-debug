# Adding a translation

All user-facing text — UI labels, status names, and the troubleshooting
guidance — lives in one JSON file per language in [`locales/`](../locales).
The server embeds these files at build time and the page offers every present
language in its picker automatically. **Adding a language is one file + one
pull request; no Go or JavaScript knowledge needed.**

## Steps

1. Copy [`locales/en.json`](../locales/en.json) to `locales/<code>.json`,
   where `<code>` is the two-letter [ISO 639-1](https://en.wikipedia.org/wiki/List_of_ISO_639-1_codes)
   code (e.g. `fr.json`, `nl.json`, `it.json`).
2. Set the header:
   ```json
   "meta": { "code": "fr", "name": "Français" }
   ```
   `code` must match the filename; `name` is the language's **own** name — it
   is what users see in the picker.
3. Translate every value. Keep:
   - the **keys** unchanged,
   - placeholders like `{time}`, `{n}`, `{err}`, `{rssi}` intact (they are
     substituted at runtime),
   - technical terms (`GND / DATA / 3V3`, `temperature:100`, env var names)
     as they are.
4. Open a pull request. CI validates all locale files (`go test` fails on
   malformed JSON or a `meta` mismatch), and the next release ships your
   language automatically.

## Which language is shown?

On first visit the page picks the browser's language if a matching locale
exists, otherwise English. The user's manual choice is remembered in the
browser (localStorage).
