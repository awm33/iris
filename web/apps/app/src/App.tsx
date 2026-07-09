// App shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Live: Projects, Scenes (+ scene detail), Characters, Library, Jobs, and the
// Generate panel with shot targeting. Story board/Timelines/Canvases land
// with M4–M5.
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { JobState } from "@iris/api-client";
import { generationClient } from "./api";
import { GeneratePanel, prefillFromRecipe, type GeneratePrefill } from "./components/GeneratePanel";
import { CharactersPage } from "./pages/CharactersPage";
import { JobsPage } from "./pages/JobsPage";
import { LibraryPage } from "./pages/LibraryPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { ScenePage } from "./pages/ScenePage";
import { ScenesPage } from "./pages/ScenesPage";
import { useEvents } from "./useEvents";

type View = "projects" | "scenes" | "characters" | "library" | "jobs";
const comingSoon = ["Story", "Timelines", "Canvases"] as const;

export function App() {
  useEvents();
  const [view, setView] = useState<View>("projects");
  const [project, setProject] = useState<{ id: string; name: string }>();
  const [sceneId, setSceneId] = useState<string>();
  const [generating, setGenerating] = useState<
    { shotId: string; label: string; prefill?: GeneratePrefill } | true | false
  >(false);

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
        // Nav always returns Scenes to the list, and closes the generate
        // panel: a shot-targeted panel surviving navigation would submit
        // into a shot the user is no longer looking at.
        setSceneId(undefined);
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
              setGenerating({ shotId, label, prefill: recipeJson ? prefillFromRecipe(recipeJson) : undefined })
            }
          />
        )}
        {view === "characters" && <CharactersPage projectId={project?.id} />}
        {view === "library" && (
          <LibraryPage projectId={project?.id} onGenerate={project ? () => setGenerating(true) : undefined} />
        )}
        {view === "jobs" && <JobsPage projectId={project?.id} />}
      </main>
      {generating !== false && project && (
        <GeneratePanel
          // Remount per intent: a regenerate prefill must not leak state
          // into a subsequent plain open (and vice versa).
          key={typeof generating === "object" ? `${generating.shotId}:${generating.prefill ? "r" : "g"}` : "library"}
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
