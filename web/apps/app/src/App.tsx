// App shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Live: Projects, Library (upload + thumbnails), Jobs (generation), and the
// Generate panel (Context-Dock v1). Story/Scenes/Timelines/Canvases land
// with M3–M5.
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { JobState } from "@iris/api-client";
import { generationClient } from "./api";
import { GeneratePanel } from "./components/GeneratePanel";
import { JobsPage } from "./pages/JobsPage";
import { LibraryPage } from "./pages/LibraryPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { useEvents } from "./useEvents";

type View = "projects" | "library" | "jobs";
const comingSoon = ["Story", "Scenes", "Timelines", "Canvases"] as const;

export function App() {
  useEvents();
  const [view, setView] = useState<View>("projects");
  const [project, setProject] = useState<{ id: string; name: string }>();
  const [generating, setGenerating] = useState(false);

  const jobs = useQuery({
    queryKey: ["jobs", project?.id ?? ""],
    enabled: !!project,
    queryFn: () => generationClient.listJobs({ projectId: project!.id }),
  });
  const activeJobs =
    jobs.data?.jobs.filter(
      (j) =>
        j.state === JobState.QUEUED || j.state === JobState.DISPATCHED || j.state === JobState.RUNNING,
    ).length ?? 0;

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
        <button
          className={view === "library" ? "active" : ""}
          disabled={!project}
          title={project ? undefined : "Open a project first"}
          onClick={() => setView("library")}
        >
          Library
        </button>
        <button
          className={view === "jobs" ? "active" : ""}
          disabled={!project}
          title={project ? undefined : "Open a project first"}
          onClick={() => setView("jobs")}
        >
          Jobs{activeJobs > 0 ? ` ⟳${activeJobs}` : ""}
        </button>
      </nav>
      <main className="main">
        {view === "projects" && (
          <ProjectsPage
            onOpen={(id, name) => {
              setProject({ id, name });
              setView("library");
            }}
          />
        )}
        {view === "library" && (
          <LibraryPage projectId={project?.id} onGenerate={project ? () => setGenerating(true) : undefined} />
        )}
        {view === "jobs" && <JobsPage projectId={project?.id} />}
      </main>
      {generating && project && (
        <GeneratePanel
          projectId={project.id}
          onClose={() => setGenerating(false)}
          onSubmitted={() => {
            setGenerating(false);
            setView("jobs");
          }}
        />
      )}
    </>
  );
}
