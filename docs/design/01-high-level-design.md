# Iris — High-Level Design

**Status:** Draft v0.1 · July 2026
**Companion docs:** [UI/UX Design](02-ui-ux-design.md) · [Technical Design](03-technical-design.md) · [Market & Model Research](../research/2026-07-market-and-model-landscape.md)

---

## 1. Vision

Iris is an integrated, AI-native image and video editor for people who *produce finished stories* — commercials, web series, short films — primarily from generated footage. It combines:

- a **Photoshop-class image studio** (layers, masks, selections, adjustments) fused with generative editing,
- a **Premiere-class video studio** (multi-track timeline, trimming, transitions, audio, color) fused with generative video,
- a **shared asset system** where scenes, sets, characters, and reference plates are first-class, reusable objects,
- a **generation engine** that treats AI generation as a *production process* — references in, multiple takes out, continuity carried shot-to-shot — not a one-shot prompt box.

The one-sentence pitch: **the first editor where "generate" and "edit" are the same workflow, and where the project itself — its scenes, characters, and continuity — is what drives generation.**

### Why now, why us

Every existing tool falls on one side of a gap:

- **Traditional editors** (Photoshop, Premiere, Resolve) have deep editing but bolt AI on as isolated filters. They have no concept of "generate the next shot in this scene with these characters."
- **AI generation tools** (Runway, Kling, LTX Studio, Flow, etc.) have strong models but shallow editing, weak asset management, and treat each generation as a disconnected event. Assembling a coherent multi-shot piece means exporting clips to a real NLE and losing all generative context.

Iris closes the gap, and it is grounded in **our own models** — a customized Wan 2.2 video stack and Qwen Image stack with capabilities most commercial APIs don't expose: image references for image *and* video, video and audio references, depth-map-driven following of untextured 3D scenes and bodies, multi-view generation, and lip sync (audio-conditioned in-generation sync in progress on the Wan stack). The product is designed around what these models can do, while remaining open to other self-hosted open-weight models and commercial APIs (Seedance, Nano Banana, etc.).

## 2. Users

### 2.1 Primary (launch): our internal production team

We produce AI stories ourselves. Iris is dogfooded from day one; internal production is the forcing function for correctness and workflow speed. Internal users are technically sophisticated, tolerate rough edges, and will exercise the deepest features (3D-guided generation, model-specific controls) immediately.

### 2.2 Secondary (fast follow): prosumers and light professionals

