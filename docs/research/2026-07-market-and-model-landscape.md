# Iris — Market & Model Landscape Research

**Date:** 2026-07-08 · **Status:** Rounds 1 & 2 integrated
**Method:** Multi-agent deep research, two rounds — round 1: 5 angles → 22 sources → 110 claims → top 25 adversarially verified (3 independent verifiers/claim; 2/3 refutes kill): 22 confirmed, 3 refuted. Round 2 (targeted gaps): 26 sources → 130 claims → 25 verified: 21 confirmed, 2 refuted, 2 verification-incomplete. All pricing/roster claims checked against live primary pages on 2026-07-08.

**Confidence labels used below:** ✅ = adversarially verified (3-0 or 2-0 votes) · ◐ = extracted from a primary source but below the verification budget (single-source; treat as probably-true, spot-check before betting on it) · ✗ = refuted.

> **Coverage note:** Verified claims concentrate on LTX Studio, Higgsfield (Canvas + Soul ID), FLORA, Krea, Runway, Google Flow/Veo, Sora 2, Midjourney, Luma, the Artificial Analysis arena, the Qwen-Image ecosystem, and Adobe. First-party API pricing (BytePlus, Alibaba, Google, OpenAI, BFL) and the traditional-editor tiering are ◐ (primary vendor pages, unverified pass). Descript, Canva, Veed, Kapwing, Hailuo app, Freepik, Weavy produced no surviving claims in either round — they are omitted rather than guessed at.

---

## 1. Executive Summary

1. **Model access is commoditized.** Every major AI-creative platform verified (LTX Studio, Higgsfield Canvas, FLORA, Krea) is a multi-model aggregator bundling the same commercial roster — Veo 3.1, Seedance 2.0, Kling 3.0, Wan 2.6/2.7, FLUX.2, Nano Banana Pro, GPT Image 2.0, Sora 2, Runway Gen-4.x, LTX-2.x, Ray 3.2, Vidu, Hailuo 2.3. *Iris cannot differentiate on model breadth.* Differentiation must come from its in-house model capabilities (image/video/audio refs, depth-map 3D scene following, multi-view, in-gen lip sync) and from workflow/asset-management design. **(high confidence, 3-0 × 6 claims)**
2. **The workflow-paradigm field has consolidated into two camps:** infinite-canvas/node-graph aggregators (Higgsfield Canvas, FLORA, Krea) vs. storyboard-first suites (LTX Studio). Nobody verified occupies "real NLE + real image editor + persistent asset system" — Iris's intended position.
3. **LTX Studio is the closest structural analog** (gen space + storyboard + timeline; hybrid in-house LTX-2 models + commercial partners). Its "Elements" saved-asset consistency system is the market's most explicit reference/asset feature — **and it's paywalled at the $35/mo tier**. Its timeline handles AI-generated shots only (no camera-footage import; exports XML to Premiere/Resolve for finishing). **(high, 3-0 × 3)**
4. **Sequence continuation is primitive everywhere verified:** first/last-frame conditioning plus manual frame-passing (users hand-feeding a clip's last frame into the next generation) is the state of the art in these products; Adobe's built-in Generative Extend caps at ~2s. No verified competitor carries scene context, 3D geometry, refs, or multi-view state across shots. **(high for what exists; the "nobody does it" gap claim is medium — absence of evidence, round 2 is actively hunting counter-evidence)**
5. **The open-weight video quality gap is real and quantified:** best downloadable open-weight video model (LTX-2.3 Fast, Elo 977) sits ~250 Elo behind the closed leader (Seedance 2.0 720p, Elo 1,224) on Artificial Analysis's blind arena. The Wan line Iris builds on ranks near the top (**Wan2.7-260612: #2 t2v-with-audio, Elo 1,161**) — but 2.6/2.7 are **closed-API**; only Wan 2.1/2.2 are downloadable. **Implication: Iris's hybrid design (self-hosted + BYO commercial APIs) is validated as necessary, not optional.** **(high, 3-0)**
6. **Qwen-Image is a sound foundation:** Apache 2.0 (weights *and* code, verified on the HF card), a fast-moving in-family editing line (Edit-2509 multi-image → Edit-2511 character consistency → Image-2512 → Image-2.0 native 2K, Feb 2026), and a community ControlNet ecosystem (InstantX inpainting/outpainting adapter, diffusers + ComfyUI native). Character consistency and multi-image reference are improving in-family, reducing Iris's in-house extension burden. **(high, 3-0 × 5)**
7. **Incumbents are absorbing basic AI continuation:** Premiere 26.0 ships built-in Firefly Generative Extend (GA since Apr 2025; ≤2s video, ≤10s ambient audio, 4K, no dialogue/music). Simple last-frame extension is table stakes, not a moat. **(high, 3-0)**
8. *(Round 2)* **Continuation state of the art:** Flow's native Extend (conditions on the previous clip's final *second*) and model-level multi-shot world persistence (Sora 2 ✅, Runway Gen-4.5 ◐) are the best shipped mechanisms; Runway's *documented* workflow is still manual last-frame chaining finished in an external editor. Nobody chains shots as durable objects with carried refs/geometry. **(✅)**
9. *(Round 2)* **The 3D gap is real but narrowing:** Runway GWM-1 (persistent scene memory, multi-view SDK, partially productized) and World Labs Marble (coarse-3D-controlled worlds, pixel-accurate camera rendering, GA) exist — as APIs/world-builders, **not** integrated production workflows. Iris's precise claim: 3D-guided, reference-carrying continuation *wired into scenes/shots/takes/timeline*. **(◐)**
10. *(Round 2)* **The Sora app is dead** (discontinued Mar 2026; API sunsets Sep 24, 2026): social-feed AI video failed to retain; production-workflow tools won. Do not build a Sora adapter. **(✅)**
11. *(Round 2)* **First-party API costs** put commercial video masters at $0.35–1.90 per 5s clip (Seedance/Wan/Veo band), with audio-in-generation ~2× silent — validating draft-locally/master-selectively economics. Alibaba's newest Wan API generation **dropped first/last-frame conditioning**, so Iris's Wan-2.2 conditioning stack isn't replicable via their API. **(◐)**

