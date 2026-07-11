# UX review — global shell + image generation/editing (2026-07-11)

**Method.** Two independent passes, merged: (1) a live session driving the real app in Chrome as a user — keyboard and mouse only, no script injection — against the local dev stack (mock BFL/Seedance/ElevenLabs); (2) a code-level audit of the shell and generation surfaces. Findings below are marked **[live]** when reproduced in the browser, **[code]** when verified by reading the source (with file:line), or both. Screenshots from the live pass are in `.scratch/ux/` (gitignored).

**Scope.** Left-rail nav + global affordances, and the image path: generate panel (t2i), canvas editing, gen-fill. Video/timeline surfaces were out of scope except where shared.

---

## What already works well (keep these)

- **Gen-fill loop is genuinely good** [live]: marquee → prompt bar → 4 candidates → in-place preview inside the selection, ←/→ to compare, Enter to commit as a layer. The committed layer lands in the layer stack with a clear hint about paint vs image layers. This is the strongest interaction in the app.
- **Manifest-driven generate panel** [live]: switching model Seedance→FLUX swaps tasks (t2v→t2i/inpaint), surfaces `output format` from params_schema, enforces the 10-ref limit, and reprices the button ("Generate 4 takes · ~0.5 USD" → "~0.1 USD"). Capability-adaptive UI working as designed.
- **⌘K palette, `?` shortcut help, visible focus rings, disabled-with-tooltip rail buttons, "saved" indicator, helpful empty state on Canvases, informative Pin tooltip** [live].

---

> **Status 2026-07-11:** all three P0s fixed in PR 42 (merged) — parent-job rollup copies the representative child error on every path incl. dependency propagation, Jobs shows reasons and suppresses futile retries (`safety_blocked`/`invalid_input`/`dependency_failed`), Story/Scene shots badge "⚠ failed" from the newest targeting job, `UPLOADING` counts as active everywhere; hash routing (`#/<view>/<projectId>[/<entityId>]`) with malformed-fragment fallback, NotFound-only stale-link redirect via replaceState, and no history junk; gen-fill bar handles Esc/Cmd+Z/Cmd+Shift+Z inside the prompt input and has a visible ×. P1/P2 items below remain open.

## P0 — fix before image dogfooding

1. **Failed generations are silent everywhere except Jobs, and even Jobs never says why.** [live + code]
   Live repro: submitted 4 FLUX candidates on a shot with a prompt the mock moderates. Story board showed nothing — no badge, no toast; the shot still reads "empty". The Jobs card says only `failed · 4 takes · t2i · draft` — no error text at all (the parent job row carries no error fields; they live on candidate rows) — and offers **Retry on a safety-blocked, non-retryable failure**, which will fail identically. A dogfooder loses work and can't learn why.
   Code: badges track only queued/dispatched/running (`ScenePage.tsx:35-43`, `StoryBoardPage.tsx:32-40`); JobsPage error line depends on fields the parent row doesn't populate (`JobsPage.tsx:75-79`).
   Fix: propagate candidate error_code/message to the parent job row (or aggregate), render the reason on the Jobs card, add a per-shot "last generation failed: <reason>" badge from the same jobs cache, and suppress Retry (or relabel "Edit & resubmit") for `safety_blocked`/`invalid_input`.

2. **No URL routing.** [live + code] Reload from the Jobs page landed back on Projects with all context gone; browser Back exits the app; nothing is deep-linkable. All navigation is `useState` (`App.tsx:38-42`). Fix: mirror `{view, projectId, entityId}` into the URL hash and restore on boot.

3. **Gen-fill bar can't be dismissed from the keyboard once you've typed.** [live + code] The prompt input autofocuses; with focus in it, Esc does nothing (confirmed live — bar stayed mounted), and there is no × button. The user must click the canvas, then Esc. `GenFillBar.tsx:110`, key handler ignores INPUT targets (`CanvasPage.tsx:190-191`). Fix: handle Escape (and Cmd+Z) in the input's own onKeyDown + add a visible close button.

> **Status 2026-07-11 (later):** P1 items 5–10 fixed in PR 43 (merged) — Library groups fan-out candidates per job ("N takes — show all") via lineage-resolved source_job_id, gen-fill source/mask upload tagged `utility` and hidden behind a toggle, server-side search; canvas mount re-attaches to still-active inpaint jobs (completed ones stay in Jobs/Library by design); gen-fill submit phase is cancelable (nonce-aborted); rejected-save recovery ("↻ Retry save" + confirm-on-leave, beforeunload guard order fixed per review); project/canvas cards are real buttons; Jobs cards link "→ Scene · shot" / "→ Canvas · name" through to their target. Item 4 (UPLOADING) shipped in PR 42. P2 backlog below remains open.

## P1 — high friction, fix soon

