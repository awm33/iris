// Golden-frame parity check (M7, PR 39): the engine preview and the server
// export must paint the same pixels. Hermetic against the LOCAL dev stack:
// builds its own fixture project/timeline (ffmpeg test sources uploaded
// through the normal asset path; cuts + a grade + a dissolve + a gap, no
// captions — those are DOM in the preview), exports it, then drives
// headless Chrome through the dev-only window.__irisGolden hooks and diffs
// preview captures against export frames per channel.
//
// Budget (recorded envelopes from PRs 36-38): mean abs error ≤ 2.5/255 per
// channel; 99th percentile ≤ 8/255; the dissolve sample gets 2× (two codec
// paths + ±half-frame sampling on both layers).
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const API = process.env.IRIS_API ?? "http://localhost:8280";
const WEB = process.env.IRIS_WEB ?? "http://localhost:5173";
const HERE = dirname(fileURLToPath(import.meta.url));
const OUT = join(HERE, "out");
// Fixture dims = the draft preset's exactly: the export then never scales
// (decrease → identity) and both sides compare at native resolution — a
// resample on one side only shows up as edge noise at hard boundaries.
const W = 1280, H = 720;

// mean catches pipeline errors (a color-matrix mistake measured 28, grade
// bugs shift it wholesale); p99 catches spatial bugs (frame misalignment
// measured 196-247). The p99 floor is encode-generation noise: the export
// IS a re-encode, so its frames are gen-2 vs the preview's gen-1 — on
// hostile content (testsrc2's noise block) that is ~20-30 at master CRF.
const MEAN_BUDGET = 2.5;
const P99_BUDGET = 32;

// Samples at FRAME CENTERS ((n+0.5)/fps): both sides then resolve the same
// frame — an on-boundary time lets the preview paint frame n while ffmpeg
// extracts n+1, and one frame of motion on test content is a huge p99.
const FPS = 24;
const center = (t) => (Math.round(t * FPS) + 0.5) / FPS;
// [time, label, budgetScale]
const SAMPLES = [
  [center(1.0), "clip-a-plain", 1],
  [center(3.25), "dissolve-mid", 1.5],
  [center(4.5), "clip-b-graded", 1],
  [center(6.5), "gap-black", 1],
  [center(7.75), "clip-a-again", 1],
];

const rpc = async (service, method, body) => {
  const res = await fetch(`${API}/iris.v1.${service}/${method}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${service}/${method}: ${res.status} ${await res.text()}`);
  return res.json();
};

const sh = (cmd, args) => execFileSync(cmd, args, { stdio: ["ignore", "pipe", "pipe"], maxBuffer: 64 * 1024 * 1024 });

async function upload(projectId, path, name) {
  const bytes = readFileSync(path);
  const start = await rpc("AssetService", "StartUpload", {
    projectId, filename: name, contentType: "video/mp4", sizeBytes: String(bytes.length),
  });
  const put = await fetch(start.partPutUrls[0], { method: "PUT", body: bytes });
  if (!put.ok) throw new Error(`put: ${put.status}`);
  const etag = (put.headers.get("etag") ?? "").replaceAll('"', "");
  const done = await rpc("AssetService", "CompleteUpload", { uploadId: start.uploadId, etags: [etag] });
  return done.version.id;
}

const oid = (() => { let n = 0; return () => `op_golden_${Date.now()}_${n++}`; })();