Creators making commercials, web series, YouTube content, music videos, and spec work — solo or in teams of 2–5. They know editing concepts (they've used Premiere/CapCut/Resolve) but are not conforming 4K features for theatrical release. They will not read documentation; defaults must be excellent and complexity progressive.

### 2.3 Explicit non-targets (for now)

- Major studio / high-end post pipelines (OTIO conform, EDL/AAF round-tripping, HDR mastering, 10-bit broadcast delivery).
- Casual meme/social-clip makers who are better served by CapCut templates.
- Live streaming / real-time broadcast.

## 3. Core Concepts (Domain Model)

These nouns define the product. Everything in the UI and API is built from them.

| Concept | Definition |
|---|---|
| **Workspace** | Top-level tenant: members, model endpoints, API keys, billing. (Single-user at launch, but modeled for teams.) |
| **Project** | A production: one commercial, one episode, one film. Contains the story structure, timelines, canvases, and a library. |
| **Scene** | A narrative/spatial unit: "the diner," "rooftop at night." Owns a **Set** and groups Shots. Scenes are the continuity anchor. |
| **Set** | The visual definition of a scene's world: reference plates (views), an optional untextured 3D scene, lighting/style notes, and derived control maps (depth, camera paths). |
| **View** | One cataloged perspective of a Set — e.g., "wide from the door," "close on the counter" — a reference plate image (generated in the image studio or imported), optionally camera-registered against the 3D scene. |
| **Character / Entity** | A person, creature, prop, or product with reference images (turnarounds, expressions), voice reference audio, and optionally a 3D body. Reusable across scenes and projects. Designed to hold *both* consistency mechanisms the market has converged on: reference bundles (per-generation conditioning) now, and an optional trained-identity slot (LoRA/embedding on our models, Soul-ID-style) later. |
| **Shot** | A unit of intended footage inside a Scene: description, refs, duration target, camera intent. A Shot *owns its Takes*. |
| **Take** | One generated (or imported) video candidate for a Shot, with full provenance: model, prompt, seed, refs, control inputs, parent take. Multiple takes per shot are the norm; one is **selected**. |
| **Sequence** | An ordered chain of Shots with continuity links — "shot B starts from the last frame of shot A's selected take" (plus carried style/character context). |
| **Canvas** | An image-studio document: layered, non-destructive, Photoshop-like. A canvas can be *promoted* to a View, a Character ref, or a keyframe. |
| **Timeline** | A video-studio document: multi-track NLE timeline referencing Takes and imported media. |
| **Asset** | Any stored media (image, video, audio, 3D model, LUT, font) with metadata, tags, and lineage. Views, Takes, Canvas exports are all assets. |
| **Generation Job** | An async request to a model: inputs (prompt, refs, control maps, conditioning frames), target (shot/canvas region), fan-out count, and resulting artifacts. |
| **Model Endpoint** | A configured generator: our hosted models, a self-hosted open-weight endpoint, or a commercial API with a user's key. Declares a **capability manifest** (what refs/controls/durations it supports). |

**The load-bearing relationships:**

1. **Canvas → View → Reference → Generation.** You paint/generate a reference plate in the image studio, catalog it as a View of a Set, and every future image or video generation in that Scene can cite it as a reference. This is the loop that makes the asset system real rather than a folder of files.
2. **Take → last frame (+ context) → next Shot's generation.** Sequence continuity is a first-class edge in the data model, not a manual export/import. When a take is re-selected upstream, downstream shots know they're stale.
3. **Shot → many Takes → one selected.** Fan-out and selection is the fundamental AI-video working rhythm; the timeline references the *shot*, resolving to its selected take, so swapping takes never breaks the edit.

## 4. Product Pillars

### 4.1 Image Studio

Photoshop-class fundamentals, generative at the core.

**Editing baseline (table stakes):** layers and groups; raster + adjustment layers (curves, levels, HSL, color balance, LUTs); layer masks and clipping masks; blend modes; selections (marquee, lasso, magic wand, *AI subject/semantic select*); transform (scale/rotate/skew/perspective/warp); crop and canvas resize; brush/eraser/clone/heal; text layers; shape layers; filters (blur, sharpen, noise, stylize); non-destructive everything; full history.

**Generative capabilities (the point):**
- **Text-to-image and reference-driven generation** — with Character and View refs pulled from the library, not re-uploaded files.
- **Generative fill / inpaint / outpaint** as selection-native operations (select → describe → fan out N candidates → pick → lands as a new layer).
- **Multi-view expansion:** from one View of a Set, generate consistent additional Views (our multi-view model capability) — this is how a Set gets built out.
- **3D-guided generation:** load the untextured 3D scene, position a camera, render a depth map, generate the textured plate from it. Camera registration is stored on the View.
- **Restyle / style transfer** with style refs; **upscale**; background removal; relight (as models permit).
- **Promote to asset:** any canvas or region becomes a View, Character ref, or video keyframe in one action.

### 4.2 Video Studio

Premiere-class fundamentals, built around generated footage.

**Editing baseline (table stakes):** multi-track video/audio timeline; blade/ripple/roll/slip/slide trimming; snapping and magnetic behaviors; transitions (cut, dissolve, wipe, custom); clip transforms and speed (including speed ramps); keyframable effects and motion; titles and captions (auto-transcription); audio: track mixer, volume/pan keyframes, ducking, basic cleanup; color: LUTs, primary wheels/curves, shot-match assist; export presets (social, web, ProRes master); proxy/preview rendering for smooth playback.

**Generative capabilities (the point):**
- **Generate into the timeline:** a Shot is a timeline citizen. An empty shot is a *planned* clip — write the description, attach refs, generate takes, and the selected take fills the slot at the right duration.
- **Sequence continuation:** "extend this shot" and "generate next shot from this one's last frame" as one-click operations, with carried context (character refs, set view, style, camera). The continuity chain is visible and editable.
- **Multi-take management:** every generation fans out (configurable N); takes are compared in a dedicated picker (side-by-side, hover-scrub, frame-diff); selection is non-destructive and revisitable. Takes record full provenance for regeneration with tweaks.
- **Reference-conditioned generation:** image refs (characters, views), video refs (motion/style), audio refs (voice for lip sync, music for rhythm) — surfaced per the active model's capability manifest.
- **3D/depth-guided shots:** attach a camera move authored against the Set's 3D scene; the depth-map sequence drives generation (our models' key differentiator).
- **Lip sync:** dialogue audio (uploaded, or generated via ElevenLabs) drives in-generation sync as an audio reference — supported by Seedance today, being added to our Wan stack (in progress); post-hoc lip sync as the fallback for models without audio conditioning.
- **Video-to-video:** restyle, inpaint a region across time, replace a character/object, extend duration.
- **AI edit assists:** transcript-based editing for dialogue-heavy content, auto shot detection on imports, silence removal.

