# Iris — UI/UX Design

**Status:** Draft v0.1 · July 2026
**Companion docs:** [High-Level Design](01-high-level-design.md) · [Technical Design](03-technical-design.md)

This document translates the product pillars and signature workflows (W1–W6) into information architecture, screen layouts, and interaction patterns. Wireframes are ASCII schematics — proportions indicative, not pixel truth.

---

## 1. Design Principles

1. **The project is the interface.** Users navigate by story structure (scenes → shots → takes), not by file listings. Media browsers exist, but the primary mental model is "where in my story am I working?"
2. **Generate and edit are one gesture apart.** Every generative action lands its output *in place* (a layer on the canvas, a take in the shot, a view in the set) — never in a downloads purgatory. Every piece of media offers "edit this" and "use as reference" wherever it appears.
3. **Fan-out is the default, not an option.** The UI assumes multiple candidates per generation. Choosing between takes is a first-class, pleasurable interaction — not a chore.
4. **Context is pre-filled, never re-entered.** Opening the generate panel from a shot inside Scene "Diner" with Mara cast means the set view, character refs, and continuity frame are already attached. The user edits intent, not plumbing.
5. **Progressive disclosure with honest depth.** Defaults serve the prosumer; every panel has an "advanced" tier for the internal team and power users (seeds, samplers, per-model params). Nothing powerful is removed — only folded.
6. **Capability-adaptive, never capability-dishonest.** Controls reflect what the selected model can actually do. Unsupported controls are visibly disabled with the reason ("Iris Video: no audio-conditioned sync yet — switch to Seedance 2.0 for in-generation lip sync"), not hidden mysteriously or silently ignored. Corollary from the research: using a consistency/reference feature must never lock the user out of editing tools (Midjourney's Omni Reference disables its inpainting, vary-region, pan, and zoom — the market's clearest anti-pattern).
7. **Async without anxiety.** Generation takes minutes. The UI never blocks on a job; progress is ambient (queue tray, slot placeholders); results arrive with a gentle signal wherever the user now is.
8. **Keyboard-first for editors.** NLE and Photoshop users bring deep muscle memory. We adopt their conventions (J/K/L, V/B/N tools, brackets for brush size) rather than inventing our own.

## 2. Information Architecture

```
Workspace
├── Home (projects grid, recent activity, queue overview)
├── Workspace Library (characters, style packs, shared sets)
├── Models & Keys (endpoints, BYO keys, usage/costs)
└── Project
    ├── Story        ← default landing: scenes & shots board
    ├── Scenes/      ← scene pages: set, views, shots, continuity
    ├── Timelines/   ← video studio documents
    ├── Canvases/    ← image studio documents
    ├── Library      ← project assets: all media, lineage, search
    └── Jobs         ← generation queue & history
```

**Primary navigation** is a slim left rail inside a project: `Story · Scenes · Timelines · Canvases · Library · Jobs`. The **top bar** holds project switcher, global search (⌘K), the **queue tray** (ambient job status), and share/export.

**The three working surfaces** — Story/Scene pages, Image Studio, Video Studio — share a persistent right-side **Context Dock** (collapsible) with three tabs: **Generate**, **Inspector** (properties of selection), and **Library** (drag-out refs and media). This is the key unifier: the same Generate tab, with the same reference chips and model picker, appears in every surface; only its target differs (canvas region / shot / view).

### Modes: Studio vs. Assist

A single toggle (per user) between:
- **Studio mode** (default for internal team): full panels, all tools, dense layout.
- **Assist mode** (default for new prosumer accounts): simplified toolbars, guided empty states, prompt-forward generate panel, common actions surfaced as big affordances. Same documents, same data — no forked features, purely presentation. Users graduate to Studio mode organically (and the app suggests it when they repeatedly open advanced panels).

## 3. Key Screens

### 3.1 Story view (project landing)

The writers'-room board: scenes as columns/rows, shots as cards. This is workflow W3's home.

