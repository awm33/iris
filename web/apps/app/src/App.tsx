// App shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Live: Projects, Story board (project landing), Scenes (+ scene detail),
// Characters, Canvases (+ canvas editor), Timelines (+ editor), Library,
// Jobs, and the Generate panel with shot targeting.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { generationClient, workspaceClient } from "./api";
import { isActiveJob } from "./jobBadges";
import { CanvasesPage } from "./canvas/CanvasesPage";
import { TimelinesPage } from "./timeline/TimelinesPage";
import { TimelinePage } from "./timeline/TimelinePage";
import { CanvasPage } from "./canvas/CanvasPage";
import { createCanvasFromAsset } from "./canvas/createFromAsset";
import { CommandPalette, type PaletteCommand } from "./components/CommandPalette";
import { GeneratePanel, prefillFromRecipe, type GeneratePrefill } from "./components/GeneratePanel";
import { ShortcutHelp } from "./components/ShortcutHelp";
import { CharactersPage } from "./pages/CharactersPage";
import { JobsPage } from "./pages/JobsPage";
import { LibraryPage } from "./pages/LibraryPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ScenePage } from "./pages/ScenePage";
import { ScenesPage } from "./pages/ScenesPage";
import { StoryBoardPage } from "./pages/StoryBoardPage";
import { useEvents } from "./useEvents";

type View = "projects" | "story" | "scenes" | "characters" | "canvases" | "timelines" | "library" | "jobs";

const VIEWS: View[] = ["projects", "story", "scenes", "characters", "canvases", "timelines", "library", "jobs"];

// URL scheme: #/projects, #/<view>/<projectId>, #/<view>/<projectId>/<entityId>
// (scenes/canvases/timelines detail views). The hash is the ONLY routing
// state carrier — refresh restores it, Back/Forward walk it, and every view
// is linkable. Unparseable hashes fall back to Projects rather than a 404:
// in a hash router a bad fragment is a typo, not a missing page.
type Route = { view: View; projectId?: string; entityId?: string };

