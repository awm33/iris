import { JobState, type GenerationJob } from "@iris/api-client";

// A job the user is still waiting on. UPLOADING counts: artifacts are still
// landing, and every surface that dropped it (rail count, shot badges, the
// poll backstop) read as "stalled" during upload.
export function isActiveJob(j: GenerationJob): boolean {
  return (
    j.state === JobState.QUEUED ||
    j.state === JobState.DISPATCHED ||
    j.state === JobState.RUNNING ||
    j.state === JobState.UPLOADING
  );
}

// Error codes where retrying the identical request fails identically — the
// prompt (or params) are the problem, not the weather.
export function isPromptFault(errorCode: string): boolean {
  return errorCode === "safety_blocked" || errorCode === "invalid_input";
}

// Retry recreates the request verbatim, so it is pointless for prompt
// faults AND for dependency failures (the retried fanout gates on the same
// terminally-failed dependency and the reaper insta-fails it again).
export function isRetryFutile(errorCode: string): boolean {
  return isPromptFault(errorCode) || errorCode === "dependency_failed";
}

export function jobFailureText(j: GenerationJob): string {
  const reason = j.errorMessage || j.errorCode || "generation failed";
  return isPromptFault(j.errorCode) ? `${reason} — edit the prompt and regenerate` : reason;
}

/**
 * Per-shot badge inputs derived from the project jobs list (newest-first,
 * as ListJobs returns it): any active job targeting a shot spins it; a shot
 * reads "failed" only when its MOST RECENT targeting job failed — an old
 * failure under a newer success or run stays quiet.
 */
export function shotJobBadges(jobs: GenerationJob[]): {
  generating: Set<string>;
  failed: Map<string, string>;
} {
  const generating = new Set<string>();
  const failed = new Map<string, string>();
  const newestSeen = new Set<string>();
  for (const j of jobs) {
    if (!j.targetEntityId) continue;
    if (isActiveJob(j)) generating.add(j.targetEntityId);
    if (newestSeen.has(j.targetEntityId)) continue;
    newestSeen.add(j.targetEntityId);
    if (j.state === JobState.FAILED) failed.set(j.targetEntityId, jobFailureText(j));
  }
  // A shot that is actively generating again never shows the stale failure.
  for (const id of generating) failed.delete(id);
  return { generating, failed };
}