## 2. Pillar 1 — AI Editing Product Landscape

### 2.1 Verified product profiles

| Product | Paradigm | Models | Asset/consistency system | Continuation | Pricing |
|---|---|---|---|---|---|
| **LTX Studio** | Storyboard-first suite: gen space + storyboard workspace + timeline editor | In-house LTX-2/2.3 Fast/Pro **+** Veo 2/3.1, Nano Banana Pro/2, FLUX.2 Pro, Z-Image, Kling, Seedance, ChatGPT Image 2.0 | **Elements**: saved characters/objects/logos/styles, @-tagged across shots — the market's most explicit consistency system; **gated at $35 Standard tier** | Storyboard cell regeneration; timeline is AI-content-only (no camera footage import; XML export to Premiere/Resolve) | Free $0 (800 one-time credits, watermark, personal-use) · Lite $15 (8k cr) · Standard $35 (28k cr, commercial license, Elements, premium partner models) · Pro $125 (110k cr, Veo 3.1 variants) · ~20% yearly discount |
| **Higgsfield Canvas** | Node-graph infinite canvas: prompts/images/refs as connected nodes; "a whole campaign or scene becomes one continuous pipeline" | Soul 2.0 (in-house) + Seedance 2.0, Kling 3.0, Wan 2.7, Veo 3.1, Nano Banana Pro, GPT Image 2.0 | Assets are loose graph nodes; no verified persistent scene/character library | First/last-frame conditioning ("Two frames in, one seamless video out"); tutorials show users *manually* passing last frames between scenes | (not in verified set) |
| **FLORA** | Pure infinite node-canvas aggregator (no timeline, no storyboard) | 50+ models — deepest verified roster: Veo 3.1 variants, Sora 2 Pro, Kling 3.0/O3, Seedance 1.5/2.0, Marey, Gen-4.5, Wan 2.6/2.7, FLUX.2, Nano Banana Pro/2, Seedream 5.0, Recraft V4, SD 3.5 | Graph nodes only | Node re-execution | Single-plan aggregation (details not in verified set) |
| **Krea** | Multi-tool generation studio (the claimed free-tier "Krea Nodes" node editor was **refuted 0-3** — do not rely on it) | All video models on Pro: Wan 2.1–2.6, Seedance 2.0, Veo 3/3.1, Gen-3/4/4.5, LTX-2.3 22B, Ray 3.2, Vidu Q2/Q3, Hailuo 2.3 | (not in verified set) | (not in verified set) | Compute units: Free 100/day (~1 Nano Banana 2 image; no video concurrency) · Basic 5,000/mo (~20 Seedance videos) · Pro 20,000/mo (~83, all video models) · Max 60,000/mo default slider (~250, unlimited concurrency) · 40% yearly discount · no rollover · top-ups expire in 90 days |

### 2.1b Round 2 — continuation / consistency / multi-take UX in the AI-video leaders

**Runway** (all ✅ from official help docs)
- *Consistency:* Gen-4 Image References — max **3 active refs per generation**; characters/objects/styles/environments; claims consistent characters "from just a single reference image," no subject training. Refs are session-temporary by default; naming one saves it across sessions, invoked via **@-name autocomplete** in prompts, shareable workspace-wide. → Second market validation (after LTX Elements) of Iris's @-mention reference chips; Runway's "temporary by default, save deliberately" is the *opposite* of Iris's catalog-first Set/View model.
- *Continuation:* documented method is **manual last-frame chaining** — extract final frame via "Use frame → Input for video," use as the next clip's start image, align shared frames *in an external editor*. No native chain object. Act-Two performance capture: single character only, ≤30s; the official multi-character dialogue workflow requires compositing per-character outputs **in a local third-party editor** (opacity/feathered overlays). → Runway officially outsources exactly the two jobs (chaining, compositing) Iris internalizes.
- ◐ *Gen-4.5 update (Dec 2025, TechCrunch):* native audio + long-form **multi-shot generation** — one-minute videos maintaining character consistency across shots, multiple angles, native dialogue; available on all paid plans.
- ✗ Refuted (0-3): the claim that Runway's spatial control is only 2D sketch references — see GWM-1 below.

