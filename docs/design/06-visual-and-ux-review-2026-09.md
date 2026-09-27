# Visual design + UX review — whole app (2026-09-27)

**Method.** Three passes, merged and cross-checked:

1. **Live pass.** I drove the real app in a Chrome DevTools-controlled browser as a user, mostly with keyboard and mouse. I used script only to take measurements, plus one synthetic ruler scrub.
   - Setup: a fresh database and the local dev stack, with mock FLUX, Seedance, ElevenLabs and image/video models.
   - Viewports: 1440×800 and 1280×800.
   - Flow: created a two-scene story, generated image takes and chained video takes, assembled them into a timeline, and opened a canvas, a character, Library and Jobs.
2. **Visual-system code audit** of `styles.css` and every `.tsx` file. Contrast ratios were computed, not estimated.
3. **UX code audit** of the story and video surfaces that the July review left out.

**Tags.**
- **[live]**: reproduced in the browser.
- **[measured]**: a computed style, geometry or contrast value read from the running page.
- **[code]**: confirmed by reading the source (file:line given).

I spot-checked the code-audit claims I relied on before including them. Screenshots are in `.scratch/ux2/` (gitignored).

**Scope.** Two parts: the visual design system across every surface, and the UX of the surfaces [05](05-ux-review-2026-07-shell-and-image.md) skipped (Story board, Scene page, Take Picker, Timeline, Characters, Library, Jobs). Nothing fixed or deliberately deferred in 05 is repeated, except that I propose widening the deferred rail-glyph redesign (see 11).

**Verdict.** The interaction model is well ahead of the visual layer. The full chain works end to end: story → shots → chained takes → timeline → export. `styles.css:1` still describes itself as the "M1 shell styling — placeholder until the design system package lands", and it shows:
- only 9 tokens;
- no type, spacing or icon system;
- emoji used as icons;
- native controls left unstyled.

This goes beyond looks: several items below are real UX bugs that come straight from the missing foundation. Buttons that are disabled look enabled. The primary button fails AA contrast. Every button renders in Arial. The ⌘K palette jumps while you type.

The good news is that inline styles are layout-only (no inline colors anywhere), so one "visual foundation" PR can fix about a dozen findings at once (see *Suggested order*).

The two P0s are not visual:
- **The timeline can't hold a one-minute cut.** That blocks the M7 W5 exit.
- **The default generation model is effectively random.** Once real keys are in, that spends money.

---

## What already works well (keep these)

- **Continuity carry is automatic and legible** [live]. Opening Generate on Shot 2 pre-checks "⛓ Continue from Shot 1's frame". The board then draws the ⛓ connector and a "⛓ Shot 1" chip, and the chain inspector explains fresh vs stale in plain words.
- **"⚡ Generate into slot" in the timeline preview** [live]. When the playhead crosses an empty shot, the preview offers to generate it. This is the animatic idea from §3.4, and it works.
- **Take Picker keyboard support and Esc behavior** [live]. 1–9, arrows, Enter and Esc all work. Esc closes only the topmost overlay: it closed the player and left the picker open behind it.
- **Shortcut sheet** [live]. It is complete and grouped by surface (Global, Timeline, Clip player, Take picker, Canvas).
- **Jobs cards link to their target** [live]. For example "→ Diner — night · Mara at the counter", shown with a strip of takes.
- **Solid baseline** [measured].
  - Lighthouse accessibility scores 100 on the Story board.
  - Body text, dim text, accent links and error text all pass AA on every surface color (e.g. dim text on raised is 6.21:1).
  - The remaining accessibility gaps are semantic ones that automated checks can't see (see P2).
- **No inline colors** [code]. All 30 `style={{}}` uses are layout-only, so the visual layer is centralized.

---

## P0 — fix before image dogfooding / the W5 exit

