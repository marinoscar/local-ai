# ADR-0014: Corona, one dark theme and a solar-eclipse identity everywhere

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** owner

## Context
Local AI had three looks: the Portal was black text on near-white ([survival-portal.md](../specs/survival-portal.md) §4.1), the ai-desktop shell pages used MUI's default light theme, and the cloud app used the platform's green. The app icon was a navy tile with an orange ring. The owner compared three dark themes and four eclipse icons on 2026-11-14 (epic #536) and chose **Corona**: gold corona on near-black, and a total solar eclipse as the icon, "because it is a natural phenomenon". A dark screen also suits the emergency case: a laptop on battery at night, in a dark room.

## Decision
1. **One theme, Corona, for every surface.** The Portal and Dashboard (`agent/ui`), the map viewer's controls, ai-desktop's shell pages and the cloud web app use the same tokens: [`packages/ui/brand/tokens.json`](../../packages/ui/brand/tokens.json). Background `#07070A`, surfaces `#111015`/`#17161D`, text `#F5EFE3`, gold `#F2B544` for the one main action of a screen and the focus ring, green, amber and red for status. Atkinson Hyperlegible stays the font.
2. **Gold is scarce.** Only the most important control of a screen glows gold (Start survival mode, Leave survival mode, Help, a confirmation). Everything else is a quiet surface, so under stress the eye finds the next step.
3. **The icon is a total solar eclipse** ([`icon.svg`](../../packages/ui/brand/icon.svg)): the moon's black disc inside a gold corona with eight faint rays. The header mark is the same eclipse without its tile ([`mark.svg`](../../packages/ui/brand/mark.svg)). The tray shows the eclipse ring in the tray's status colour ([`tray.svg`](../../packages/ui/brand/tray.svg)), so the colours of desktop.md §4.3 keep their meaning.
4. **Accessibility is not traded for looks.** Text is at least 4.5:1 on every surface it sits on (checked for the tokens), status is never colour alone (✓ ! ✗ glyphs stay), focus is a 3 px gold ring, and the three text sizes and keyboard use of survival-portal.md §4.10 are unchanged. Nothing is loaded from the network (AGENTS.md rule 3).
5. **The README** shows the banner ([`banner.png`](../../packages/ui/brand/banner.png), rendered from `banner.html` by `agent/ui/scripts/render-brand.mjs`) and screenshots of the app.

## Consequences
- The light Portal of survival-portal.md §4.1 is replaced; its wireframes and copy are unchanged.
- The cloud app opens dark by default; the platform's light theme stays available and uses the gold accent.
- PNG icons are generated, committed artifacts (`apps/desktop/scripts/icons.mjs`); change the SVG sources, never the PNGs.