**Google Flow / Veo 3.1** (✅ from blog.google + labs.google)
- *Continuation:* **native Extend** — each new segment is conditioned on the **final second** (not just final frame) of the previous clip, extendable to a minute-plus; "Frames to Video" = first+last-frame bridging; "Scenebuilder" for scene assembly; "Jump-to" cuts.
- *Consistency:* "Ingredients to Video" — multiple reference images controlling characters/objects/style, with generated audio in Veo 3.1.
- *Models:* Flow runs Veo 3.1 (video), Nano Banana (image), and "**Gemini Omni**" — "create and edit videos from any input reference" — a reference-driven video *editing* model beyond Veo.
- *Pricing:* Free 50 credits/day · AI Plus $4.99 (200/mo) · AI Pro $19.99 (1,000/mo) · AI Ultra $99.99 (10k) / $199.99 (25k).
- → Flow is the strongest continuation UX verified, and the nearest thing to Iris's chain concept — but per-clip conditioning only: no persistent scene/set/character *library* spanning projects, no take management, no real NLE.

**OpenAI Sora 2** (✅)
- *Model-level continuity:* Sora 2 natively supports **multi-shot prompting with persistent world state** — one generation spanning multiple shots with the scene held consistent. This is the model-level alternative to frame-chaining, and the pattern to watch (Runway Gen-4.5 now does the same ◐).
- *Consistency:* "characters" (née cameos) — one-time video+audio capture creates an insertable identity; OpenAI states the capability generalizes to any human/animal/object.
- **✗→⚠ The Sora app is dead:** the social create-and-remix app was **discontinued March 24, 2026** (experience ended Apr 26; downloads fell 3.3M/mo peak → 1.1M by Feb 2026; ~$2.1M lifetime revenue). Sora 2 survives as a model inside ChatGPT, **and the Sora API sunsets September 24, 2026.** → Market signal: social-feed AI video failed to retain; production-workflow tools are where the value is. Practical consequence: **do not build a Sora adapter** (see Gap B).

**Midjourney** (✅ from official docs)
- *Consistency:* Omni Reference — inject a character/object/vehicle/creature from a reference image; **only ONE reference per prompt** (multi-character requires a single image containing both); tunable `--ow` 1–1,000 (default 100, ≤400 advised), "Omni Strength" slider on web.
- *Cost/UX walls (2-0):* Omni Reference costs **2× GPU time** and is incompatible with inpainting/outpainting, Vary Region, Pan, Zoom Out, Fast/Draft/Conversational modes — using the consistency feature walls you off from the editing/iteration UX. → Anti-pattern for Iris: reference-conditioned generation must not disable the surgical tools; and note *nobody verified supports >4 refs* (Runway 3, Midjourney 1, Iris manifest: 4 image + video + audio).

**Higgsfield Soul ID** (✅ ×2, ⚠ on the video half)
- *Consistency:* a **trained identity** (from ~20+ photos, minutes of training) rather than per-generation reference matching — "an internalized model of the face itself, not a lookup reference to a single image." Trained once, reusable "across every project and every model on Higgsfield indefinitely" — including Seedance 2.0, Veo 3.1, Kling 3.0, Wan 2.6. → Proof that an *aggregator can layer its own cross-model consistency system on top of third-party models* — structurally what Iris's Character entity does. Caveat: the claim that Soul ID applies automatically to *video* generation was contradicted by Higgsfield's own persona docs (1 verifier; others hit limits) — treat the video path as image-ref-mediated.
- → Strategic note for Iris: train-once identity (LoRA-style) vs. reference-image conditioning is a real UX fork. Iris's Character entity should be able to hold *both* (ref bundle now; optional trained-embedding slot later on our own models).

**Luma Dream Machine** (verification incomplete — 1 verifier confirmed, 2 hit limits; treat as ◐)
- Commercial use + watermark removal gate at Plus $29.99/mo (10k credits, adds HDR); Lite $9.99/mo (3,200 credits) is non-commercial and watermarked despite 4K upscaling.

**Pika** (◐, fal.ai model page + pricing tracker)
- *Continuation:* **Pikaframes** — interpolation across **2–5 user keyframes**, per-segment motion prompt + transition duration, ≤25s total, 720/1080p. Frame-level control, no scene memory. Paid-only feature; free tier 480p. Tiers: Free/$8/$28/$76 (annual); watermark-free + commercial only from Pro $28. Pikaformance audio-driven lip sync: 3 credits/sec, all plans.
- → Multi-keyframe conditioning (not just first/last) is worth exposing in Iris's manifest schema — our models' multi-frame conditioning maps onto it.

**Kling** (◐, official quickstart + site)
- *Control:* Motion Brush — animate up to **6 elements with drawn trajectories** (hard constraints) + Static Brush to pin backgrounds/suppress camera motion. Site promotes Video 3.0 Motion Control, **Element Library 3.0** (a saved-elements consistency system — a third Elements-style validation), Omni variants, claimed "world's first cinema-grade native 4K" model.