1. **The timeline has no scroll area or zoom of its own, so the whole page scrolls sideways.** [live + measured + code]
   - Scale is fixed at 40 px/s (`TimelinePage.tsx:29`). The ruler and tracks are sized to the full duration (`:968`, `:987`), and it is `.main` that scrolls (`styles.css:51`).
   - At 1280px, even the empty-ish 15s test cut overflows. `.main` scrollWidth is 1228 against a clientWidth of 1112. Scrolling it moves the **toolbar and preview** off-screen along with the tracks (`.scratch/ux2/20-timeline-1280-hscroll.jpeg`).
   - A one-minute W5 piece is 2,400px wide. Track labels scroll away, the playhead isn't followed during playback, and there is no zoom ("zoom lands later", `:31`).
   - **Fix:** give ruler and tracks their own horizontal scroll area with sticky track headers. Add zoom (⌘= / ⌘− / fit) and auto-follow of the playhead.

2. **The default generation model is effectively random.** [live + code]
   - Observed: I opened Generate three times without choosing a model and got Mock Image, then Seedance, then FLUX. The dropdown order also changed between opens.
   - Cause: `Registry.List` iterates a Go map with no sort (`backend/internal/registry/registry.go:206-216`), and the panel falls back to `healthy[0]` (`GeneratePanel.tsx:148`).
   - The "remembered" choice is one shared localStorage key (`iris.generate.endpoint`, `:143`). It is not per modality or per target.
   - With real BFL and Seedance keys, an untouched panel can send a paid job to a model the user never picked.
   - **Fix:** sort server-side in a stable rank (in-house → self-hosted → API, then by name). Group the select per §3.6 (Iris models · Your endpoints · API models). Remember the choice per target kind and modality.

3. **Timeline save and export can lose or misrender edits.** [code]
   - The canvas save-recovery work from PR 43 was never ported. A failed save shows only "save failed — ops kept locally" (`TimelinePage.tsx:878`): no Retry button, no confirmation on leave, no `beforeunload` guard.
   - Export and Transcribe call the server directly (`ExportControl.tsx:18-21`, `TranscribeControl.tsx:19-22`) without flushing the 800ms op debounce (`doc-runtime/src/sync.ts:14`). Exporting right after an edit, or while saves are failing, renders an older cut with no warning.
   - **Fix:** reuse the canvas Retry save, confirm-on-leave and `beforeunload` guard. `await sync.flush()` before export or transcribe, and block with a message if ops are still pending.

---

## P1 — high friction

### Visual foundation: each is a CSS-level fix with app-wide reach

4. **Disabled buttons look enabled.** [live + code] The only disabled rule is for rail buttons (`styles.css:49`), and `.btn { color:#fff }` overrides the browser's disabled styling.
   - These all rendered full-strength violet or white while disabled, and still brightened on hover: "Create project", "+ Scene", "Add shot", "Takes ▾" on a shot with no takes, and "⚡ Generate 4 takes" with an empty prompt.
   - There are 38 `disabled=` sites. **Fix:** add `.btn:disabled { opacity:.45; cursor:not-allowed; filter:none }` and a matching secondary variant.

5. **The primary button fails AA.** [measured] White on `--accent #8b7cf6` is **3.33:1**, and 2.87:1 on hover (`brightness(1.1)`). The text is 13.33px, so it needs 4.5:1. **Fix:** a dedicated button fill such as `#6d5dd9` (4.98:1). Keep `#8b7cf6` for text, focus rings and borders.

6. **Every button and most inputs render in Arial.** [measured] Buttons compute to `13.3333px Arial`, while body text is Inter → system-ui.
   - `.btn`, the toolbar inputs, the gen-fill bar controls and the palette input set no font (`styles.css:88, 96-104, 200, 421-435`). Native controls don't inherit the body font.
   - Inter itself is never loaded: there is no `@font-face` or font link, and `document.fonts` is empty. Whatever is installed on each machine gets used.
   - The result is two typefaces on every screen. There are also 9 effective font sizes, mostly 1px apart, and no `tabular-nums` on the clip-player timecode, costs or grade values.
   - **Fix:** add `button, input, select, textarea { font: inherit }` and self-host Inter. Use 5 size tokens (11/12/14/16/20), `font-variant-numeric: tabular-nums` for times and costs, and monospace for prompts and seeds per §5.

