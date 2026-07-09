import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { Character } from "@iris/api-client";
import { storyClient } from "../api";
import { AssetThumb } from "../components/AssetThumb";
import { ImagePicker } from "../components/ImagePicker";

export function CharactersPage(props: { projectId?: string }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const chars = useQuery({
    queryKey: ["characters", props.projectId ?? ""],
    queryFn: () => storyClient.listCharacters({ projectId: props.projectId ?? "" }),
  });
  const create = useMutation({
    mutationFn: (n: string) => storyClient.createCharacter({ name: n, projectId: props.projectId ?? "" }),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["characters"] });
    },
  });

  const submit = () => {
    if (name.trim() && !create.isPending) create.mutate(name.trim());
  };

  return (
    <div>
      <h2>Characters</h2>
      <div className="toolbar">
        <input
          type="text"
          placeholder="New character name… (e.g. Mara)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submit()}
        />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={submit}>
          Create character
        </button>
      </div>
      {create.isError && <div className="status error">{String(create.error)}</div>}
      {chars.isLoading && <div className="empty">Loading…</div>}
      {chars.isError && <div className="status error">Failed to load characters: {String(chars.error)}</div>}
      {chars.data?.characters.length === 0 && (
        <div className="empty">
          No characters yet. A character carries its reference images everywhere — cast it in shots and cite it in
          generations for a consistent identity.
        </div>
      )}
      <div className="char-list">
        {chars.data?.characters.map((c) => <CharacterCard key={c.id} character={c} projectId={props.projectId} />)}
      </div>
    </div>
  );
}

const refRoles = ["turnaround", "expression", "other"] as const;

function CharacterCard({ character, projectId }: { character: Character; projectId?: string }) {
  const qc = useQueryClient();
  const [picking, setPicking] = useState(false);
  const [role, setRole] = useState<string>("turnaround");

  const addRef = useMutation({
    mutationFn: (assetId: string) =>
      storyClient.addCharacterRef({ characterId: character.id, role, asset: { assetId, versionId: "" } }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["characters"] }),
  });
  const removeRef = useMutation({
    mutationFn: (p: { role: string; assetId: string }) =>
      storyClient.removeCharacterRef({ characterId: character.id, role: p.role, assetId: p.assetId }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["characters"] }),
  });

  return (
    <div className="char-card">
      <div className="char-head">
        <span className="name">{character.name}</span>
        <span className="meta">{character.projectId ? "project" : "workspace"}</span>
      </div>
      <div className="ref-strip">
        {character.refs.map((r) => (
          <div key={`${r.role}:${r.asset?.assetId}`} className="ref-item" title={r.role}>
            <AssetThumb assetId={r.asset?.assetId ?? ""} className="ref-thumb" />
            <span className="ref-role">
              {r.role}
              <button
                className="chip-x"
                title="Remove reference"
                onClick={() => r.asset && removeRef.mutate({ role: r.role, assetId: r.asset.assetId })}
              >
                ×
              </button>
            </span>
          </div>
        ))}
        <div className="ref-add">
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            {refRoles.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
          <button className="btn secondary" onClick={() => setPicking(true)}>
            + Add ref
          </button>
        </div>
      </div>
      {(addRef.isError || removeRef.isError) && (
        <div className="status error">{String(addRef.error ?? removeRef.error)}</div>
      )}
      {picking && (
        <ImagePicker
          projectId={projectId}
          title={`Add ${role} reference for ${character.name}`}
          onPick={(assetId) => {
            addRef.mutate(assetId);
            setPicking(false);
          }}
          onClose={() => setPicking(false)}
        />
      )}
    </div>
  );
}