### 2.1c The 3D/world-model counter-evidence (revises round 1's "nobody does 3D")

Round 1 found no competitor doing 3D/depth-guided generation. Round 2's targeted hunt **did** find the frontier moving (both ◐ — primary announcements, unverified pass):

- **Runway GWM-1** (Dec 11, 2025): general world model built on Gen-4.5; explicit **persistent scene memory** ("turn around, and what was behind you is still there"); **multi-view video generation** in the SDK (framed for robotics); three variants (Worlds / Avatars / Robotics); ≤2 min, 720p, real time; action-conditioning on camera pose, events, speech. Partially productized: Avatars ("Runway Characters") is live in the API; Worlds/Robotics in early access.
- **World Labs Marble** (GA Nov 12, 2025): persistent 3D worlds from text/images/video/**coarse 3D layouts**; renders to video with **pixel-accurate camera control** + an AI "enhance" pass that adds detail/motion while adhering to the 3D structure; **Chisel mode: block out coarse geometry (boxes/planes) as a spatial control signal** — nearly Iris's untextured-3D-following concept; exports Gaussian splats + meshes for standard pipelines.
- **Adobe** (Jan 2026, ✅-adjacent ◐): Photoshop's Reference Image in Generative Fill is now "geometry-aware" (reorients objects to scene scale/rotation/lighting/perspective) — geometry-awareness reaching mainstream image editing.

**Revised gap statement:** *purpose-built world models exist and are partially productized, but none are integrated into a production editing workflow* — GWM-1 is an API/SDK aimed at robotics + explorable worlds; Marble is a world-builder that exports to other tools. Iris's differentiation narrows from "nobody has 3D-guided generation" to **"nobody has 3D-guided generation wired into scenes/shots/takes/timeline"** — still real, but the window is measured in quarters, not years. Marble is also a candidate *complement*: its mesh/splat exports could feed Iris's Set 3D blockouts.

### 2.2 Verified pricing patterns (for Iris's Phase 1+ pricing)

- **LTX pattern:** advanced *features* (camera controls, audio-to-video, v2v, even the in-house model) are free-tier acquisition bait; monetization sits on **commercial-use licensing, watermark removal, partner-model access, and the consistency/asset system**.
- **Krea pattern:** plans denominated in compute units, communicated as "~N videos of the flagship model"; **concurrency and model access as tier levers**; expiring top-ups.
- **Implication for Iris:** the going mid-market rate for a serious tier is $35/mo (LTX Standard) with commercial licensing as the gate. Iris's asset system being *core and free-ish* (vs. LTX paywalling Elements) is a viable positioning wedge; monetize compute, master-quality renders, and commercial licensing instead.

### 2.3 Practitioner pain points (directional, from gap-finding angle)

The strongest thematic signal (Rewake studio essay, practitioner guides): **continuity breaks because the workflow loses decision context** — prompts drift and detach from the shots they produced; a "take" loses the prompt/refs/seed that made it. This is precisely Iris's take-provenance + lineage design. *(Directional — these sources fed claims that weren't all individually verified.)*

## 3. Pillar 2 — Model Landscape

### 3.1 Video: the arena picture (Artificial Analysis, verified live 2026-07-08)

| Fact | Detail |
|---|---|
| Closed leader | Dreamina **Seedance 2.0** 720p — Elo 1,224 t2v (its claimed *dual* #1 in t2v **and** i2v was refuted 1-2; treat as #1 in t2v-with-audio context only) |
| Wan trajectory | **Wan2.7-260612** (Jun 2026, $9.00/min API): **#2 t2v-with-audio, Elo 1,161** (CI rank 2–3); Wan 2.7 #7 t2v (1,104), #4 i2v (1,096); up from Wan 2.6's 1,028/897 in Dec 2025 — steep improvement curve |
| Open-weight reality | **No open-weights badge on Wan 2.6/2.7** — official Wan-AI HF hosts only 2.1/2.2. Only HF open-weights models in the t2v top 25: **Lightricks LTX-2/2.3 family** (best: LTX-2.3 Fast, rank 20, Elo 977, $2.40/min) |
| The gap | **~247 Elo** between best downloadable (977) and closed leader (1,224) |

**Implications for Iris:**
- Self-hosted video (customized Wan 2.2) will **trail commercial APIs on raw quality**. The hybrid architecture isn't a nice-to-have — it's how Iris stays competitive on output quality while keeping the *capability* moat (depth/3D following, multi-view, in-gen lip sync, refs) on the self-hosted tier.
- A rational default: **draft/iterate on Iris models (cheap, capability-rich), master/hero shots optionally on commercial APIs** where their look wins — the capability manifest already supports this.
- **Watch item:** if Alibaba ships Wan 2.6/2.7 weights (consistent with its 2.1/2.2 pattern), the gap largely closes and the R&D stack has an upgrade path. This single event materially strengthens the self-hosted tier.
- LTX-2.3 (22B, open) is the strongest candidate for an additional self-hosted "generic" video endpoint alongside the Wan-derived stack.

### 3.2 Image: the Qwen foundation (all 3-0)

- **License:** Apache 2.0 on the official HF weights card (not just repo code) — commercial self-hosting, in-house modification, no output restrictions for locally-run instances. (The 20B-MMDiT spec claim was refuted 1-2 — don't cite a parameter count.)
- **In-family editing line:** Qwen-Image-Edit (Aug 2025) → **Edit-2509** (multi-image input) → **Edit-2511** (Dec 2025: character consistency, LoRAs baked in, multi-person fusion) → **Qwen-Image-2512** (Alibaba's own AI-Arena claims "strongest open-source" — first-party benchmark, treat accordingly; independent commentary treats FLUX.2 [dev] as the open-weight benchmark) → **Qwen-Image-2.0** (Feb 2026, native 2K, unified gen+edit).
- **Ecosystem:** InstantX Qwen-Image-ControlNet-Inpainting (Apache 2.0; object replacement, text modification, background replacement, outpainting; native diffusers `QwenImageControlNetInpaintPipeline` + ComfyUI workflows). Single-lab adapter, prompt-sensitive quality — but it means Iris's inpaint/outpaint modes don't start from zero.
- **Implication:** track the in-family line closely — consistency/multi-ref improvements arriving upstream reduce the in-house extension burden; plan for rebasing the custom capabilities onto newer Qwen-Image releases.

### 3.3 Commercial API availability (medium confidence, 2-1)

Veo 3.x is servable to third-party apps via fal.ai (native synced audio incl. lip-synced dialogue; $0.20–0.40/s at 720/1080p, to $0.60/s 4K+audio; t2v, i2v, extend, reference-to-video) — **no Google platform lock-in**; use the `veo3.1` endpoint (original `veo3` endpoints deprecated). LTX/Krea reselling Seedance 2.0 and Kling 3.0 under commercial licenses confirms ByteDance/Kuaishou frontier models are third-party-available.

### 3.4 First-party API capability & pricing matrix (round 2 — all ◐, live vendor pricing pages 2026-07-08)

**Video APIs**

| Model / vendor | Modes & capabilities | Price (5s reference clip unless noted) |
|---|---|---|
| **Seedance 2.0** (BytePlus ModelArk, `dreamina-seedance-2-0-260128`) | t2v, i2v, **video input 2–15s** (continuation/extension-style, billed in tokens); up to 4K; Fast + Mini variants (no 1080p); draft mode only on 1.5 Pro; failed gens not billed | $0.35 (480p) · $0.76 (720p) · $1.87 (1080p) · $3.89 (4K); Fast: $0.28/$0.60; with video input roughly +10–140% by input length |
| **Seedance 1.5 Pro** | **native audio at 2× the silent rate**; draft mode (0.6–0.7× factor) | audio 2.4 vs silent 1.2 USD/M tokens |
| **Wan 2.6/2.7** (Alibaba Model Studio, Intl) | t2v, i2v (**native audio; silent = half price** on i2v-flash), **r2v reference-to-video** (input video ≤5s billed), **`wan2.7-videoedit`** v2v editing; max 1080p (no 4K tier). **First/last-frame (kf2v) exists only on wan2.2/2.1 models** — the newest Wan API generation has no priced first+last-frame mode | t2v: $0.10/s 720p, $0.15/s 1080p (= $0.50–0.75 per 5s); i2v-flash silent: $0.025/s 720p; kf2v-flash (2.2): $0.015–0.07/s |
| **Veo 3.1** (Gemini API) | t2v/i2v with audio default; Standard/Fast/**Lite** tiers; 4K on Standard/Fast; Veo 3 & 2 **deprecated, shut down June 30, 2026** | Standard $0.40/s (720/1080p), $0.60/s 4K · Fast $0.10–0.30/s · Lite $0.05/s 720p |
| **Sora 2 / 2 Pro** (OpenAI) | 720p (base) / ≤1080p (Pro); batch at 50% | $0.10/s base 720p; Pro $0.30–0.70/s — **but the Sora API sunsets 2026-09-24; do not build on it** |
| **Pika 2.5 (via fal)** | Pikaframes 2–5 keyframes, ≤25s | $0.04/s 720p, $0.06/s 1080p (aggregator resale) |

**Image APIs**

| Model | Notes | Price |
|---|---|---|
| **Nano Banana Pro** (Gemini 3 Pro Image) | 1K/2K/4K; batch half-price; input images $0.0011 | $0.134 per 1K/2K image · $0.24 per 4K |
| **Nano Banana** (Gemini 2.5 Flash Image) | low-cost editing tier | $0.039/image ($0.0195 batch) |
| **GPT Image 2 / 1.5 / 1-mini** (OpenAI) | gpt-image-2 has superseded 1.5 at the top; token-priced | output $30–32/M tokens; mini $8/M |
| **Seedream 5.0 Pro** (BytePlus) | **multi-reference input: first ref free, +$0.003 per additional** — per-ref pricing signals multi-ref is a first-class API concept | $0.045/image ≤2.36MP, $0.09 above; Lite $0.035 |
| **FLUX.2 family** (BFL) | five tiers (flex/pro/max/klein 4B/9B) + dedicated **Outpainting, Eraser, VTO endpoints**; pure pay-as-you-go, resolution-dependent | calculator-based, no flat list |
| **Qwen-Image 2.0 / Edit** (Alibaba, for comparison vs self-hosting) | hosted versions of Iris's base family | $0.035–0.075/image |
| **Lyria 3** (Google, music) | 30s clip / full song | $0.04 / $0.08 |

**Licensing finding that matters for Iris's roster (◐):** BFL sells **commercial self-hosting licenses** for its open weights — Builder (FLUX.2 klein, 10K img/mo, fine-tune + LoRA rights), Platform (adds FLUX.2 [dev], 100K/mo), Professional (3 domains), plus a Synthetic-Data license (rights to use outputs as training data — relevant if the R&D team wants FLUX outputs for training). **FLUX.2 [dev] open weights are not free for commercial volume use** — unlike Apache-2.0 Qwen-Image/Wan 2.2. Any "self-hosted open-weight" roster addition needs a license check per model; Iris's own stack being Apache-clean is a real cost/simplicity advantage.

**Cost modeling takeaways:**
- Commercial video masters cost **$0.35–1.90 per 5s clip** at production resolutions (Seedance/Wan/Veo-Fast band) — fan-out×4 at master quality on commercial APIs is $1.40–7.60 per shot, which validates the draft-locally-master-selectively economics.
- Audio-in-generation doubles cost on both Seedance 1.5 and Wan i2v — silent drafts for takes, audio on the selected master.
- **Alibaba's own API dropping first/last-frame from its newest Wan generation** means Iris's Wan-2.2-based conditioning stack (first/last/depth) isn't replicable via their API — a small but genuine self-hosted capability moat.

## 4. Pillar 3 — Traditional Editor Baselines

Round 1 verified: **Premiere 26.0's built-in Generative Extend** — ≤2s video / ≤10s ambient audio per extension, 4K + vertical, cloud-based, won't extend dialogue/music, GA since 25.2 (Apr 2025). Standing implication: **"extend my clip a little" is an incumbent checkbox.** Iris's continuation story must be the richer one — context/ref/geometry carry-over across *shots*, not frame-padding within a clip.

### 4.1 Round 2 — landscape & pricing (◐, vendor pages + practitioner comparisons)

| Editor | Price | Notes relevant to Iris |
|---|---|---|
| DaVinci Resolve 21 | **Free** / Studio **$295 one-time** | Free tier is a full NLE; Studio gates HDR toolset, advanced NR, collaboration, and **all AI/Neural tools** (incl. Magic Mask 2). Resolve 21 adds a **Photo page** (video+still convergence — incumbents moving toward Iris's integrated thesis), AI IntelliSearch (people/content search in footage), AI Speech Generator (TTS incl. voice cloning), **direct publish to YouTube/TikTok/Vimeo/X**, square/vertical presets |
| Final Cut Pro | $299 one-time (12.2) | Magnetic Mask; easiest/fastest AI masking in hands-on tests, but weakest on hair/shadows |
| Premiere Pro 26 | subscription (~$23–60/mo depending on plan; one source's $59.99 figure is flagged as needing verification) | Object Mask (weakest of the three in hands-on testing); Generative Extend |
| CapCut | Free / $7.99 Pro | template-heavy AI assist, basic auto-captions — the floor Iris must clear, not the bar |
| Photoshop 2026 | subscription | see below |

**AI masking is now cross-NLE table stakes** (Larry Jordan hands-on, Apr 2026): FCP Magnetic Mask, Premiere Object Mask, Resolve Magic Mask 2 — all one-gesture create + AI track + manual refine. Quality ranking in testing: Resolve best (but hardest UI, ~2× slower tracking), FCP fastest/easiest, Premiere weakest.

**Photoshop 2026 practitioner reality (◐):**
- The **Remove Tool is the most-used AI feature among working pros** — daily cleanup (blemishes, power lines, sensor dust), not full generative synthesis.
- Generative Fill (Firefly Image 4, now 2K output): strong at background extension/mid-complexity removal; still fails at pattern continuity and in-fill text; pros use it as an **accelerator, not a finisher** ("cut background-extension time ~70%, but I still paint over every fill by hand").
- Improved Select Subject (hair/translucents): rough-cutout time 30–45 min → 2–3 min. AI selection is a core productivity feature, not a gimmick.
- Generative Expand's killer use is **aspect-ratio adaptation** of one asset across deliverables (feed/story/landscape).
- Jan 2026 additions: Clarity/Dehaze/Grain adjustment layers (non-destructive system still expanding), geometry-aware Reference Image in Gen Fill, Dynamic Text on paths (beta).

### 4.2 Table-stakes / differentiator / deferrable (for light pros cutting commercials & web series)

**Table stakes (Iris must have; users leave without these):**
- NLE: multi-track timeline, full trim suite, transitions, keyframable motion/effects, **auto-captions/transcription** (every NLE has it), audio mixing with voice cleanup, LUT-based color + basic wheels, proxy playback, social + master export presets (incl. 9:16/1:1), **AI subject masking** (now universal), direct social publish (Resolve has it; cheap goodwill).
- Image: layers/groups/blend modes, masks, adjustment layers, marquee/lasso + **AI subject select**, transform/warp, brush/clone/heal (the *most-used* daily tools), crop/canvas, text, sharpen/blur/noise filters, non-destructive workflow, generative fill/expand at credible quality.

**Differentiators (praised, drive choice):**
- Text-based/transcript editing (Descript's thesis, now spreading); shot-match color; voice isolation quality; render-in-place; the *speed* of AI masking; Resolve's one-time pricing vs subscription fatigue (relevant to Iris pricing psychology).

**Deferrable for Iris MVP (pros at this tier rarely touch):**
- EDL/AAF/OTIO interchange (they finish in-app), HDR grading (Studio-gated even in Resolve; light pros deliver SDR web), 10-bit broadcast delivery, advanced multicam, scripting/plugin SDKs, stereoscopic. *(Matches the HLD's non-goals list — no changes needed.)*
- Caveat: Fairlight-depth audio and Resolve-depth color are *differentiators for editors who know them* but are explicitly not Iris's Phase 0/1 fight; LUT + wheels + match + ducking + voice cleanup covers the commercial/web-series delivery bar.

## 5. UX Patterns — Adopt / Avoid (verified subset)

**Adopt:**
- **LTX's three-workspace model** (generate / storyboard / timeline) validates Iris's Story–Studios separation; Iris's edge is that its timeline is a *real* NLE (mixed AI + camera footage) rather than AI-only.
- **@-taggable saved assets** (LTX Elements) — the mechanic maps directly onto Iris's reference chips + `{character}`/`{set.view}` prompt slots.
- **Node-graph-as-lineage** (Higgsfield/FLORA): the *graph* is a good way to see how assets flow between generations. Iris's lineage view should offer this graph *as a read/inspect surface* over the real data model, without making users author in a node editor.
- **Krea's pricing legibility** ("≈ N videos of flagship model per month").

**Avoid:**
- **AI-only timelines** (LTX): breaks the moment a real production needs one camera pickup shot or stock clip.
- **Loose-node asset management** (canvas aggregators): assets as disconnected graph outputs with no persistent scene/character identity is exactly the "folder of disconnected outputs" failure Iris's Set/View/Character model fixes.
- **Paywalling the consistency system** (LTX Elements at $35): consistency is the product's core loop; gating it kills the habit that creates retention.
- **Manual frame-passing as the continuation story** (Higgsfield tutorials): making users hand-carry last frames between generations is the market's weakness, not a pattern.

## 6. Market Gaps & Strategic Implications (synthesis — medium confidence)

1. **Persistent scene/reference asset systems are the weakest, most monetizable layer.** Only LTX has a named system (Elements), and it's paywalled; aggregators have loose nodes. Iris's Scene/Set/View/Character library with lineage occupies an essentially unoccupied position.
2. **Sequence continuation beyond per-clip conditioning is unoccupied — but narrowing.** The best shipped continuation is Flow's Extend (conditioned on the previous clip's final second) and model-level multi-shot world persistence (Sora 2, Runway Gen-4.5) *within one generation*. Nobody chains shots as first-class objects with carried refs/style/geometry across generations. The world-model frontier (Runway GWM-1's persistent scene memory + multi-view SDK; Marble's coarse-3D-controlled worlds with pixel-accurate camera rendering) shows the capability arriving — **but as APIs/world-builders, not integrated production workflows**. Iris's claim is now precisely: *3D/depth-guided, reference-carrying continuation wired into scenes → shots → takes → timeline.* Window: quarters, not years.
3. **Multi-take/variant management is primitive everywhere verified** — regenerate-the-node or regenerate-the-cell, no comparison UX, no provenance. Iris's Take Picker + provenance is a differentiator with no verified peer. Practitioner sources independently name lost decision-context (prompt/refs/seed detaching from the output) as *the* continuity killer — take provenance is the fix.
3b. **Consistency mechanics have converged on two patterns Iris should span:** per-generation reference images (Runway ≤3 refs, Midjourney 1 ref, Flow Ingredients, Kling Elements, LTX Elements) and train-once identities (Higgsfield Soul ID). Reference counts are tiny everywhere; @-mention invocation is the emerging standard (LTX, Runway both). Midjourney's anti-pattern — consistency features that *disable* the editing/iteration tools and cost 2× — is the thing to never do.
4. **Paradigm synthesis:** borrow the node graph for *generation lineage inspection*, the storyboard for *narrative sequencing*, and the NLE timeline for *finishing* — LTX's three-workspace model validates the shape; nobody has fused it with real editing depth.
5. **Pricing wedge:** make the asset/consistency system core-tier; monetize compute, master renders, commercial licensing, and premium partner models.

## 7. Refuted Claims (do not rely on these)

| Claim | Vote | Note |
|---|---|---|
| Krea ships a free-tier node editor ("Krea Nodes" / Nodes Agent / App Builder) | 0-3 | Not on the live pricing page |
| Seedance 2.0 holds dual #1 (t2v **and** i2v) on Artificial Analysis | 1-2 | #1 verified only in t2v-with-audio context |
| Qwen-Image is a 20B-parameter MMDiT | 1-2 | Don't cite parameter count/architecture from this |
| Runway's spatial control is limited to 2D sketch references (no 3D work) | 0-3 | Refuted by GWM-1's existence (§2.1c) |
| The Sora app is a live invite-based iOS social app | 0-3 | **Discontinued Mar 24, 2026**; API sunsets Sep 24, 2026 |
| Soul ID applies automatically to video generation | 1-refute (2 verifiers cut off) | Higgsfield's own persona docs contradict the video half |

## 8. Open Questions (post round 2)

1. Will Alibaba release Wan 2.6/2.7 weights? (Single biggest swing factor for the self-hosted tier. No release-plan evidence surfaced in either round.)
2. How fast do Runway GWM-1 / Marble-class world models get *workflow* integration (into Runway's own editor, or via partnerships)? This is the clock on Iris's 3D-guidance window.
3. Descript/Canva/Veed/Kapwing/Freepik/Weavy produced no surviving claims — low priority (different segments), but Descript's transcript-editing UX deserves a hands-on look before Iris builds its own.
4. Practitioner "non-negotiables" (Gap C) came from comparison articles more than working-editor surveys; validate the table-stakes tiering against internal dogfooding before cutting anything close to the line.

## 9. Caveats

- **Time-sensitivity is high:** pricing pages, model rosters, and Elo rankings verified live 2026-07-08 can shift within weeks. The open-weight-gap conclusion inverts if Wan 2.6/2.7 weights ship.
- Several feature claims rest on vendor marketing pages — adequate for "what a vendor sells," weak for quality assertions.
- Qwen-Image-2512's "strongest open-source" ranking is Alibaba's own first-party benchmark.
- All ◐ items (first-party API pricing, GWM-1/Marble details, Photoshop/NLE tiering) come from primary vendor/announcement pages but did not go through the adversarial verification pass — spot-check any figure before it drives a signed commitment.
- Round 2's final synthesis agent and ~4 verifier votes were cut off by a session limit; the 21 confirmed claims stand (each independently 3-0/2-0), and the synthesis in §2.1b–c/§3.4/§4 was performed manually from the raw verified claims + journal.

## 10. Sources

### Round 2 (fetched & claim-bearing)

**Primary:** help.runwayml.com (Gen-4 Image References; Act-Two multi-character) · runwayml.com/research/introducing-runway-gwm-1 · blog.google/technology/ai/veo-updates-flow · labs.google/flow/about · openai.com/index/sora-2 · developers.openai.com/api/docs/pricing · help.openai.com (Sora discontinuation, article 20001152) · docs.midjourney.com (Omni Reference) · higgsfield.ai/blog (Soul ID) · lumalabs.ai/learning-hub (pricing) · kling.ai/quickstart (Motion Brush) · fal.ai/models/fal-ai/pika/v2.2/pikaframes · ai.google.dev/gemini-api/docs/pricing · docs.byteplus.com/en/docs/ModelArk (Seedance/Seedream pricing) · alibabacloud.com/help/en/model-studio/model-pricing · bfl.ai/pricing · blackmagicdesign.com/products/davinciresolve/whatsnew · blog.adobe.com (Jan 2026 Photoshop) · worldlabs.ai/blog (Marble; World API)

**Secondary:** techcrunch.com (GWM-1, Dec 2025; Sora shutdown, Mar 2026) · larryjordan.com (AI masking hands-on, Apr 2026) · photoshopnews.com (Photoshop 2026 AI features) · subclip.app (NLE comparison) · eesel.ai (Pika pricing) · buildmvpfast.com (API costs)

### Round 1 (fetched & claim-bearing)

**Primary:** ltx.studio/ltx-studio-alternatives · ltx.io/studio/pricing · help.ltx.io (Elements) · higgsfield.ai/canvas-intro · florafauna.ai → flora.ai · docs.flora.ai/models/video-models · krea.ai/pricing · artificialanalysis.ai/video/arena (+ t2v/i2v leaderboards) · fal.ai/video · fal.ai/models/fal-ai/veo3.1 · github.com/QwenLM/Qwen-Image (+ issue #98) · huggingface.co/Qwen/Qwen-Image · huggingface.co/Qwen/Qwen-Image-Edit-2511 · huggingface.co/Qwen/Qwen-Image-2512 · qwen.ai/blog (edit-2511) · huggingface.co/InstantX/Qwen-Image-ControlNet-Inpainting · huggingface.co/Lightricks/LTX-2.3 · helpx.adobe.com/premiere (what's new) · comfy.org

**Secondary/corroborating:** CineD (Jan 2026 LTX coverage) · PetaPixel (Premiere Generative Extend, Jan 2026 & Apr 2025) · Jim Clyde Monge / Medium (Higgsfield Canvas, Jun 2026) · Creative Bloq (FLORA) · CostBench/UsagePricing (Krea) · wavespeed.ai, runware.ai, replicate.com/blog, melies.co (model comparisons) · rewake.studio (continuity essay) · james-palm.medium.com (character-consistency guide) · subclip.app, larryjordan.com, toolfarm.com (NLE comparisons)