7. **Native controls aren't themed.** [live + code] There is no `color-scheme: dark` and no `accent-color`. As a result:
   - The Library search box is a **white, square, inset** field (`type="search"` misses the `.toolbar input[type="text"]` rule; `LibraryPage.tsx:84`).
   - The timeline's export-preset select is white (`ExportControl.tsx:49`), and so is the dissolve select (`TimelinePage.tsx:825`).
   - Brush size, layer opacity and the grade sliders are system blue, and so is the continuity checkbox.
   - Deleting a shot or view uses unthemed, blocking `window.confirm` (`ScenePage.tsx:118, 214`).
   - **Fix:** add `:root { color-scheme: dark; accent-color: var(--accent) }`, one input style for every text-like type, and a small in-app confirm component.

8. **The Generate panel's parameter row overflows.** [live + measured] The Takes / Quality / (Seconds) / Seed row has no `min-width:0` on flex children (`styles.css:137-138`). The Seed input runs past the panel's 16px padding to the screen edge (right edge 1441 vs panel 1440), and gets cut off when Seconds is present (`.scratch/ux2/08-…`).
   - Also, the "Target" chip is an accent-bordered box that looks like a focused text field (`styles.css:275-281`).
   - **Fix:** add `.field-row .field { min-width:0 }` and `input { width:100% }`. Restyle Target as a label plus value.

9. **The ⌘K palette jumps while you type, and misses entities.** [live + measured]
   - `.overlay { align-items:center }` (`styles.css:295`) comes after `.palette-overlay { align-items:flex-start }` (`:198`) in the file, so it wins. The palette ends up vertically centered, pushed down by 12vh of padding.
   - Its top edge moves 267 → 387 → 363px as the results change.
   - Typing "mara" returns "No matches" because characters, shots and Library assets aren't indexed. §4 expects "shot 3" and "diner set".
   - **Fix:** raise the rule's specificity (`.overlay.palette-overlay`) and index characters, shots and assets.

10. **State colors don't follow §5.** [live + code]
    - **"Generating" has no gold and no slot placeholder.** A generating shot reads "no takes · ⟳ generating" in plain text, and its thumbnail turns into a bare 11px ⟳. The progress bar is violet (`:181`). Gold is instead spent on captions and the grade marker (`:509-513, 533`).
    - **Green, meant for selected/ready, marks other things.** The timeline playhead is green (`:495`), while the clip player's playhead is violet (`:478`). So are the active-clip outline (`:489`) and the ⛓ continuity connector (`:239`).
    - **"Selected" has four looks:** green take cells, violet layers and tools, white timeline clips, and a bare "✓" on shots.
    - **"Stale" has three looks:** amber on the board, red in the chain inspector (`StoryBoardPage.tsx:431`), dim elsewhere. ⚠ means both stale and failed.
    - **Fix:** semantic tokens `--generating` (gold), `--selected`, `--stale` (amber), `--danger`, and a neutral or white playhead. Add a gold shimmer slot for in-flight takes that respects reduced motion.

