import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { StockPhoto } from "@iris/api-client";
import { assetClient } from "../api";
import { useEscape } from "./AssetThumb";

// Stock-photo import (Pexels): search externally, import into the project
// library as normal content-addressed assets with attribution metadata.
// Real imagery for the view/reference/canvas dogfood until generative
// models produce comparable plates.
export function StockPicker(props: { projectId: string; onClose: () => void }) {
  useEscape(props.onClose);
  const qc = useQueryClient();
  const [input, setInput] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const [importedIds, setImportedIds] = useState<Set<string>>(new Set());

  const search = useQuery({
    queryKey: ["stock", query, page],
    enabled: query !== "",
    staleTime: 5 * 60 * 1000,
    queryFn: () => assetClient.searchStock({ query, page }),
  });

  const importPhoto = useMutation({
    mutationFn: (p: StockPhoto) =>
      assetClient.importStock({ projectId: props.projectId, source: p.source, id: p.id }),
    onSuccess: (_res, p) => {
      setImportedIds((s) => new Set(s).add(p.id));
      void qc.invalidateQueries({ queryKey: ["assets"] });
    },
  });

  const submit = () => {
    const q = input.trim();
    if (q && q !== query) {
      setQuery(q);
      setPage(1);
    }
  };

  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal modal-wide" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Stock photos</h3>
          <span className="meta">Photos provided by Pexels</span>
          <button className="btn secondary" onClick={props.onClose}>
            Close
          </button>
        </div>
        <div className="toolbar">
          <input
            type="text"
            placeholder="Search Pexels… (e.g. diner interior night)"
            value={input}
            autoFocus
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && submit()}
            style={{ flex: 1 }}
          />
          <button className="btn" disabled={!input.trim()} onClick={submit}>
            Search
          </button>
        </div>

        {search.isLoading && <div className="empty">Searching…</div>}
        {search.isError && <div className="status error">{String(search.error)}</div>}
        {importPhoto.isError && <div className="status error">Import failed: {String(importPhoto.error)}</div>}
        {query === "" && <div className="empty">Search for reference plates, textures, or anything visual.</div>}
        {search.data?.photos.length === 0 && <div className="empty">No results for "{query}".</div>}

        <div className="picker-grid stock-grid">
          {search.data?.photos.map((p) => {
            const imported = importedIds.has(p.id);
            return (
              <div key={p.id} className="picker-cell">
                {/* Thumbnails load straight from the Pexels CDN. */}
                <img className="picker-thumb" src={p.thumbUrl} alt={p.alt} loading="lazy" />
                <span className="picker-name" title={p.alt}>
                  {p.alt || "untitled"}
                </span>
                <span className="stock-credit">
                  <a href={p.photographerUrl} target="_blank" rel="noreferrer">
                    {p.photographer}
                  </a>
                </span>
                <button
                  className="btn secondary chip-add"
                  disabled={imported || importPhoto.isPending}
                  onClick={() => importPhoto.mutate(p)}
                >
                  {imported ? "✓ Imported" : "Import"}
                </button>
              </div>
            );
          })}
        </div>

        {search.data && (
          <div className="toolbar" style={{ justifyContent: "center" }}>
            <button className="btn secondary" disabled={page === 1} onClick={() => setPage((p) => p - 1)}>
              ← Prev
            </button>
            <span className="meta">page {page}</span>
            <button className="btn secondary" disabled={!search.data.hasMore} onClick={() => setPage((p) => p + 1)}>
              Next →
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
