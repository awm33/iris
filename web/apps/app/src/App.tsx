// App shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Live: Projects, Scenes (+ scene detail), Characters, Canvases (+ canvas
// editor), Library, Jobs, and the Generate panel with shot targeting.
// Story board lands later in M5.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { JobState } from "@iris/api-client";
import { generationClient } from "./api";
import { CanvasesPage } from "./canvas/CanvasesPage";
import { TimelinesPage } from "./timeline/TimelinesPage";
import { TimelinePage } from "./timeline/TimelinePage";
import { CanvasPage } from "./canvas/CanvasPage";
import { createCanvasFromAsset } from "./canvas/createFromAsset";
import { GeneratePanel, prefillFromRecipe, type GeneratePrefill } from "./components/GeneratePanel";
import { CharactersPage } from "./pages/CharactersPage";
import { JobsPage } from "./pages/JobsPage";
import { LibraryPage } from "./pages/LibraryPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ScenePage } from "./pages/ScenePage";
import { ScenesPage } from "./pages/ScenesPage";
import { useEvents } from "./useEvents";

type View = "projects" | "scenes" | "characters" | "canvases" | "timelines" | "library" | "jobs";
const comingSoon = ["Story"] as const;

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

  const navButton = (v: View, label: string) => (
    <button
      className={view === v ? "active" : ""}
      disabled={!project}
      title={project ? undefined : "Open a project first"}
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
      {label}
    </button>
  );

  return (
    <>
      <nav className="rail">
        <div className="logo">⬡ Iris{project ? ` · ${project.name}` : ""}</div>
        <button className={view === "projects" ? "active" : ""} onClick={() => setView("projects")}>
          Projects
        </button>
        {comingSoon.map((label) => (
          <button key={label} disabled title="Lands in a later milestone">
            {label}
          </button>
        ))}
        {navButton("scenes", "Scenes")}
        {navButton("characters", "Characters")}
        {navButton("canvases", "Canvases")}
        {navButton("timelines", "Timelines")}
        {navButton("library", "Library")}
        {navButton("jobs", activeJobs > 0 ? `Jobs ⟳${activeJobs}` : "Jobs")}
      </nav>
      <main className="main">
        {view === "projects" && (
          <ProjectsPage
            onOpen={(id, name) => {
              setProject({ id, name });
              setSceneId(undefined);
              setGenerating(false); // stale targets/refs must not cross projects
              setView("scenes");
            }}
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