11. **Emoji used as icons.** [live + code] There are 58 distinct glyphs. At least 17 always render as color emoji, and more do on macOS. Specific problems:
    - **Pin:** the red 📍 on *every unpinned* shot is the loudest thing on the board and reads as an alert or "pinned". The pinned 📌 looks nearly identical, so state lives in a "· 📌" suffix (`StoryBoardPage.tsx:276`).
    - **Timeline toolbar** (`TimelinePage.tsx:754-853`): 🔪 🕘 🎨 ⧓ 🗣 ⚙ are unguessable without hovering. ← ↩ ↪ have no title at all.
    - **⚡ is used as a verb** ("Generate", 9 sites), not as the §5 AI-provenance badge. Generated assets, takes and layers carry no badge.
    - **Glyphs mean several things:** ♻ is "regenerate"; ⟳ means Jobs, generating, loading, replay and queuing.
    - **Collapsed rail:** it mixes mono ⌂ ▦ ⟳ with color 👤 🖼 🎞 📁. Color emoji ignore the active and dim colors, so the only active cue is a 1.09:1 background.
    - **Fix:** widen 05's deferred "rail glyph redesign" into one SVG line-icon set (e.g. Lucide, 16/20px, `currentColor`). Reserve ⚡ for AI provenance and ⛓ for continuity.

12. **Text hierarchy is flat in shot and job metadata.** [measured] `.meta` only gets a color under `.card` (`styles.css:64`). So `.board-shot-body .meta`, the Scene page's "no takes / 4 takes · ✓ selected" and the Jobs status line render in full `--text` (rgb 232), the same as their titles. **Fix:** a global `.meta { color: var(--text-dim) }`.

### UX

13. **▶ on an *image* take opens a video player that fails with a misleading error.** [live]
    - The Take Picker shows ▶ "Play this take" on every tile (`TakePicker.tsx:129`). For a FLUX or mock-image take it opens ClipPlayer, which shows a black frame, 0:00.0 / 0:00.0 and "Playback failed — the signed link may have expired; close and reopen the clip" (`.scratch/ux2/07-…`).
    - This breaks Principle 6 twice: the control shouldn't be there, and the error blames something else.
    - **Fix:** hide ▶ for image takes and open an image lightbox instead.

14. **Take Picker: number keys commit immediately, and the header lacks context.** [live + code]
    - Pressing "2" swapped the shot's thumbnail behind the modal at once (`TakePicker.tsx:60-65`). The header hint reads "1–9 select · ↵ commit", which implies two steps.
    - Since the selection drives freshness, just browsing takes marks chained downstream shots ⚠ stale.
    - The header says only "Takes", not "Shot 2 — 4 takes".
    - The selected tile has a green and a violet ring stacked together.
    - Provenance (model, seed, prompt) is only in a hover tooltip, and uses the manifest id.
    - **Fix:** numbers stage and Enter commits (per §3.5). Name the shot in the header. Use one selected treatment. Show provenance on the highlighted tile.

15. **"♻ Regenerate from this" on the Story board throws away the recipe.** [code, verified] Its `onRegenerate` ignores its argument and calls `props.onGenerate()` (`StoryBoardPage.tsx:380-383`), so the panel opens blank. The Scene page passes the recipe correctly. This is a one-line fix.

16. **"⛓⟳ Regenerate chain" spends money in one click.** [code, verified]
    - There is no confirmation and no per-shot cost preview (`StoryBoardPage.tsx:447-454`). §7.3 requires both.
    - The tooltip is internal jargon ("One count=1 job per chained shot… depends_on ordering").
    - **Fix:** a confirm step listing each shot, its model and its cost, in plain copy.

17. **Timeline: shots are only half represented as clips.** [live + code]
    - A shot with no takes (placeholder) looks identical to shots that have takes: same dashed violet box and 🎬 (`.scratch/ux2/11-…`). §3.4 calls for striped placeholders.
    - There is no `▾ takes` on a clip, and no generating, stale or failed badge.
    - "Generate into slot" stays available while that shot's job is running (`TimelinePage.tsx:945-955`), which invites paying twice.
    - **Fix:** stripe empty shots, add a takes affordance that opens the TakePicker, badge clips from `shotJobBadges`, and disable Generate while a job is active.