### 4.3 Asset & Scene Management (the connective tissue)

Not a DAM bolted on the side — the spine of the product.

- **Library per project + shared workspace library.** Characters and style packs are workspace-level (reused across projects); Scenes/Sets are project-level by default, promotable.
- **Sets with cataloged Views:** the reference-plate workflow described above. A Set page shows its views spatially (registered to the 3D scene when present) and lists every generation that used them.
- **Character pages:** ref images (turnaround, expressions), voice samples, 3D body; every appearance across projects tracked.
- **Lineage everywhere:** every asset knows what generated it (job, model, prompt, refs, seed) and what it was used in. "Show me every take that used this view." Regenerate-with-changes from any point in the lineage.
- **Versioning:** assets are immutable-by-version; edits create versions; references pin or float ("latest") explicitly.
- **Search:** text metadata + semantic (embedding) search over the library; filter by scene, character, type, model, date.

### 4.4 Generation Engine

The subsystem that turns model inference into a production tool.

- **Job orchestration:** every generation is an async job with queue position, progress, cost estimate, cancellation, and retry. Fan-out (N takes) is native. Long video jobs survive page reloads.
- **Capability manifests:** each Model Endpoint declares what it supports — modalities, max resolution/duration, ref types (image/video/audio), conditioning (first frame, last frame, first+last, depth, pose), lip sync, extension, camera control, audio generation. The UI adapts: controls that the selected model can't honor are hidden or marked, and the *task* ("continue this shot with this character") can suggest capable models.
- **Model roster:** our Wan 2.2-derived video models and Qwen-Image-derived image models (hosted on our GPUs) are the defaults; self-hosted open-weight endpoints (user-provided URLs) and commercial APIs (Seedance 2.0, Nano Banana, and peers — BYO keys) are peers in the same abstraction.
- **Continuity payloads:** the engine standardizes what "carry context to the next generation" means — conditioning frames (last frame or last k frames), ref bundles, style embeddings/prompt scaffolds — and maps it to each model's actual interface, degrading gracefully (a model with only first-frame conditioning still gets the last frame as its init image).
- **Prompt & preset system:** reusable prompt templates with slots filled from the domain model ("{character} in {set.view}, {shot.camera}"); per-model prompt dialects handled by adapters; workspace-level style presets.

### 4.5 Platform & Openness

- **Web-first**, engineered so a desktop shell (for local file performance, local GPU inference later) is a packaging decision, not a rewrite.
- **BYO keys and endpoints** at workspace level, with per-project overrides; cost visibility per job/shot/project.
- **Export without lock-in:** standard media exports always; project-structure export (JSON + media) so users are never trapped.

## 5. Signature Workflows

These are the flows the whole design optimizes for — the demo, the dogfood, and the differentiation.

### W1 — Build a Set from nothing
Create Scene "Diner" → in the image studio, generate a hero wide shot (text + style refs) → refine with generative fill and manual paint → promote to View "wide from door" → multi-view generate three more angles → catalog all as Views → (optional) import/blockout an untextured 3D diner, register views to cameras. The Set is now a reusable, referenceable place.

### W2 — Cast a character
Create Character "Mara" → generate/import a turnaround and expression sheet in the image studio → attach voice reference audio → Mara is now citable in any generation, in any scene, with consistent identity.

### W3 — Shoot a scene
In Scene "Diner," write Shots 1–5 as text beats → per shot: pick View + Characters, set camera intent (freeform, or a camera path on the 3D set → depth sequence) → generate 4 takes each on our video model → pick takes in the comparison view → Shots 2–5 auto-chain from the previous shot's last frame with carried context. Result: a coherent 5-shot scene, no exports, no re-uploads.