```
┌────────────────────────────────────────────────────────────────────────────┐
│ ⬡ Iris   Diner Story ▾              🔍 ⌘K            ⟳ 3 jobs   Export ▾  │
├──────┬─────────────────────────────────────────────────────────────────────┤
│Story │  SCENE 1 · Diner ────────────── SCENE 2 · Rooftop ─────────  + Scene│
│Scenes│  Set: Diner ✓ 4 views          Set: Rooftop ⚠ no views             │
│Time- │ ┌─────────┐ ┌─────────┐        ┌─────────┐ ┌─────────┐             │
│lines │ │ SHOT 1  │ │ SHOT 2  │        │ SHOT 6  │ │ SHOT 7  │             │
│Canv- │ │[thumb ▶]│ │[thumb ▶]│──────▶ │ (empty) │ │ (empty) │             │
│ases  │ │ 4 takes │ │ 2 takes │ chain  │ Generate│ │ Generate│             │
│Libr- │ │ ✓ T2    │ │ ⚠ stale │        │         │ │         │             │
│ary   │ └─────────┘ └─────────┘        └─────────┘ └─────────┘             │
│Jobs  │  Mara · Cook                    Mara                                │
├──────┴──────────────────────────────────────────────┬──────────────────────┤
│                                                     │ CONTEXT DOCK         │
│  [Shot 2 selected]                                  │ [Generate][Insp][Lib]│
│                                                     │ Target: Shot 2       │
│                                                     │ …                    │
└─────────────────────────────────────────────────────┴──────────────────────┘
```