async function main() {
  rmSync(OUT, { recursive: true, force: true });
  mkdirSync(OUT, { recursive: true });

  // 1. Fixture media: deterministic test sources (seeded noise-free).
  console.log("· generating fixture media");
  const clipA = join(OUT, "a.mp4"), clipB = join(OUT, "b.mp4");
  // Explicit BT.709 tags: untagged HD is interpreted with DIFFERENT YUV
  // matrices by Chrome and ffmpeg (601 vs 709 assumptions) — a hue shift
  // on saturated content that has nothing to do with our pipeline.
  const tags = ["-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
    "-vf", "scale=out_color_matrix=bt709"];
  sh("ffmpeg", ["-v", "error", "-f", "lavfi", "-i", `testsrc2=duration=4:size=${W}x${H}:rate=24`,
    ...tags, "-c:v", "libx264", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", "-an", "-y", clipA]);
  sh("ffmpeg", ["-v", "error", "-f", "lavfi", "-i", `smptehdbars=duration=4:size=${W}x${H}:rate=24`,
    ...tags, "-c:v", "libx264", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", "-an", "-y", clipB]);

  // 2. Fixture project + timeline via the API.
  console.log("· building fixture timeline");
  const proj = await rpc("WorkspaceService", "CreateProject", { name: `golden-${Date.now()}` });
  const projectId = proj.project.id;
  const vA = await upload(projectId, clipA, "golden-a.mp4");
  const vB = await upload(projectId, clipB, "golden-b.mp4");
  const tl = await rpc("TimelineService", "CreateTimeline", { projectId, name: "golden", fps: 24 });
  const timelineId = tl.timeline.id;
  const ops = [
    { op_id: oid(), type: "add_track", track: { id: "v1", kind: "video" } },
    // A [0,3) with a 0.5s dissolve out; B [3,6) graded; gap [6,7); A [7,8.5)
    { op_id: oid(), type: "add_clip", track_id: "v1", clip: { id: "ga", name: "a", version_id: vA, start: 0, duration: 3, transition: { kind: "dissolve", duration: 0.5 } } },
    { op_id: oid(), type: "add_clip", track_id: "v1", clip: { id: "gb", name: "b", version_id: vB, start: 3, duration: 3, color: { exposure: 0.4, contrast: 1.1, temp: -0.5 } } },
    { op_id: oid(), type: "add_clip", track_id: "v1", clip: { id: "gc", name: "a2", version_id: vA, start: 7, duration: 1.5, in_point: 1 } },
  ];
  await rpc("TimelineService", "AppendTimelineOps", {
    timelineId, baseSeq: "0", payloads: ops.map((o) => JSON.stringify(o)),
  });

  // 3. Export + wait + download + extract frames (scaled back to fixture
  // dims — same aspect, so no letterbox on either side).
  console.log("· exporting");
  const exp = await rpc("TimelineService", "StartExport", { timelineId, preset: "master" });
  let version = "";
  for (let i = 0; i < 120; i++) {
    await new Promise((r) => setTimeout(r, 2000));
    const list = await rpc("TimelineService", "ListExports", { timelineId });
    const e = list.exports.find((x) => x.id === exp.export.id);
    if (e.state === "complete") { version = e.versionId; break; }
    if (e.state === "failed") throw new Error(`export failed: ${e.error}`);
  }
  if (!version) throw new Error("export timed out");
  const dl = await rpc("AssetService", "SignDownload", { versionId: version });
  const mp4 = join(OUT, "export.mp4");
  const buf = Buffer.from(await (await fetch(dl.url)).arrayBuffer());
  writeFileSync(mp4, buf);
  for (const [t, label] of SAMPLES) {
    // Extract the COVERING frame (the one the preview paints at t):
    // -ss alone grabs the first frame with pts ≥ t, i.e. the NEXT one.
    const idx = Math.round(t * FPS - 0.5);
    sh("ffmpeg", ["-v", "error", "-i", mp4,
      "-vf", `select=eq(n\\,${idx})`, "-vsync", "0", "-frames:v", "1", "-y", join(OUT, `export-${label}.png`)]);
  }

  // 4. Headless preview captures.
  console.log("· capturing preview frames (headless chrome)");
  // Branded Chrome, not the bundled Chromium: WebCodecs H.264 decode needs
  // the proprietary codecs only the real build ships.
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  // Before ANY app code runs: the preview must decode ORIGINALS (see
  // srcFor) so codec-generation noise from proxies stays out of the diff.
  await page.addInitScript(() => { window.__irisGoldenOriginals = true; });
  await page.goto(WEB);
  await page.getByText(proj.project.name).click();
  await page.getByRole("button", { name: "Timelines" }).click();
  await page.getByText("golden", { exact: true }).click();
  await page.waitForFunction(() => !!window.__irisGolden, null, { timeout: 15000 });

  const results = [];
  for (const [t, label, scale] of SAMPLES) {
    await page.evaluate((tt) => window.__irisGolden.seek(tt), t);
    // Paint settles when two captures 250ms apart agree (or 6s worst case).
    let data = null, prev = null;
    for (let i = 0; i < 24; i++) {
      await page.waitForTimeout(250);
      data = await page.evaluate(() => window.__irisGolden.capture());
      if (data && data === prev) break;
      prev = data;
    }
    // A cleared canvas (gap) captures as transparent — composite onto
    // black in-page during the diff instead of special-casing here.
    const stats = await page.evaluate(async ({ previewURL, exportPNG, w, h }) => {
      const load = (src) => new Promise((res, rej) => {
        const img = new Image();
        img.onload = () => res(img);
        img.onerror = rej;
        img.src = src;
      });
      const draw = (img) => {
        const c = document.createElement("canvas");
        c.width = w; c.height = h;
        const g = c.getContext("2d");
        g.fillStyle = "#000";
        g.fillRect(0, 0, w, h); // transparent (gap) composites to black
        if (img) {
          const s = Math.min(w / img.width, h / img.height);
          const dw = img.width * s, dh = img.height * s;
          g.drawImage(img, (w - dw) / 2, (h - dh) / 2, dw, dh);
        }
        return g.getImageData(0, 0, w, h).data;
      };
      const a = draw(previewURL ? await load(previewURL) : null);
      const b = draw(await load(exportPNG));
      const diffs = [[], [], []];
      for (let i = 0; i < a.length; i += 4) {
        for (let c = 0; c < 3; c++) diffs[c].push(Math.abs(a[i + c] - b[i + c]));
      }
      return diffs.map((d) => {
        d.sort((x, y) => x - y);
        return {
          mean: d.reduce((s, v) => s + v, 0) / d.length,
          p99: d[Math.floor(d.length * 0.99)],
        };
      });
    }, {
      previewURL: data,
      exportPNG: "data:image/png;base64," + readFileSync(join(OUT, `export-${label}.png`)).toString("base64"),
      w: W, h: H,
    });

    const worstMean = Math.max(...stats.map((s) => s.mean));
    const worstP99 = Math.max(...stats.map((s) => s.p99));
    const pass = worstMean <= MEAN_BUDGET * scale && worstP99 <= P99_BUDGET * scale;
    results.push({ t, label, worstMean, worstP99, scale, pass });
    console.log(`  ${pass ? "✓" : "✗"} t=${t} ${label}: mean=${worstMean.toFixed(2)} p99=${worstP99} (budget ×${scale})`);
    if (!pass && data) {
      writeFileSync(join(OUT, `preview-${label}.png`), Buffer.from(data.split(",")[1], "base64"));
    }
  }
  await browser.close();

  const failed = results.filter((r) => !r.pass);
  if (failed.length) {
    console.error(`\nGOLDEN FAILED: ${failed.map((f) => f.label).join(", ")} — artifacts in tools/golden/out/`);
    process.exit(1);
  }
  console.log("\nGOLDEN PASSED — preview and export agree within budget.");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