### W4 — Dialogue with lip sync
Write dialogue → generate voice takes via ElevenLabs (matching Mara's voice ref) or upload recordings → generate the shot with the audio as a reference for in-generation sync (Seedance today; our Wan stack when the R&D work lands), or apply post lip sync to existing takes.

### W5 — Cut and finish
Timeline assembles Shots (auto-populated from the Scene in story order) → trim, reorder, add music and SFX → auto-captions from dialogue → shot-match color pass → notice Shot 3's take is weak → regenerate with a tweak from its lineage page → new take drops into the same timeline slot → export 16:9 master + 9:16 cut.

### W6 — Fix in image, not in re-roll
A take is great except a sign in the background says gibberish → open its last frame (or any frame) in the image studio → fix with generative fill/type tools → use the fixed frame as a conditioning keyframe to regenerate or patch the take (video inpaint where supported). Image studio as the *surgical instrument* for video.

## 6. Competitive Position

*(Verified findings and citations in the [research report](../research/2026-07-market-and-model-landscape.md); adversarially-verified July 2026.)*

The verified field has consolidated into two camps, and both commoditize model access — every major platform (LTX Studio, Higgsfield Canvas, FLORA, Krea) aggregates the same commercial roster (Veo 3.1, Seedance 2.0, Kling 3.0, Wan 2.6/2.7, FLUX.2, Nano Banana…). **Iris cannot differentiate on model breadth.**

- **Node-graph/canvas aggregators** (Higgsfield Canvas, FLORA, Krea): powerful routing, but assets are loose graph nodes — no persistent scene/character identity, no editing depth.
- **Storyboard-first suites** (LTX Studio — the closest structural analog: gen space + storyboard + timeline, hybrid in-house + partner models): its "Elements" consistency system is the market's best asset feature but is **paywalled at $35/mo**, and its timeline is AI-content-only (no camera footage; exports XML to Premiere for real finishing).
- **Incumbent NLEs**: Premiere 26.0 ships built-in Generative Extend (≤2s) — *simple clip extension is already a checkbox feature, not a differentiator.*

Verified gaps Iris occupies: **(1)** persistent scene/reference asset systems (unoccupied or paywalled everywhere; consistency mechanics converged on tiny per-generation ref counts — Runway 3, Midjourney 1 — plus train-once identities like Higgsfield Soul ID; @-mention invocation is the emerging standard), **(2)** continuation richer than per-clip conditioning — the best shipped is Flow's Extend (conditions on the prior clip's final second) and single-generation multi-shot world persistence (Sora 2, Runway Gen-4.5); Runway's own documented workflow is still manual last-frame chaining finished in an external editor; nobody chains shots as durable objects with carried refs/style/geometry, **(3)** multi-take management beyond "regenerate the node/cell" — no verified comparison/provenance UX anywhere, and practitioners independently name lost decision-context (prompt/refs/seed detaching from outputs) as *the* continuity killer, **(4)** a timeline that mixes AI and camera footage with real editing depth.

Two watch items temper the 3D claim: **Runway GWM-1** (world model with persistent scene memory and a multi-view SDK, partially productized) and **World Labs Marble** (coarse-3D-blockout-controlled world generation with pixel-accurate camera rendering, GA) show the capability arriving — but as APIs and world-builders, not integrated production workflows. Iris's precise differentiation is 3D-guided, reference-carrying continuation *wired into scenes/shots/takes/timeline*; the window is quarters, not years, which argues for shipping the depth/3D workflow in Phase 0, not deferring it. (Marble's mesh/splat exports are also a candidate *input* for Iris Set blockouts.) Also instructive: **the Sora social app was discontinued in March 2026** after failing to retain users — the market is rewarding production workflow tools over feed-based generation toys, which is Iris's bet.

