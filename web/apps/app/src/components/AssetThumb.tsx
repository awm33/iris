import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { assetClient } from "../api";

// Shared version thumbnail: poster variant first (videos post-probe; images
// 404 → fall back to the original). <img> onError guards the pre-probe-video
// case where the original is an mp4.
export function VersionThumb({ versionId, className = "thumb" }: { versionId: string; className?: string }) {
  const [imgFailed, setImgFailed] = useState(false);
  const poster = useQuery({
    queryKey: ["artifact-thumb", versionId, "poster"],
    enabled: versionId !== "",
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
      className={className}
      src={url}
      alt=""
      onError={() => setImgFailed(true)}
      onLoad={() => setImgFailed(false)}
    />
  ) : (
    <div className={`${className} thumb-placeholder-sm`}>▢</div>
  );
}

// Resolves an asset id to its head version, then renders the thumb.
export function AssetThumb({ assetId, className = "thumb" }: { assetId: string; className?: string }) {
  const asset = useQuery({
    queryKey: ["asset", assetId],
    enabled: assetId !== "",
    staleTime: 60 * 1000,
    queryFn: () => assetClient.getAsset({ id: assetId }),
  });
  const head = asset.data?.asset?.headVersionId ?? "";
  if (!head) return <div className={`${className} thumb-placeholder-sm`}>▢</div>;
  return <VersionThumb versionId={head} className={className} />;
}
