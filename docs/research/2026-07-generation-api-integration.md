# Iris — Generation API Integration Comparison

*2026-07-10 — deep-research run wf_965a3957-5c8 (106 agents, 3-vote adversarial verification; findings cite live OpenAPI specs and vendor docs). Companion to [2026-07-market-and-model-landscape.md](2026-07-market-and-model-landscape.md), which holds the capability/pricing matrix (verified 2026-07-08). This doc adds the INTEGRATION view: what it costs to wire each API into our adapter seam, for the image-first dogfood.*

## 1. What our seam requires (the rubric)

An Iris backend is an adapter implementing `Client` (GetManifest / CreateJob / GetJob / CancelJob) plus a static capability manifest. Two proven patterns exist:

- **Async, Ark-style** (`adapters/seedance.go`): submit → poll remote task id → result URL → download → re-upload to our storage (custody bridge), error taxonomy incl. `safety_blocked`, post-send timeouts as taxonomy-transient.
- **Sync** (`adapters/elevenlabs.go`): one request returns the artifact bytes; CreateJob performs generation + custody and returns terminal status.

**Image dogfood must-haves** (from the spec + shipped UI):

| Requirement | Wire shape on our side |
|---|---|
| `t2i` | prompt, seed, size/profile, fan-out = N jobs |
| `inpaint` (canvas gen-fill) | `conditioning.source_image` + `conditioning.mask` — mask is an opaque B/W PNG, **white = generate**, plus prompt (`features.video_inpaint`-style modes: mask / mask+prompt) |
| Image references | `references.image` with roles (character consistency — cast refs flow from shots) |
| `features.seed` | reproducibility for takes/regeneration recipes |

**Nice-to-haves:** `i2i`/instruction edit, `outpaint`, `negative_prompt`, `native_batch`.

**Before-real-keys subset that image-first actually needs** (the full ticket was scoped for video-scale spend):
1. params_schema server-side validation (small, do it).
2. Idempotency on retries — for ~$0.04 images at dogfood scale, a rare double-spend is acceptable; MUST land before video keys ($0.35–1.90/clip).
3. Prompt `--`-flag stripping — only applies to text-command channels (Ark); pure-JSON APIs are unaffected. Verify per adapter.
4. Per-workspace vault rows — BYO-keys concern, NOT internal dogfood (env refs suffice).
5. Recorded-shape verification — applies to the mock-built seedance adapter; NEW adapters are built against live docs + a real key from day one.

## 2. Image APIs — integration readiness

*Verified set only (deep-research run 2026-07-10, adversarial 3-vote verification; primary sources = live OpenAPI specs + vendor docs). Providers where **no claim survived verification** are listed in §7 — notably Seedream/SeedEdit and OpenAI Images, both worth a follow-up before final commitment.*

| | **BFL FLUX** (FLUX.2 + FLUX.1 Fill) | **Gemini image** (Nano Banana 2 / Pro) |
|---|---|---|
| Async model | Async: `id` + `polling_url` (must poll the returned URL, not a constructed one) + first-class webhooks (`webhook_url`/`webhook_secret`, `X-Webhook-Secret`) | Sync-ish: inline base64 via the Interactions API (`interaction.output_image`) |
| **Mask inpaint (gen-fill)** | ✅ **FLUX.1 Fill: base64 source + B/W mask, white = inpaint, dimension-validated** — byte-for-byte our gen-fill semantics. Plus prompt-free **Erase** and **Outpaint/Expand** endpoints | ❌ Semantic/conversational masking only; **no explicit mask parameter exists**. Google's only explicit-mask path (Vertex Imagen) **shut down 2026-06-24** (refuted 0-3 as viable) |
| Multi-reference | FLUX.2: up to **10 reference images** in one request | Role-typed limits: 10 object / **4 character-consistency** / 3 style (Nano Banana 2) |
| Seeds | ✅ documented integer seed "for reproducibility" | ❌ no seed parameter documented |
| Output delivery | Region-specific signed URLs, **10-minute expiry, CORS disabled** — hard re-hosting requirement (our custody bridge already does this) | Inline base64, up to 4K |
| Moderation shape | Distinct statuses in the enum: `Request Moderated` / `Content Moderated` → map to a typed error | Unverified |
| Adapter lift | **Low-medium**: async pattern ≈ our Ark adapter with `polling_url` instead of constructed GET; webhooks optional (we poll today) | **Low**: sync pattern ≈ our elevenlabs adapter |
| Fit | **Gen-fill + t2i + refs + seeds: the only verified API meeting all four image requirements** | Reference-heavy instruction editing + cheap 1K–4K iteration; cannot serve gen-fill |

