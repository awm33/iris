// M1 app shell: left-rail IA per docs/design/02-ui-ux-design.md §2.
// Story/Scenes/Timelines/Canvases/Jobs are placeholders until M3/M5;
// Projects (home) and Library are live.
import { useState } from "react";
import { LibraryPage } from "./pages/LibraryPage";
import { ProjectsPage } from "./pages/ProjectsPage";

type View = "projects" | "library";
const comingSoon = ["Story", "Scenes", "Timelines", "Canvases", "Jobs"] as const;

export function App() {
  const [view, setView] = useState<View>("projects");
  const [project, setProject] = useState<{ id: string; name: string }>();

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
        <button className={view === "library" ? "active" : ""} onClick={() => setView("library")}>
          Library
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
        {view === "library" && <LibraryPage projectId={project?.id} />}
      </main>
    </>
  );
}