18. **Timeline preview and transport are undersized for editing.** [live + measured]
    - The preview is fixed at 220px tall (`styles.css:496`). A 16:9 frame fills about ⅓ of the 1216px-wide preview box, and about 45% of the 1440×800 viewport sits empty below the tracks.
    - There is no timecode readout anywhere (the ruler shows whole seconds only).
    - Space and B work, but J/K/L and ←/→ frame step exist only in ClipPlayer, not on the timeline.
    - Play sits among about 20 toolbar controls instead of under the preview.
    - The toolbar reflows whenever ▶/⏸ or "saved"/"…" change width. At 1280px its labels wrap to two lines ("+ / Clip", "Scene / shots", "⬇ / Export") and the timeline name truncates to "…".
    - **Fix:** a resizable preview that fills the available height, an `HH:MM:SS:FF` readout in tabular numerals under it, the ClipPlayer transport keys, a fixed-width status slot, and toolbar groups (edit · transport · output) that overflow into a menu.

19. **Shots can't be edited after creation.** [code, verified] `UpdateShot` accepts description, duration target, view and cast, but the UI only ever sends position and pinned (`StoryBoardPage.tsx:131, 262`). Consequences:
    - Typos are permanent.
    - Timeline placeholders are always 5s.
    - Voice prefill from cast (`GeneratePanel.tsx:212-235`) can never trigger.
    - Views never pre-fill references, which undercuts Principle 4.

    **Fix:** inline description and duration editing, cast chips and a view picker on the shot card or Scene page.

20. **Story-board shot cards are cluttered and uneven.** [live + measured]
    - Pin, chain chip, "⚡ More takes" and "Takes ▾" wrap onto up to three rows, so cards range from 81px to 135px tall.
    - "Takes ▾" often wraps onto its own line.
    - The same action has four labels across surfaces: "⚡ Generate" / "⚡ More takes" (board), "⚡ Generate takes" (Scene), "⚡ Generate into slot" (Timeline).
    - The Scene page shows none of the continuity chain the board shows.
    - **Fix:** one primary action per card ("Generate" / "More takes"), plus an overflow ⋯ menu (Takes, Pin, Chain, Delete) and one verb everywhere.

---

## P2 — polish and consistency backlog

- **Button hierarchy.** The Scene page shows four primary violet buttons at once: three "⚡ Generate takes" and "Add shot". Use secondary buttons per row and one primary per view. There is also no danger variant: 🗑 delete-shot looks like "Takes ▾", and "Really delete" on Canvases is a plain chip.
- **Page headers differ on every surface.** Projects puts the H2 above its toolbar. Story puts the H2 inline with an input. Scene uses a back button plus title. Timeline and Canvas put ← and the name inside the toolbar, where they wrap. Build one page-header component (title, back, primary action, status).
- **Empty states are one gray sentence.** None has a call to action; for example "No timelines yet." doesn't mention ⧉ Scene shots. §6 asks for empty states that teach and for story templates. The first-run Projects page is 90% empty.
- **Jobs cards.**
  - Raw task ids still leak ("t2v", "t2i"); PR 44 humanized them only in the panel.
  - Cost units don't match: the panel quoted "~0.5 USD" for the Seedance job, and Jobs reports it as "16.0 gpu·s".
  - Status is carried by a 1.7–1.9:1 border tint, and there is no model name or timestamp.
- **Generate panel copy.**
  - Task options mix humanized and raw labels ("Text → image", "Inpaint / fill", "outpaint", "upscale").
  - The audio row shows raw roles ("— speech_lipsync/music"), and it appears for the mock *image* model.
  - "No healthy model endpoints. Is the dev stack up?" survived the PR 44 vocabulary sweep (`GeneratePanel.tsx:372`).
  - There is no Esc to close.
  - The ⛓ carry chip is text only; §3.4 shows the carried frame's thumbnail.
