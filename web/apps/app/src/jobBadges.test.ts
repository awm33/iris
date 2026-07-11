import { describe, expect, it } from "vitest";
import { JobState, type GenerationJob } from "@iris/api-client";
import { isActiveJob, isPromptFault, jobFailureText, shotJobBadges } from "./jobBadges";

// Jobs arrive newest-first, as ListJobs returns them.
const job = (target: string, state: JobState, errorCode = "", errorMessage = ""): GenerationJob =>
  ({ id: `j_${Math.random()}`, targetEntityId: target, state, errorCode, errorMessage }) as GenerationJob;

describe("isActiveJob", () => {
  it("counts UPLOADING as active — artifacts are still landing", () => {
    expect(isActiveJob(job("s", JobState.UPLOADING))).toBe(true);
    expect(isActiveJob(job("s", JobState.RUNNING))).toBe(true);
    expect(isActiveJob(job("s", JobState.FAILED))).toBe(false);
    expect(isActiveJob(job("s", JobState.COMPLETE))).toBe(false);
  });
});

describe("shotJobBadges", () => {
  it("newest failed job badges the shot with its reason", () => {
    const { generating, failed } = shotJobBadges([
      job("shot1", JobState.FAILED, "safety_blocked", "prompt was moderated"),
      job("shot1", JobState.COMPLETE), // older success must not mask the new failure
    ]);
    expect(generating.has("shot1")).toBe(false);
    expect(failed.get("shot1")).toContain("prompt was moderated");
    expect(failed.get("shot1")).toContain("edit the prompt"); // prompt-fault hint
  });

  it("an older failure under a newer success stays quiet", () => {
    const { failed } = shotJobBadges([
      job("shot1", JobState.COMPLETE),
      job("shot1", JobState.FAILED, "transient", "boom"),
    ]);
    expect(failed.size).toBe(0);
  });

  it("an active regeneration suppresses the failure badge and spins instead", () => {
    const { generating, failed } = shotJobBadges([
      job("shot1", JobState.RUNNING),
      job("shot1", JobState.FAILED, "transient", "boom"),
    ]);
    expect(generating.has("shot1")).toBe(true);
    expect(failed.has("shot1")).toBe(false);
  });

  it("untargeted jobs badge nothing; failures without detail get a generic reason", () => {
    const { generating, failed } = shotJobBadges([
      job("", JobState.FAILED, "", ""),
      job("shot2", JobState.FAILED, "", ""),
    ]);
    expect(generating.size).toBe(0);
    expect(failed.get("shot2")).toBe("generation failed");
  });
});

describe("retryability", () => {
  it("safety_blocked and invalid_input are prompt faults; transient is not", () => {
    expect(isPromptFault("safety_blocked")).toBe(true);
    expect(isPromptFault("invalid_input")).toBe(true);
    expect(isPromptFault("transient")).toBe(false);
    expect(isPromptFault("")).toBe(false);
  });

  it("failure text prefers the message, falls back to code, then generic", () => {
    expect(jobFailureText(job("s", JobState.FAILED, "transient", "dial tcp refused"))).toBe("dial tcp refused");
    expect(jobFailureText(job("s", JobState.FAILED, "transient", ""))).toBe("transient");
    expect(jobFailureText(job("s", JobState.FAILED, "", ""))).toBe("generation failed");
  });
});