Two strategic consequences adopted from the research:
- **Quality-gap strategy:** the best downloadable open-weight video models trail the closed API leaders by ~250 Elo, and top Wan versions (2.6/2.7, arena #2) are closed-API while we build on open Wan 2.2. So the hybrid design is load-bearing: *draft and iterate on Iris models (cheap, capability-rich — depth/3D following, multi-view, in-gen lip sync), master hero shots on commercial APIs where their look wins.* Watch item: Alibaba releasing Wan 2.6/2.7 weights would largely close the gap and gives the R&D stack an upgrade path.
- **Pricing wedge:** the mid-market serious tier is ~$35/mo with commercial licensing as the gate (LTX), and LTX paywalls its consistency system. Iris does the opposite — **the asset/consistency system is core-tier** (it *is* the retention loop); monetize compute, master-quality renders, commercial licensing, and premium partner models (Krea's "≈N flagship videos/month" framing is the legibility model to copy).

## 7. Scope & Phasing

**Phase 0 — Internal alpha (dogfood core).** The six signature workflows end-to-end with our models only. Image studio: layers/masks/selections/gen-fill/refs/promote-to-asset. Video studio: timeline with trim/transitions/audio basics/captions/export, shots/takes/continuation. Library: scenes, sets, views, characters, lineage. Single user per project. Rough edges acceptable everywhere except data safety and take provenance.

**Phase 1 — Private beta (light pros).** Commercial API adapters (Seedance, Nano Banana, +2–3 more) with BYO keys; capability-manifest UI; color tools and audio polish; proxy playback maturity; onboarding, templates, and progressive-disclosure pass; billing/metering; project export.

**Phase 2 — Public launch.** Review links with frame comments (pre-collab); workspace roles; performance hardening (desktop shell decision point); marketplace of style/prompt presets; semantic library search.

**Phase 3 — Collaboration & scale.** Real-time co-editing (data model prepared from day one — see technical design); shared workspace libraries with permissions; team review workflows.

**Non-goals through Phase 2:** real-time collab editing, mobile apps, broadcast/HDR delivery, EDL/AAF/OTIO interchange, plugin SDK, local-GPU inference.

## 8. Success Criteria

- **Phase 0:** our team produces a complete multi-scene story (≥3 min finished) entirely in Iris — no external editor, no manual file shuttling between generation and editing. Time from script to finished scene measurably drops vs. our current pipeline.
- **Phase 1:** a beta user with Premiere experience ships a real commercial/web-series episode; >50% of their generations use library refs (proof the asset system is load-bearing, not decorative); take fan-out is used on >70% of shots.
- **Ongoing product health:** median time from "regenerate this shot" to "new take selected in timeline" under 2 minutes of user attention (queue time excluded); zero data-loss incidents.

## 9. Key Risks

| Risk | Mitigation |
|---|---|
| **Scope: two pro editors + a DAM + an orchestration engine is 3–4 products.** | Baseline features are ruthlessly tiered (see research doc's table-stakes analysis); everything not on a signature workflow's path is deferred. Dogfooding forces honest prioritization. |
| **Model churn:** commercial APIs and open-weight SOTA change monthly. | Capability-manifest abstraction isolates churn to adapters; our own models guarantee a stable core capability floor. |
| **Continuity chains create staleness cascades** (re-pick shot 2's take → shots 3–5 stale). | Explicit stale-state in the model; regeneration is cheap and queued, never automatic without consent; users can pin/freeze downstream shots. |
| **Browser performance ceilings** (4K playback, large canvases). | Server-side preview rendering + proxies from day one; WebGPU/WASM where it pays; desktop shell held as a Phase 2 option, architecture kept shell-ready. |
| **GPU cost of fan-out-by-default.** | Draft-quality tier for takes (lower res/steps) with upscale-on-select; per-project budgets and cost surfacing; BYO keys shift marginal cost for external users. |
| **Repeating the last project's failure mode** (R&D entanglement). | Iris consumes models strictly through the endpoint/manifest API boundary. No model research code in this repo — ever. |

## 10. Open Questions

*(Updated July 2026 after review — items 3–5 resolved or parked.)*

1. **Desktop timing** *(open)*: ship web-only through Phase 1 and decide on a desktop shell (Tauri/Electron) from measured performance pain, or commit earlier? *Current lean: decide at Phase 2 from data.*
2. **3D authoring depth** *(open)*: is in-app 3D blockout (place primitives, set cameras) Phase 0, or is importing externally-authored scenes (Blender) enough for dogfooding? *Current lean: import + camera authoring first; blockout later.*
3. **Audio generation scope** — *decided:* audio tracks and audio generation are in scope. Integrate commercial audio APIs where they excel — ElevenLabs et al. for voice/TTS, plus music/SFX generation APIs — rather than building in-house.
4. **TTS/voice** — *decided:* ElevenLabs is the current voice/TTS provider. The dialogue workflow is: generate voice audio (ElevenLabs) → feed it into video generation as an audio reference for in-generation sync. Seedance already supports audio-conditioned sync; the R&D team is adding it to the Wan stack (in progress). Post-hoc lip sync remains the fallback for models without audio conditioning.
5. **Pricing shape** — *parked:* business/pricing questions are deliberately out of scope for now (we're a ways from that). The verified competitor pricing benchmarks live in the research doc (§2.2, §3.4) for when the question opens.

---

*Next: [02 — UI/UX Design](02-ui-ux-design.md) translates the pillars and workflows into information architecture, layout, and interaction patterns; [03 — Technical Design](03-technical-design.md) covers frontend/backend architecture, the generation pipeline, and the data model in depth.*