- Shot cards show: selected-take thumbnail (hover to scrub), take count, duration, cast chips, and state badges — `✓ take selected` · `⚠ stale` (upstream continuity changed) · `⟳ generating` · `empty`.
- **Continuity chains** render as arrows between shot cards; clicking an arrow opens the chain inspector (what's carried: last frame, refs, style).
- Drag shots to reorder; reordering across a chain link prompts to re-link or break.
- "Open in Timeline" assembles the scene's selected takes in story order.

### 3.2 Scene page & Set

One scene's world: the set with its views, cast, shots, and continuity settings. Home of W1.

```
┌────────────────────────────────────────────────────────────────────┐
│ Scene 1 · "Diner"                                    Open Timeline │
├──────────────────────────────┬─────────────────────────────────────┤
│ SET · Diner                  │  VIEWS                    + Add View│
│ ┌──────────────────────────┐ │ ┌──────┐┌──────┐┌──────┐┌──────┐   │
│ │   [3D blockout viewport] │ │ │wide  ││count ││booth ││door  │   │
│ │   ▦ untextured scene     │ │ │door ●││er cu ││med   ││rev   │   │
│ │   📷 cams: V1 V2 V3 V4   │ │ └──────┘└──────┘└──────┘└──────┘   │
│ └──────────────────────────┘ │  ● = camera-registered to 3D       │
│ Style notes: 50s diner,      │  [Generate more views…]            │
│ neon, night, 35mm            │                                     │
├──────────────────────────────┴─────────────────────────────────────┤
│ CAST: (Mara)(Cook)  + Add          SHOTS: [1][2][3][4][5] + Shot   │
└─────────────────────────────────────────────────────────────────────┘
```

- **Views strip:** each view card = reference plate + name + camera badge. Actions: `Edit in Image Studio` · `Use as reference` · `Generate video from this view` · `Register camera`.
- **"Generate more views"** runs multi-view expansion from selected views (W1); candidates arrive in a picker (same component as the take picker, §4.2) before being cataloged.
- 3D viewport (when a scene model exists): orbit, place/adjust cameras, `Render depth from this camera` → becomes a control input chip usable in any generate panel.

### 3.3 Image Studio

Photoshop-familiar skeleton; generation folded into selection and layers. Home of W1, W2, W6.

```
┌──────────────────────────────────────────────────────────────────────────┐
│ Canvas: "diner-wide-plate" ▾        ↺ ↻     zoom 62% ▾        Promote ▾  │
├───┬──────────────────────────────────────────────────┬───────────────────┤
│ V │                                                  │ [Gen][Insp][Lib]  │
│ M │                                                  ├───────────────────┤
│ L │                                                  │ GENERATE          │
│ W │              CANVAS                              │ ▸ Target: selection│
│ ✂ │        (selection marching ants)                 │ ┌───────────────┐ │
│ 🖌│                                                  │ │"replace sign  │ │
│ ⌫ │                                                  │ │ with EAT neon"│ │
│ T │                                                  │ └───────────────┘ │
│ ◻ │                                                  │ Refs: (Diner/wide)│
│ 💧│                                                  │       (+ add)     │
│   │                                                  │ Model: Iris Image▾│
│   │                                                  │ Takes: 4 ▾  Adv ▸ │
│   ├──────────────────────────────────────────────────┤ [⚡ Generate]      │
│   │ LAYERS                    ○ fx  ▣ mask  + layer  ├───────────────────┤
│   │ ▣ gen: neon sign     👁 ◻mask  ← candidate layer │ HISTORY / VERSIONS│
│   │ ▣ paint fixes        👁                          │                   │
│   │ ▣ base plate         👁 🔒                       │                   │
└───┴──────────────────────────────────────────────────┴───────────────────┘
```

- **Tool rail:** move (V), marquee (M), lasso (L), magic wand (W), AI select (S — subject/semantic; click a thing, get a mask), crop, brush (B), clone/heal, eraser, text (T), shapes, eyedropper. Familiar single-key shortcuts.
- **Generative fill flow (W6):** make any selection → Generate tab auto-targets it → prompt → N candidates → **candidate strip** appears above the canvas; arrow through them rendered in place → confirm lands as a masked layer (fully re-editable; alternates retained in layer's history flyout).
- **Layers panel** is standard (blend modes, opacity, masks, adjustment layers, groups). Generated layers carry a ⚡ badge; clicking it opens provenance (prompt, model, seed) with `Regenerate with changes…`.
- **Promote ▾** (top right): `→ View of a Set…` · `→ Character reference…` · `→ Video keyframe…` · `→ Export`. This single menu is the bridge that makes the asset system real (Principle 2).
- **Canvas-from-camera:** starting a canvas from a 3D set camera pre-loads the depth map as a control chip so generation follows the scene's geometry.

### 3.4 Video Studio

NLE-familiar skeleton; shots and takes are timeline citizens. Home of W3–W5.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ Timeline: "Episode 1 cut" ▾      ◐ proxy ▾        ⟳ 2 jobs      Export ▾    │
├──────────────────────────────────────────────┬───────────────────────────────┤
│                                              │ [Generate][Inspect][Library]  │
│                PREVIEW                       ├───────────────────────────────┤
│              (JKL, space)                    │ GENERATE · Target: Shot 3     │
│                                              │ Continue from: Shot 2 ▣ chain │
│         ◀◀  ◀  ▶  ▶▶    00:00:41:12         │ [last frame thumb] + style ✓  │
├──────────────────────────────────────────────┤ Prompt: "Mara slides the     │
│ ═╤═ TIMELINE ═══════════════════════════════ │  plate across the counter…"  │
│ V2 ─────────▓titles▓─────────────────────── │ Refs: (Mara)(Diner/counter)   │
│ V1 ▓Shot1·T2▓▓Shot2·T4▓▓Shot3 ⟳ gen▓▓Shot4▓ │ Audio: (dialog_03.wav) 🗣lip  │
│    │✓4 takes││⚠stale ▾││░placeholder░│      │ Camera: (cam V2 dolly) depth  │
│ A1 ▓dialog──▓▓dialog───▓─────────────────── │ Model: Iris Video ▾  ✓caps    │
│ A2 ▓─── music ─────────────────────────────  │ Duration: 6s · Takes: 4       │
│ ═╧══════════════════════════════════════════ │ Advanced ▸    [⚡ Generate]   │
└──────────────────────────────────────────────┴───────────────────────────────┘
```

- **Clips are shots.** A timeline clip bound to Shot 3 plays Shot 3's *selected take*; a small `▾ takes` affordance on the clip opens the take picker in place. Swapping takes never disturbs the edit (trims re-apply within the new take's bounds; over-length mismatch is flagged on the clip).
- **Placeholder clips:** an empty shot occupies real timeline space at its target duration, striped, with a `Generate` button — you can cut the whole piece before a frame exists (animatics with temp images/audio supported).
- **Standard NLE toolset:** blade (B), select (V), ripple/roll (Q/W), slip/slide (Y/U), snapping (S), markers (M), J/K/L transport, I/O in-out points. Transitions drag from the Library tab. Right-click clip → speed/duration, speed ramp editor.
- **Audio:** per-track mixer flyout, clip gain handles, keyframable volume/pan, auto-duck music under dialogue, waveforms always visible.
- **Color:** clip → Inspector → Color section (LUT, exposure/contrast/temp/tint, wheels in Advanced); `Match to previous shot` assist.
- **Captions:** auto-transcribe → caption track; text edits in Inspector; style presets.
- **Stale propagation (W3/W5):** re-selecting Shot 2's take marks downstream chained shots ⚠ stale with a one-click `Regenerate chain…` (queues jobs in order, each waiting on its predecessor) — with per-shot pin/freeze to opt out.

### 3.5 Take Picker

The most important novel component (Principle 3). One component, reused for video takes, image candidates, and view candidates.

```
┌ Shot 3 — 4 takes (2 new) ─────────────────────────────── ⚙ compare ▾ ┐
│ ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐          │
│ │  TAKE 1    │ │  TAKE 2 ✓  │ │  TAKE 3 ●  │ │  TAKE 4 ●  │          │
│ │ [hover-    │ │ [selected] │ │  [new]     │ │  [new]     │          │
│ │  scrub]    │ │            │ │            │ │            │          │
│ └────────────┘ └────────────┘ └────────────┘ └────────────┘          │
│ ▶ synced play all  ·  A/B: hold ⇧ to flip  ·  ⌨ 1-4 select, ↵ commit │
│ Take 3 ▸ prompt/seed/model/refs · ♻ regenerate from this · ⭐ keep    │
└───────────────────────────────────────────────────────────────────────┘
```

- **Synced playback** of all candidates; hover-scrub on any tile; A/B flip between two on hold-shift; keyboard: number keys to stage, Enter to commit, arrows to navigate.
- Committing is non-destructive: all takes remain on the shot (with archive/cleanup policies for cost).
- Every tile exposes provenance and `Regenerate from this` (pre-fills the generate panel with that take's exact recipe for tweaking) — the tight iteration loop.
- **Fan-out economics in the UI:** takes generate at **draft quality by default** (badge on tile); committing a take offers/auto-runs `Master` (full res/steps re-render or upscale). Cost per fan-out is always visible next to the Generate button.

### 3.6 Generate panel (Context Dock · Generate tab)

Identical anatomy everywhere; only the target changes:

1. **Target** (auto: selection / shot / view / new canvas) — with continuity block when the target chains from a predecessor (shows carried frame + toggles for what's carried).
2. **Prompt** (template-aware: `{character}`, `{set.view}` chips resolve from context; typing `@` opens an inline library mention menu — `@Mara`, `@Diner/wide` — inserting the entity as both prompt token and attached reference in one gesture; prompt presets menu). *(The @-mention mechanic is validated by LTX Studio's Elements — the one consistency UX with market traction — but ours is core-tier, not paywalled.)*
3. **References** — chips, drag-in from Library tab: image refs (characters, views, style), video refs (motion), audio refs (voice → lip-sync toggle, music). Voice audio can be generated in place via ElevenLabs (character's voice ref preselected) without leaving the panel. Chip shows *role*; roles offered per model manifest.
4. **Control inputs** — depth-map/camera chips from 3D sets, pose, first/last frame conditioning.
5. **Model picker** — grouped: *Iris models · Your endpoints · API models (key status)*. Each entry shows capability badges; picking a model that can't honor an attached chip strikes the chip with an explanation and suggests capable models (Principle 6).
6. **Count & quality** — takes: 1/2/4/8 · draft/master.
7. **Advanced** (collapsed): seed, steps, guidance, sampler, negative prompt, raw model params (schema-driven from the manifest).
8. **⚡ Generate** — with cost estimate and queue ETA.

### 3.7 Jobs & queue tray

- **Queue tray** (top bar): count + mini-progress; popover lists running/queued jobs with thumbnails, target links, cancel.
- **Jobs page:** full history with filters (project/model/status/cost), per-job detail (inputs, artifacts, cost, timing), bulk retry/cancel. Failures are legible: model error text, input snapshot, one-click `Edit & retry`.
- Completion behavior: if the user is looking at the target, results animate in (candidate strip / take badge); otherwise a toast + badge on the queue tray. Never a modal.

## 4. Cross-Cutting Interaction Patterns

- **⌘K everywhere:** navigate ("shot 3", "diner set"), act ("generate", "add caption track"), search library semantically.
- **Universal media affordances:** every thumbnail anywhere (library, take, view, frame) right-clicks/long-presses to: `Use as reference` · `Edit in Image Studio` · `Set as conditioning frame` · `Open lineage` · `Download`.
- **Lineage view:** a graph panel from any asset — upstream (what made it) and downstream (what used it). Doubles as the "why does this look like that" debugger and the regeneration launchpad.
- **Undo:** per-document undo stacks (canvas, timeline) with standard ⌘Z; *structural* actions (take selection, promote, catalog) are individually reversible via the same stack of the surface where they happened; destructive actions (delete asset with dependents) require typed confirmation and show the dependents.
- **Drag-and-drop is always meaningful:** file → library (import), library item → canvas (layer), → timeline (clip), → generate panel (reference chip), view → shot (set the shot's view).
- **Autosave everything; named versions on demand** (canvas snapshots, timeline versions). No "save" button.

## 5. Visual & Design System

- **Dark UI by default** (creative-tool convention; light theme supported at the token level from day one). Near-black neutral surfaces, one restrained accent (iris violet), semantic colors reserved for states: gold = generating, red = failed/stale-critical, amber = stale, green = selected/ready.
- **Density:** compact 4px-grid metrics in Studio mode; Assist mode relaxes spacing and font sizes. All components take a density token.
- **Typography:** a neutral grotesk (e.g., Inter) UI-wide; tabular numerals for timecode; monospace for prompts/params.
- **Iconography:** consistent 16/20px line icons; the ⚡ badge is the universal "AI-generated" marker; the chain-link glyph is the universal continuity marker.
- **Motion:** 120–200ms ease-out for panels/toasts; candidate arrivals use a subtle materialize; no decorative animation in the timeline (performance and pro feel).
- **Accessibility:** WCAG AA contrast in both themes; full keyboard operability of panels and pickers; reduced-motion mode; captions/transcripts are first-class product features and get the same care internally.
- **Component inventory (build order):** Context Dock, Generate panel, Take Picker, shot card, view card, reference chip, capability badge, queue tray, lineage graph, layers panel, timeline track system, transport, mixer flyout.

## 6. Onboarding (Phase 1)

- First-run: create a project from a **story template** ("commercial 30s", "web series episode", "blank") that pre-scaffolds scenes/shots to teach the model-of-work by example.
- Empty states teach: an empty Set shows the W1 loop as three illustrated steps with a `Generate first view` button; an empty shot card explains takes.
- A guided "first scene" checklist (create set → cast character → generate shot → pick take → export) completable in under 15 minutes with Iris models and default settings.

## 7. Open UX Questions

1. **Story view scale:** board layout above works to ~10 scenes × ~10 shots; long-form needs an outline/list mode — Phase 1 or later?
2. **Take retention policy UX:** unlimited takes are a storage cost; propose auto-archive of unselected drafts after N days with a "⭐ keep" override — validate with internal use.
3. **Chain regeneration consent:** batch "regenerate chain" can be expensive (5 shots × 4 takes); current design queues with per-shot cost preview and requires explicit confirm — is a "draft-quality chain preview" tier needed?
4. **Assist mode scope at launch:** internal team doesn't need it; build the toggle skeleton in Phase 0 but defer Assist presentation polish to Phase 1?
5. **3D viewport depth:** how much camera-authoring UI (keyframed moves? just orbit+place?) before it becomes a modeling tool we don't want to build — needs a dogfooding checkpoint.

---

*Next: [03 — Technical Design](03-technical-design.md) — architecture that makes these surfaces real: rendering engines, generation orchestration, capability manifests, and the collab-ready data model.*
