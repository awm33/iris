import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { assetClient } from "../api";

// Shared version thumbnail. kind-aware: images skip the guaranteed-404 poster
// probe entirely; videos try poster first (post-probe) then fall back to the
// original, with an <img> onError guard for the pre-probe mp4 case.
export function VersionThumb({
  versionId,
  kind,
  className = "thumb",
  variant = "poster",
}: {
  versionId: string;
  kind?: "image" | "video";
  className?: string;
  /** Derived-frame override, e.g. "last_frame" for the continuity carry. */
  variant?: string;
}) {
  const [imgFailed, setImgFailed] = useState(false);
  const tryPoster = kind !== "image";

  const poster = useQuery({
    queryKey: ["artifact-thumb", versionId, variant],
    enabled: versionId !== "" && tryPoster,
    retry: false,
    staleTime: 10 * 60 * 1000,
    queryFn: () => assetClient.signDownload({ versionId, variant }),
  });
  const posterNotFound = poster.error instanceof ConnectError && poster.error.code === Code.NotFound;
  const original = useQuery({
    queryKey: ["artifact-thumb", versionId, "original"],
    enabled: versionId !== "" && (!tryPoster || posterNotFound),
    retry: 1,
    staleTime: 10 * 60 * 1000,
    queryFn: () => assetClient.signDownload({ versionId }),
  });
  const url = (tryPoster ? poster.data?.url : undefined) ?? original.data?.url;

  // A failed <img> (pre-probe mp4) must recover when the URL changes (the
  // probe lands, the poster query refetches) — the latch resets per URL.
  useEffect(() => setImgFailed(false), [url]);

  return url && !imgFailed ? (
    <img className={className} src={url} alt="" onError={() => setImgFailed(true)} />
  ) : (
    <div className={`${className} thumb-placeholder-sm`}>▢</div>
  );
}

// Resolves an asset id to its head version (for callers that only hold an
// asset id — character refs, view plates; both are images by validation).
export function AssetThumb({
  assetId,
  kind = "image",
  className = "thumb",
}: {
  assetId: string;
  kind?: "image" | "video";
  className?: string;
}) {
  const asset = useQuery({
    queryKey: ["asset", assetId],
    enabled: assetId !== "",
    staleTime: 60 * 1000,
    queryFn: () => assetClient.getAsset({ id: assetId }),
  });
  const head = asset.data?.asset?.headVersionId ?? "";
  if (!head) return <div className={`${className} thumb-placeholder-sm`}>▢</div>;
  return <VersionThumb versionId={head} kind={kind} className={className} />;
}

// Escape-to-close for modal pickers (UX doc §5 keyboard operability, minimum).
export function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}