function parseHash(hash: string): Route {
  const [view, projectId, entityId] = hash
    .replace(/^#\/?/, "")
    .split("/")
    .filter(Boolean)
    .map(decodeURIComponent);
  if (!view || !VIEWS.includes(view as View) || view === "projects" || !projectId) {
    return { view: "projects" };
  }
  return { view: view as View, projectId, entityId };
}

function buildHash(view: View, projectId?: string, entityId?: string): string {
  if (view === "projects" || !projectId) return "#/projects";
  const parts = [view, projectId, ...(entityId ? [entityId] : [])];
  return `#/${parts.map(encodeURIComponent).join("/")}`;
}

// localStorage can throw (Safari private mode, storage-disabled embeds) —
// a rail-width nicety must never crash the shell at boot.
const railPref = {
  get: () => { try { return localStorage.getItem("iris.rail"); } catch { return null; } },
  set: (v: string) => { try { localStorage.setItem("iris.rail", v); } catch { /* session-only */ } },
};

export function App() {
  useEvents();
  const qc = useQueryClient();
  // Boot state comes from the hash (not an effect): the state→hash sync
  // effect below runs on first render, and if state started at defaults it
  // would clobber a deep link before any restore effect could apply it.
  const bootRoute = useRef(parseHash(window.location.hash)).current;
  const [view, setView] = useState<View>(bootRoute.view);
  const [project, setProject] = useState<{ id: string; name: string } | undefined>(
    bootRoute.projectId ? { id: bootRoute.projectId, name: "…" } : undefined,
  );
  const [sceneId, setSceneId] = useState<string | undefined>(
    bootRoute.view === "scenes" ? bootRoute.entityId : undefined,
  );
  const [canvasId, setCanvasId] = useState<string | undefined>(
    bootRoute.view === "canvases" ? bootRoute.entityId : undefined,
  );
  const [timelineId, setTimelineId] = useState<string | undefined>(
    bootRoute.view === "timelines" ? bootRoute.entityId : undefined,
  );
  const [canvasError, setCanvasError] = useState<string>();
  const [generating, setGenerating] = useState<
    { shotId: string; label: string; prefill?: GeneratePrefill; nonce: number } | true | false
  >(false);
  const generateNonce = useRef(0);
  const [railCollapsed, setRailCollapsed] = useState(() => railPref.get() === "min");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);

  // Resolve a hash-restored project id to its real name (and confirm it
  // still exists — a stale link must land on Projects, not a ghost shell).
  const projectRef = useRef(project);
  projectRef.current = project;
  const resolveProject = (id: string) => {
    workspaceClient
      .getProject({ id })
      .then((res) => {
        setProject((p) =>
          p?.id === id ? { id, name: res.project?.name || id } : p,
        );
      })
      .catch(() => {
        if (projectRef.current?.id !== id) return; // user already moved on
        setProject(undefined);
        setView("projects");
      });
  };
  useEffect(() => {
    if (bootRoute.projectId) resolveProject(bootRoute.projectId);
    // Back/Forward (and hand-edited fragments): apply the route. Same-
    // document traversals that differ in fragment always fire hashchange.
    const onHashChange = () => {
      const r = parseHash(window.location.hash);
      setView(r.view);
      setSceneId(r.view === "scenes" ? r.entityId : undefined);
      setCanvasId(r.view === "canvases" ? r.entityId : undefined);
      setTimelineId(r.view === "timelines" ? r.entityId : undefined);
      setGenerating(false); // stale shot targets must not survive traversal
      if (!r.projectId) {
        setProject(undefined);
      } else if (projectRef.current?.id !== r.projectId) {
        setProject({ id: r.projectId, name: "…" });
        resolveProject(r.projectId);
      }
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // State → URL. Equality-guarded: applying a route FROM the hash re-derives
  // the same hash, so traversal doesn't push echo entries.
  useEffect(() => {
    const h = buildHash(view, project?.id, sceneId ?? canvasId ?? timelineId);
    if (window.location.hash !== h) window.history.pushState(null, "", h);
  }, [view, project?.id, sceneId, canvasId, timelineId]);

  // Global affordances: ⌘K opens the palette anywhere; ? opens the
  // shortcut reference. Same input guards as every other hotkey surface.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if ((e.metaKey || e.ctrlKey) && !e.repeat && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
        return;
      }
      if (t.isContentEditable || t.closest?.("input,textarea,select,[contenteditable]")) return;
      // No alt guard: on many layouts "?" itself needs a modifier, and
      // e.key already tells us the layout produced a question mark.
      if (e.key === "?" && !e.metaKey && !e.ctrlKey) setHelpOpen(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const toggleRail = () => {
    setRailCollapsed((v) => {
      railPref.set(v ? "" : "min");
      return !v;
    });
  };

  // EVERY navigation — rail, palette command, palette entity jump — goes
  // through here. Nav returns Scenes/Canvases/Timelines to their lists and
  // closes the generate panel: a shot-targeted panel surviving navigation
  // would submit into a shot the user is no longer looking at. Jumps that
  // want a detail view set the id AFTER goTo.
  const goTo = (v: View) => {
    setView(v);
    setSceneId(undefined);
    setCanvasId(undefined);
    setTimelineId(undefined);
    setGenerating(false);
  };

  const jobs = useQuery({
    queryKey: ["jobs", project?.id ?? ""],
    enabled: !!project,
    queryFn: () => generationClient.listJobs({ projectId: project!.id }),
    // Slow-poll backstop while jobs are in flight: SSE is best-effort (a
    // dropped terminal event or a bridge outage must not strand the UI).
    refetchInterval: (query) => (query.state.data?.jobs.some(isActiveJob) ? 15_000 : false),
  });
  const activeJobs = jobs.data?.jobs.filter(isActiveJob).length ?? 0;

  const glyphs: Record<string, string> = {
    story: "▦", scenes: "🎬", characters: "👤", canvases: "🖼", timelines: "🎞", library: "📁", jobs: "⟳",
  };
  const navButton = (v: View, label: string) => (
    <button
      className={view === v ? "active" : ""}
      disabled={!project}
      title={project ? label : "Open a project first"}
      onClick={() => goTo(v)}
    >
      {railCollapsed ? glyphs[v] ?? label[0] : label}
    </button>
  );

  return (
    <>
      <nav className={`rail${railCollapsed ? " rail-min" : ""}`}>
        <div className="logo" title={project ? `Iris · ${project.name}` : "Iris"}>
          ⬡{!railCollapsed && <> Iris{project ? ` · ${project.name}` : ""}</>}
        </div>
        <button
          className={view === "projects" ? "active" : ""}
          title="Projects"
          onClick={() => goTo("projects")}
        >
          {railCollapsed ? "⌂" : "Projects"}
        </button>
        {navButton("story", "Story")}
        {navButton("scenes", "Scenes")}
        {navButton("characters", "Characters")}
        {navButton("canvases", "Canvases")}
        {navButton("timelines", "Timelines")}
        {navButton("library", "Library")}
        {navButton("jobs", activeJobs > 0 ? `Jobs ⟳${activeJobs}` : "Jobs")}
        <div className="rail-foot">
          <button title="Keyboard shortcuts (?)" onClick={() => setHelpOpen(true)}>
            ⌨{!railCollapsed && " Shortcuts"}
          </button>
          <button title={railCollapsed ? "Expand" : "Collapse"} onClick={toggleRail}>
            {railCollapsed ? "⟩⟩" : "⟨⟨ Collapse"}
          </button>
        </div>
      </nav>
      <main className="main">
        {view === "projects" && (
          <ProjectsPage
            onOpen={(id, name) => {
              setProject({ id, name });
              setSceneId(undefined);
              setGenerating(false); // stale targets/refs must not cross projects
              setView("story"); // UX doc §3.1: the board is the project landing
            }}
          />
        )}
        {view === "story" && project && (
          <StoryBoardPage
            projectId={project.id}
            onOpenScene={(id) => {
              setSceneId(id);
              setView("scenes");
            }}
            onGenerateForShot={(shotId, label) =>
              setGenerating({ shotId, label, nonce: ++generateNonce.current })
            }
          />
        )}
        {view === "scenes" && project && !sceneId && (
          <ScenesPage projectId={project.id} onOpen={(id) => setSceneId(id)} />
        )}
        {view === "scenes" && project && sceneId && (
          <ScenePage
            projectId={project.id}
            sceneId={sceneId}
            onBack={() => {
              setSceneId(undefined);
              setGenerating(false);
            }}
            onGenerateForShot={(shotId, label, recipeJson) =>
              setGenerating({
                shotId,
                label,
                prefill: recipeJson ? prefillFromRecipe(recipeJson) : undefined,
                nonce: ++generateNonce.current,
              })
            }
          />
        )}
        {view === "characters" && <CharactersPage projectId={project?.id} />}
        {view === "canvases" && project && !canvasId && (
          <>
            {canvasError && <div className="status error">{canvasError}</div>}
            <CanvasesPage projectId={project.id} onOpen={(id) => setCanvasId(id)} />
          </>
        )}
        {view === "canvases" && project && canvasId && (
          <CanvasPage
            // Session identity is per-canvas: never reuse a mounted editor
            // across canvas ids.
            key={canvasId}
            canvasId={canvasId}
            projectId={project.id}
            onBack={() => {
              setCanvasId(undefined);
              void qc.invalidateQueries({ queryKey: ["canvases"] });
            }}
          />
        )}
        {view === "timelines" && project && !timelineId && (
          <TimelinesPage projectId={project.id} onOpen={(id) => setTimelineId(id)} />
        )}
        {view === "timelines" && project && timelineId && (
          <TimelinePage
            key={timelineId}
            timelineId={timelineId}
            projectId={project.id}
            onBack={() => setTimelineId(undefined)}
            onGenerateForShot={(shotId, label) =>
              setGenerating({ shotId, label, nonce: ++generateNonce.current })
            }
          />
        )}
        {view === "library" && (
          <LibraryPage
            projectId={project?.id}
            onGenerate={project ? () => setGenerating(true) : undefined}
            onEditInCanvas={
              project
                ? (assetId) => {
                    setCanvasError(undefined);
                    createCanvasFromAsset(project.id, assetId)
                      .then((id) => {
                        setCanvasId(id);
                        setView("canvases");
                        setGenerating(false);
                      })
                      .catch((e) => {
                        setCanvasError(`Couldn’t open in canvas: ${String(e)}`);
                        setView("canvases");
                      });
                  }
                : undefined
            }
          />
        )}
        {view === "jobs" && <JobsPage projectId={project?.id} />}
      </main>
      {paletteOpen && (
        <CommandPalette
          projectId={project?.id}
          commands={([
            ...(project
              ? ([
                  { label: "Go to Story board", hint: "nav", run: () => goTo("story") },
                  { label: "Go to Scenes", hint: "nav", run: () => goTo("scenes") },
                  { label: "Go to Characters", hint: "nav", run: () => goTo("characters") },
                  { label: "Go to Canvases", hint: "nav", run: () => goTo("canvases") },
                  { label: "Go to Timelines", hint: "nav", run: () => goTo("timelines") },
                  { label: "Go to Library", hint: "nav", run: () => goTo("library") },
                  { label: "Go to Jobs", hint: "nav", run: () => goTo("jobs") },
                  { label: "Generate (library)", hint: "action", run: () => { goTo("library"); setGenerating(true); } },
                ] satisfies PaletteCommand[])
              : []),
            { label: "Go to Projects", hint: "nav", run: () => goTo("projects") },
            { label: railCollapsed ? "Expand rail" : "Collapse rail", hint: "action", run: toggleRail },
            { label: "Keyboard shortcuts", hint: "help", run: () => setHelpOpen(true) },
          ] as PaletteCommand[])}
          onOpenScene={(id) => { goTo("scenes"); setSceneId(id); }}
          onOpenCanvas={(id) => { goTo("canvases"); setCanvasId(id); }}
          onOpenTimeline={(id) => { goTo("timelines"); setTimelineId(id); }}
          onClose={() => setPaletteOpen(false)}
        />
      )}
      {helpOpen && <ShortcutHelp onClose={() => setHelpOpen(false)} />}
      {generating !== false && project && (
        <GeneratePanel
          // Remount per intent (nonce): two successive regenerates from
          // DIFFERENT takes of the same shot must not share panel state —
          // useState initializers only run on mount.
          key={typeof generating === "object" ? `${generating.shotId}:${generating.nonce}` : "library"}
          projectId={project.id}
          target={typeof generating === "object" ? generating : undefined}
          prefill={typeof generating === "object" ? generating.prefill : undefined}
          onClose={() => setGenerating(false)}
          onSubmitted={() => {
            const wasShot = typeof generating === "object";
            setGenerating(false);
            if (!wasShot) setView("jobs");
            // Shot-targeted generations: stay on the scene; takes arrive live.
          }}
        />
      )}
    </>
  );
}
