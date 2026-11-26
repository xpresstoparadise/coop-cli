# Backlog

Ideas and requests not yet scheduled. Move an item into
[design.md](design.md) when it is decided.

## Terminal look and feel

Done in v0.2.0 (`internal/ui` on Lip Gloss; `coop watch` is a small raw-mode loop — Bubble Tea v1 was dropped because its init() queries the terminal, which hangs or eats input where the terminal does not answer): styled cards,
icons, turn bars, banners, spinners, live view with typing indicator and
keys. Plain text stays for `--agent`, MCP, pipes, `NO_COLOR` and
`COOP_PLAIN=1`. Still open:

- Check legacy Windows console (conhost) without Windows Terminal: emoji
  may show as boxes; consider an ASCII icon set there.
- Styled `coop setup` and `coop export` summaries.

## Found during testing

- (fixed in v0.2.0) `Slugify` dropped Cyrillic; it now transliterates.
- (fixed in v0.2.0) the `unread_human_message` hint was lost.
- (fixed in v0.2.0) `empty message` now says how to give the text.
