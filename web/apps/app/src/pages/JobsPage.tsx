import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { JobState, type GenerationJob } from "@iris/api-client";
import { assetClient, generationClient } from "../api";
import { isActiveJob, isPromptFault, isRetryFutile } from "../jobBadges";

export function JobsPage(props: {
  projectId?: string;
  /** Jump to the shot's scene or the canvas a job targeted. */
  onOpenTarget?: (targetEntityId: string, targetSceneId: string) => void;
}) {
  const jobs = useQuery({
    queryKey: ["jobs", props.projectId ?? ""],
    enabled: !!props.projectId,
    queryFn: () => generationClient.listJobs({ projectId: props.projectId! }),
  });

  if (!props.projectId) {
    return (
      <div>
        <h2>Jobs</h2>
        <div className="empty">Open a project to see its generation jobs.</div>
      </div>
    );
  }
  return (
    <div>
      <h2>Jobs</h2>
      {jobs.isLoading && <div className="empty">Loading…</div>}
      {jobs.data && jobs.data.jobs.length === 0 && (
        <div className="empty">No generation jobs yet — hit ⚡ Generate in the Library.</div>
      )}
      <div className="job-list">
        {jobs.data?.jobs.map((j) => <JobCard key={j.id} job={j} onOpenTarget={props.onOpenTarget} />)}
      </div>
    </div>
  );
}

const stateLabel: Partial<Record<JobState, string>> = {
  [JobState.DRAFT]: "draft",
  [JobState.QUEUED]: "queued",
  [JobState.DISPATCHED]: "dispatched",
  [JobState.RUNNING]: "running",
  [JobState.UPLOADING]: "uploading",
  [JobState.COMPLETE]: "complete",
  [JobState.FAILED]: "failed",
  [JobState.CANCELED]: "canceled",
};

function JobCard({
  job,
  onOpenTarget,
}: {
  job: GenerationJob;
  onOpenTarget?: (targetEntityId: string, targetSceneId: string) => void;
}) {
  const qc = useQueryClient();
  const active = isActiveJob(job);
  // A safety-blocked / invalid-input / dependency failure fails identically
  // on retry — offering Retry there is a trap.
  const retryable =
    job.state === JobState.CANCELED || (job.state === JobState.FAILED && !isRetryFutile(job.errorCode));

  const cancel = useMutation({
    mutationFn: () => generationClient.cancelJob({ id: job.id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
  const retry = useMutation({
    mutationFn: () => generationClient.retryJob({ id: job.id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs"] }),
  });

  return (
    <div className={`job-card state-${stateLabel[job.state] ?? "unknown"}`}>
      <div className="job-main">
        <div className="name">{job.prompt || (job.task === "inpaint" ? "removal" : job.id)}</div>
        <div className="meta">
          {stateLabel[job.state]} · {job.count > 1 ? `${job.count} takes · ` : ""}
          {job.task} · {job.profile}
          {job.costActual ? ` · ${job.costActual.toFixed(1)} gpu·s` : ""}
          {job.targetLabel &&
            (onOpenTarget ? (
              <>
                {" · "}
                <button
                  className="link-button"
                  title="Open the shot's scene / the canvas"
                  onClick={() => onOpenTarget(job.targetEntityId, job.targetSceneId)}
                >
                  → {job.targetLabel}
                </button>
              </>
            ) : (
              ` · → ${job.targetLabel}`
            ))}
        </div>
        {active && (
          <div className="progress">
            <div className="progress-bar" style={{ width: `${Math.round(job.progress * 100)}%` }} />
          </div>
        )}
        {job.state === JobState.FAILED && (
          <div className="status error">
            {job.errorCode ? `${job.errorCode}: ` : ""}
            {job.errorMessage || "generation failed (no error detail)"}
            {isPromptFault(job.errorCode) ? " — edit the prompt and regenerate; retrying won't help" : ""}
          </div>
        )}
        {job.artifactVersionIds.length > 0 && (
          <div className="artifact-strip">
            {job.artifactVersionIds.map((v) => (
              <ArtifactThumb key={v} versionId={v} />
            ))}
          </div>
        )}
      </div>
      <div className="job-actions">
        {active && (
          <button className="btn secondary" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
            Cancel
          </button>
        )}
        {retryable && (
          <button className="btn secondary" disabled={retry.isPending} onClick={() => retry.mutate()}>
            Retry
          </button>
        )}
      </div>
    </div>
  );
}

// Artifact thumbnails: try the poster variant first (videos have one after
// the probe; images 404) and fall back to the original object (correct for
// images; a pre-probe video's mp4 will fail to render as <img> and drops to
// the placeholder via onError until the media event brings the poster).
function ArtifactThumb({ versionId }: { versionId: string }) {
  const [imgFailed, setImgFailed] = useState(false);
  const poster = useQuery({
    queryKey: ["artifact-thumb", versionId, "poster"],
    retry: false,
    staleTime: 10 * 60 * 1000,
    queryFn: () => assetClient.signDownload({ versionId, variant: "poster" }),
  });
  const isNotFound = poster.error instanceof ConnectError && poster.error.code === Code.NotFound;
  const original = useQuery({
    queryKey: ["artifact-thumb", versionId, "original"],
    enabled: isNotFound,
    retry: 1,
    staleTime: 10 * 60 * 1000,
    queryFn: () => assetClient.signDownload({ versionId }),
  });
  const url = poster.data?.url ?? original.data?.url;
  return url && !imgFailed ? (
    <img
      className="artifact-img"
      src={url}
      alt="artifact"
      onError={() => setImgFailed(true)}
      onLoad={() => setImgFailed(false)}
    />
  ) : (
    <div className="artifact-img placeholder">⟳</div>
  );
}