4. **`UPLOADING` is excluded from every "active job" filter** [code]: rail count drops, shot ⟳ badges vanish, JobsPage hides progress+Cancel, and the poll backstop stops while a job uploads (`App.tsx:97-103`, `JobsPage.tsx:49`); with SSE down a job strands. Include UPLOADING in all four filters.
5. **Library litter from gen-fill** [live]: one gen-fill run deposited 6 cards — 4 candidates with identical titles ("gen: neon diner sign gl…") plus "(gen-fill source)" and "(gen-fill mask)". Fan-out candidates from shot generation also appear as N identical-looking cards. With no search/filter/grouping the Library becomes unusable within days of dogfooding. Fix: group candidates by job, tag/hide utility uploads, add a text filter.
6. **Back-nav mid-gen-fill orphans the flow** [code]: the job keeps running, candidates land in Library/Jobs, but the choosing strip never re-offers on return (`CanvasPage.tsx:226-231`). Scan for inpaint jobs targeting the canvas on mount and reattach.
7. **Non-retryable canvas save failure is a dead end** [code]: "ops kept locally — not retryable" with no retry button; leaving discards the ops (`CanvasPage.tsx:571-577`). Add manual retry + confirm-before-leave.
8. **No cancel during gen-fill submit phase** [code]: flatten + two uploads with no Discard and Esc suppressed (`GenFillBar.tsx:79-93`). Show Discard during submitting.
9. **Project/canvas cards are clickable divs** [live + code]: unreachable by keyboard (confirmed by tabbing through the whole Projects page), no role/tabIndex (`ProjectsPage.tsx:46`, `CanvasesPage.tsx:85`). The story board already has the right `.card-button` pattern — reuse it.
10. **Jobs cards don't say which shot they targeted** [live]: a failed shot-generation can't be traced back to its shot, and there's no click-through. Add "Target: Scene 2 · Shot 1" linkage.

## P2 — polish / consistency backlog

- **Vocabulary drift** [live]: label "Takes" above a button that says "Generate 4 candidates" in the same panel; gen-fill says "Candidates ×N" with a different option set (1/2/4/6 vs 1/2/4/8). Pick one word and one set.
- **"Model" vs "endpoint" jargon** [code]: gen-fill error copy says `No endpoint offers gen-fill (task "inpaint" + mask/source_image)` (`GenFillBar.tsx:99,147`). Reword around "model".
- **Model choice persistence is inconsistent** [live + code]: gen-fill remembers its model (localStorage); the generate panel re-defaults to Seedance video every open (`GeneratePanel.tsx:137`) — image dogfooders will switch it every single time. Persist per modality. Defaults also differ (panel=draft, gen-fill=master quality).
- **Projects page** [live]: cards with no description fall back to the raw `prj_…` id, which also overflows without ellipsis; "New project name" Enter doesn't submit (Create stays disabled until typed — Enter does work on Canvases; inconsistent).
- **Pluralization** [live]: "1 views · 1 shots" on every scene card.
- **⌘K can't jump to projects** [live]: on the Projects page, typing a project name gives "No matches" under a "Jump to…" placeholder (entity jump only covers the open project's scenes/canvases/timelines). Add project entries.
- **Collapsed rail (the default) hides the project name** [live]: only the expanded rail shows "Iris · Diner Story"; collapsed icons are low-contrast mixed emoji/glyphs, several nearly invisible on the dark theme. Disabled tooltips say only "Open a project first," hiding what the sections *are*. Show section name + the gate in the tooltip; consider a compact project chip in collapsed mode.
- **Story vs Scenes IA** [live]: two nav entries over the same entities (board with shots vs thin list with style notes) with nothing explaining the split. Consider folding Scenes into Story (scene settings on click-through).
- **Esc closes all stacked overlays at once; modals don't trap focus** [code] (`AssetThumb.tsx:75-83`, `TakePicker.tsx:50`).
- **Marquee can extend beyond the canvas bounds** [live]: the dashed rect renders into the off-canvas checkerboard (fill correctly clips, but the visual lies).
- **Candidate strip shows no model/prompt context** [live]; layer auto-name is "Gen fill · gen-fill" — use a prompt excerpt.
- **Canvas zoom is wheel-only** [code] (`CanvasViewport.tsx:157-172`): add fit/100% buttons.
- **Subject tool's shift-to-exclude and click-to-refine are undiscoverable** [code] (`CanvasViewport.tsx:199`): tooltip + ShortcutHelp rows.
- **document.title is always "Iris", favicon is `data:,`** [code + live]: 12× 404 resource errors in console (favicon requests). Set per-view titles.
- **Misc** [code]: error states styled as plain grey status text (`ProjectsPage.tsx:36`, `LibraryPage.tsx:56`); generate button gives no "Submitting…" feedback; GeneratePanel loading state has no Close; task select shows raw ids ("t2i"); Remove silently ignores a typed prompt (`GenFillBar.tsx:160`); collapsed rail loses the active-jobs count; "Canvas" action chips clip the card edge in Library [live]; stale `canvasError` persists across navigation (`App.tsx:43`).

---

**Suggested order for the image-dogfood window:** P0.1 (failure surfacing — it will burn the team on day one), P0.3 (gen-fill Esc — the surface they'll live in), P1.5 (Library grouping — scales with usage), P0.2 (routing — reloads happen), then P1.4/P1.6/P1.7 as the re-attach/save-integrity cluster.