## 3. Video APIs — integration readiness

| | **Seedance (BytePlus ModelArk)** | **Veo 3.1 (Gemini API)** | **Wan 2.6/2.7 (Alibaba)** | **Runway** |
|---|---|---|---|---|
| Async model | create / retrieve(poll) / list / cancel — **1:1 with our existing adapter** | Gemini long-running ops | Async-only, `X-DashScope-Async` required, **no webhooks** | Clean async task API (`GET /v1/tasks/{id}`) |
| Feature fit | t2v/i2v, our lip-sync path already wired (mock) | **Best feature match**: i2v first-frame, extension +7s→~141s (**Veo-generated clips only**), first+last interpolation (preview) | t2v/i2v/r2v/v2v-edit; kf2v only on old models | t2v/i2v |
| Hazards | Recorded shapes must be live-verified (existing ticket) | Google API churn (Veo 3 deprecated; pin IDs in config) | **24h URL AND task_id expiry — stuck jobs unrecoverable after 24h** | — |
| Adapter lift | **Zero new code** — verification + key only | Medium (new adapter) | Medium | Medium |

## 4. Aggregator vs first-party

- **fal.ai**: uniform durable queue (`request_id` + response/status/cancel URLs, "requests are never lost"), webhooks with 15s timeout + 10 retries over 2h, **configurable media expiry (default ≥7 days)**. A credible single integration — but our adapter seam already exists and BFL's first-party mechanics are near-identical to it, so fal adds breadth later rather than being the first wire.
- **Replicate**: **deletes inputs/outputs/logs after ONE HOUR** by default; webhook retries only on terminal events with a ~1-minute ceiling → polling fallback is mandatory. Materially worse fit for our custody model. Prefer fal.ai if an aggregator is added.

## 5. Recommendation

1. **Wire BFL FLUX first** (one adapter kind, two endpoint families): FLUX.1 Fill for canvas gen-fill, FLUX.2 for t2i + multi-ref character/style consistency. Only verified API meeting all four image requirements; async shape maps directly onto the seam; ~a day of adapter work reusing the Ark custody/error patterns.
2. **Gemini Nano Banana 2 second** for role-typed reference editing and cheap iteration — sync adapter (elevenlabs pattern), accepting the no-mask/no-seed gaps (its manifest simply won't declare `inpaint` or `seed`; the UI already routes by manifest).
3. **Video next: Seedance** — the adapter exists; the work is the before-real-keys ticket (live-shape verification, remote-id persistence) plus a key. **Then Veo 3.1** for extension/frame-control.
4. **fal.ai later** for long-tail breadth (and as the Seedream/Kling access path if their first-party friction is high); not the first integration.
5. **Follow-ups before final commitment** (open questions the research could not verify): does Seedream/SeedEdit expose mask inpaint through ModelArk (near-zero lift if yes)? Does gpt-image-2 still take alpha masks like gpt-image-1 did? Both are cheap to answer with a console account + one curl each.

## 6. Integration hazards (engineer these)

1. **URL-expiry spectrum**: BFL 10 min < Replicate 1h < Wan 24h < fal configurable. Download must happen inside the job-completion handler with retry — our orchestrator's custody bridge already does this; the BFL window just forbids ever deferring it.
2. **Webhooks are at-least-once and unordered everywhere** — if we adopt them, key state transitions on the provider job id with idempotent monotonic updates, and keep polling as the fallback (we poll today; webhooks are an optimization).
3. **Wan task_ids purge at 24h** — jobs stuck longer are unrecoverable and must fail terminally.
4. **Treat provider status enums as open sets** — BFL's own example code checks a `Failed` status absent from its enum; map moderation statuses (`Request Moderated`/`Content Moderated`) to our `safety_blocked` taxonomy.
5. **Google churns API surfaces** (Imagen shut down, Veo 3 deprecated, image gen moved to the Interactions API) — pin model/endpoint IDs in config, not code.

## 7. Unverified / to re-check at implementation time

No claims survived verification for: **OpenAI Images (gpt-image-2/1.5/1-mini), BytePlus Seedream 5.0/SeedEdit, Alibaba Qwen-Image APIs, Ideogram, Recraft, Kling, Luma**. Also unverified across the board: entry-tier rate limits/concurrency, commercial terms (training-on-inputs defaults, output ownership, retention), BytePlus/Alibaba account-entity friction, aggregator pricing overhead. **ToS/console review is required before dogfooding with internal content.** Verification snapshots are from 2026-07-10 in a fast-deprecating space — re-verify endpoints when building.