- **Characters.**
  - The card is sparse: no portrait or reference-slot placeholders, and "project" scope floats as bare white text top-right.
  - The create placeholder truncates to "(e.g. Mara".
  - The voice id is a free-text vendor id.
  - Removing a reference takes one click with no undo (`CharactersPage.tsx:100`).
- **Library.**
  - Titles are "gen: <truncated prompt>".
  - There is no image/video/audio filter.
  - Cards that do nothing on click still get a pointer cursor and hover state (`styles.css:60-62`).
- **Timeline details.**
  - The "V1" label draws over the first clip's name [measured: overlapping rects].
  - Each clip's always-visible × sits next to the right trim handle.
  - Clips show no filmstrip or waveform, even though ClipPlayer already generates them.
  - The History panel (`position:fixed`) and the Color panel (`absolute; top:96`) collide with the docked Generate panel (`styles.css:208, 524-527`).
  - "⚙" and ClipPlayer's "⚙ engine" expose a developer testbed with jargon tooltips ("WebCodecs compositor… `<video>` fallback", "no proxy — playing original").
- **Modal semantics are inconsistent.**
  - The Take Picker is `role=dialog`, but the "Add scene shots" picker isn't [live].
  - Its scene list uses pill buttons unlike any other button in the app.
  - Focus doesn't move into modals (the focus trap is already deferred in 05).
- **Icon-only buttons are labeled by emoji.** `title` is the description, so screen readers announce "kitchen knife", "round pushpin", "bust in silhouette". Add `aria-label` to every icon button, or better, fix it with the icon set in item 11.
- **Scales.**
  - Spacing: 54% of padding/margin/gap values are off the 4px grid (10px ×31, 6px ×30, 7px ×10…).
  - 8 border radii.
  - Card padding varies between siblings (card 12, job/char 12/14, shot 10/14, board-shot 8).
  - **Fix:** space tokens 4/8/12/16/24 and radius tokens 4/6/8/999.
- **Low-contrast UI edges** (non-text elements need 3:1):
  - `--border` is 1.36:1 on the background, so input edges nearly disappear.
  - Active and hover fills are 1.09–1.11:1.
  - Disabled rail items blend to 1.86:1.
  - **Fix:** an input-border token around `#62626c` (3.13:1) and a 2px accent bar on the active rail item.
- **Motion.** The only transition is a 0.6s progress bar. There are no panel or toast transitions, no candidate "materialize" effect, and no `prefers-reduced-motion` guard. Add 120/180ms ease-out tokens and the guard before adding any motion.
- **Light theme.** It would take about a day of CSS work: roughly 15 new semantic tokens, plus about 29 CSS color literals and 3 TS/canvas literals (`ClipPlayer.tsx:257`, `genfill.ts:59-62`) to move onto tokens. There are four near-duplicate violets (`#8b7cf6`, `#8f7aff` fallbacks and favicon, `#2d2a55`, `#26243d`).

---

**Suggested order**

1. **Visual foundation PR, about a day, CSS-first.** Covers items 4–9, 12 and the token part of 10:
   - semantic tokens;
   - base control reset (`font: inherit`, `color-scheme`, `accent-color`, input border);
   - `.btn` disabled, danger and primary-fill fixes;
   - self-hosted Inter plus the type scale and tabular numerals;
   - global `.meta` color;
   - the field-row fix;
   - the palette cascade fix.

   This one change moves the app from "prototype" to "tool" in every screenshot.
2. **P0.2, sorted and grouped endpoints.** Small, and it must land before real keys.
3. **P0.1 and P0.3, the timeline scroll/zoom and save/export integrity cluster.** This is the W5-exit path. Fold in item 18 (preview, timecode, transport) while that code is open.
4. **Quick UX fixes:** items 15 (one line), 13, 14 and 16.
5. **Icon set** (item 11), with ⚡ becoming the provenance badge. This also closes 05's deferred rail item.
6. **Shot editing** (item 19), then the board and timeline shot-representation work (items 17 and 20).
