// App shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Live: Projects, Story board (project landing), Scenes (+ scene detail),
// Characters, Canvases (+ canvas editor), Timelines (+ editor), Library,
// Jobs, and the Generate panel with shot targeting.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { JobState } from "@iris/api-client";
import { generationClient } from "./api";
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

export function App() {
  useEvents();
  const qc = useQueryClient();
  const [view, setView] = useState<View>("projects");
  const [project, setProject] = useState<{ id: string; name: string }>();
  const [sceneId, setSceneId] = useState<string>();
  const [canvasId, setCanvasId] = useState<string>();
  const [timelineId, setTimelineId] = useState<string>();
  const [canvasError, setCanvasError] = useState<string>();
  const [generating, setGenerating] = useState<
    { shotId: string; label: string; prefill?: GeneratePrefill; nonce: number } | true | false
  >(false);
  const generateNonce = useRef(0);
  const [railCollapsed, setRailCollapsed] = useState(() => localStorage.getItem("iris.rail") === "min");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);

  // Global affordances: ⌘K opens the palette anywhere; ? opens the
  // shortcut reference. Same input guards as every other hotkey surface.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
        return;
      }
      if (t.isContentEditable || t.closest?.("input,textarea,select,[contenteditable]")) return;
      if (e.key === "?" && !e.metaKey && !e.ctrlKey && !e.altKey) setHelpOpen(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const toggleRail = () => {
    setRailCollapsed((v) => {
      localStorage.setItem("iris.rail", v ? "" : "min");
      return !v;
    });
  };

  const jobs = useQuery({
    queryKey: ["jobs", project?.id ?? ""],
    enabled: !!project,
    queryFn: () => generationClient.listJobs({ projectId: project!.id }),
    // Slow-poll backstop while jobs are in flight: SSE is best-effort (a
    // dropped terminal event or a bridge outage must not strand the UI).
    refetchInterval: (query) =>
      query.state.data?.jobs.some(
        (j) =>
          j.state === JobState.QUEUED || j.state === JobState.DISPATCHED || j.state === JobState.RUNNING,
      )
        ? 15_000
        : false,
  });
  const activeJobs =
    jobs.data?.jobs.filter(
      (j) =>
        j.state === JobState.QUEUED || j.state === JobState.DISPATCHED || j.state === JobState.RUNNING,
    ).length ?? 0;

  const glyphs: Record<string, string> = {
    story: "▦", scenes: "🎬", characters: "👤", canvases: "🖼", timelines: "🎞", library: "📁", jobs: "⟳",
  };
  const navButton = (v: View, label: string) => (
    <button
      className={view === v ? "active" : ""}
      disabled={!project}
      title={project ? label : "Open a project first"}
      onClick={() => {
        setView(v);
        // Nav always returns Scenes/Canvases to their lists, and closes the
        // generate panel: a shot-targeted panel surviving navigation would
        // submit into a shot the user is no longer looking at.
        setSceneId(undefined);
        setCanvasId(undefined);
        setTimelineId(undefined);
        setGenerating(false);
      }}
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
        <button className={view === "projects" ? "active" : ""} onClick={() => setView("projects")}>
          Projects
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
                  { label: "Go to Story board", hint: "nav", run: () => { setView("story"); setSceneId(undefined); setCanvasId(undefined); setTimelineId(undefined); } },
                  { label: "Go to Scenes", hint: "nav", run: () => { setView("scenes"); setSceneId(undefined); } },
                  { label: "Go to Characters", hint: "nav", run: () => setView("characters") },
                  { label: "Go to Canvases", hint: "nav", run: () => { setView("canvases"); setCanvasId(undefined); } },
                  { label: "Go to Timelines", hint: "nav", run: () => { setView("timelines"); setTimelineId(undefined); } },
                  { label: "Go to Library", hint: "nav", run: () => setView("library") },
                  { label: "Go to Jobs", hint: "nav", run: () => setView("jobs") },
                  { label: "Generate (library)", hint: "action", run: () => setGenerating(true) },
                ] satisfies PaletteCommand[])
              : []),
            { label: "Go to Projects", hint: "nav", run: () => setView("projects") },
            { label: railCollapsed ? "Expand rail" : "Collapse rail", hint: "action", run: toggleRail },
            { label: "Keyboard shortcuts", hint: "help", run: () => setHelpOpen(true) },
          ] as PaletteCommand[])}
          onOpenScene={(id) => { setView("scenes"); setSceneId(id); }}
          onOpenCanvas={(id) => { setView("canvases"); setCanvasId(id); }}
          onOpenTimeline={(id) => { setView("timelines"); setTimelineId(id); }}
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
