package api

import (
	"context"
	"errors"
	"image"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/store"
)

type AssetServer struct {
	Store *store.Store
	Blob  *blob.Store
}

const (
	putExpiry = 30 * time.Minute
	getExpiry = 15 * time.Minute
)

func (s *AssetServer) StartUpload(ctx context.Context, req *connect.Request[irisv1.StartUploadRequest]) (*connect.Response[irisv1.StartUploadResponse], error) {
	m := req.Msg
	if m.Filename == "" || m.ContentType == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("filename and content_type are required"))
	}
	u := &store.PendingUpload{
		WorkspaceID: workspaceID(m.WorkspaceId),
		ProjectID:   m.ProjectId,
		Filename:    m.Filename,
		ContentType: m.ContentType,
		SizeBytes:   m.SizeBytes,
	}
	if err := s.Store.CreatePendingUpload(ctx, u); err != nil {
		return nil, connectErr(err)
	}
	putURL, err := s.Blob.PresignPut(ctx, u.ObjectKey, putExpiry)
	if err != nil {
		return nil, connectErr(err)
	}
	// Single-part in M1; multipart lands with large-file support.
	return connect.NewResponse(&irisv1.StartUploadResponse{
		UploadId:    u.ID,
		PartPutUrls: []string{putURL},
	}), nil
}

func (s *AssetServer) CompleteUpload(ctx context.Context, req *connect.Request[irisv1.CompleteUploadRequest]) (*connect.Response[irisv1.CompleteUploadResponse], error) {
	u, err := s.Store.TakePendingUpload(ctx, req.Msg.UploadId)
	if err != nil {
		return nil, connectErr(err)
	}
	hash, size, open, err := s.Blob.HashAndPromote(ctx, u.ObjectKey)
	if err != nil {
		return nil, connectErr(err)
	}

	v := &store.AssetVersion{SHA256: hash, ContentType: u.ContentType, SizeBytes: size}
	// Image dimensions via stdlib decode (video/audio probe is the
	// media-worker's job — M2; those versions start with null dims/duration).
	if strings.HasPrefix(u.ContentType, "image/") {
		if rc, err := open(); err == nil {
			if cfg, _, err := image.DecodeConfig(rc); err == nil {
				v.Width, v.Height = int32(cfg.Width), int32(cfg.Height)
			}
			rc.Close()
		}
	}

	a := &store.Asset{
		WorkspaceID: u.WorkspaceID,
		ProjectID:   u.ProjectID,
		Kind:        kindFromContentType(u.ContentType),
		Name:        u.Filename,
	}
	if err := s.Store.CreateAssetWithVersion(ctx, a, v); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CompleteUploadResponse{
		Asset:   assetPB(a),
		Version: versionPB(v),
	}), nil
}

func (s *AssetServer) GetAsset(ctx context.Context, req *connect.Request[irisv1.GetAssetRequest]) (*connect.Response[irisv1.GetAssetResponse], error) {
	a, versions, err := s.Store.GetAsset(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.GetAssetResponse{Asset: assetPB(a)}
	for _, v := range versions {
		resp.Versions = append(resp.Versions, versionPB(v))
	}
	return connect.NewResponse(resp), nil
}

func (s *AssetServer) ListAssets(ctx context.Context, req *connect.Request[irisv1.ListAssetsRequest]) (*connect.Response[irisv1.ListAssetsResponse], error) {
	assets, err := s.Store.ListAssets(ctx,
		workspaceID(req.Msg.WorkspaceId), req.Msg.ProjectId, kindString(req.Msg.Kind), req.Msg.Query)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListAssetsResponse{}
	for _, a := range assets {
		resp.Assets = append(resp.Assets, assetPB(a))
	}
	return connect.NewResponse(resp), nil
}

func (s *AssetServer) GetLineage(ctx context.Context, req *connect.Request[irisv1.GetLineageRequest]) (*connect.Response[irisv1.GetLineageResponse], error) {
	up, down, err := s.Store.GetLineage(ctx, req.Msg.VersionId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.GetLineageResponse{}
	for _, l := range up {
		resp.Upstream = append(resp.Upstream, linkPB(l))
	}
	for _, l := range down {
		resp.Downstream = append(resp.Downstream, linkPB(l))
	}
	return connect.NewResponse(resp), nil
}

func (s *AssetServer) SignDownload(ctx context.Context, req *connect.Request[irisv1.SignDownloadRequest]) (*connect.Response[irisv1.SignDownloadResponse], error) {
	sha, contentType, err := s.Store.GetVersionObjectInfo(ctx, req.Msg.VersionId)
	if err != nil {
		return nil, connectErr(err)
	}
	url, err := s.Blob.PresignGet(ctx, blob.ContentKey(sha), contentType, getExpiry)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.SignDownloadResponse{
		Url:         url,
		ExpiresUnix: time.Now().Add(getExpiry).Unix(),
	}), nil
}

// ── mapping helpers ───────────────────────────────────────────────────────────

func kindFromContentType(ct string) string {
	switch {
	case strings.HasPrefix(ct, "image/"):
		return "image"
	case strings.HasPrefix(ct, "video/"):
		return "video"
	case strings.HasPrefix(ct, "audio/"):
		return "audio"
	default:
		return "image" // safest default for M1; refine with probe results in M2
	}
}

var kindToPB = map[string]irisv1.AssetKind{
	"image": irisv1.AssetKind_ASSET_KIND_IMAGE,
	"video": irisv1.AssetKind_ASSET_KIND_VIDEO,
	"audio": irisv1.AssetKind_ASSET_KIND_AUDIO,
	"model_3d": irisv1.AssetKind_ASSET_KIND_MODEL_3D,
	"lut":   irisv1.AssetKind_ASSET_KIND_LUT,
	"font":  irisv1.AssetKind_ASSET_KIND_FONT,
}

func kindString(k irisv1.AssetKind) string {
	for s, pb := range kindToPB {
		if pb == k {
			return s
		}
	}
	return ""
}

func assetPB(a *store.Asset) *irisv1.Asset {
	return &irisv1.Asset{
		Id: a.ID, WorkspaceId: a.WorkspaceID, ProjectId: a.ProjectID,
		Kind: kindToPB[a.Kind], Name: a.Name, HeadVersionId: a.HeadVersionID,
		Tags: a.Tags, Timestamps: ts(a.CreatedAt, a.UpdatedAt),
	}
}

func versionPB(v *store.AssetVersion) *irisv1.AssetVersion {
	return &irisv1.AssetVersion{
		Id: v.ID, AssetId: v.AssetID, Sha256: v.SHA256, ContentType: v.ContentType,
		SizeBytes: v.SizeBytes, Width: v.Width, Height: v.Height,
		DurationS: v.DurationS, Fps: v.FPS,
		Timestamps: ts(v.CreatedAt, v.CreatedAt),
	}
}

func linkPB(l *store.AssetLink) *irisv1.AssetLink {
	role := irisv1.LinkRole_LINK_ROLE_UNSPECIFIED
	switch l.Role {
	case "generated_by":
		role = irisv1.LinkRole_LINK_ROLE_GENERATED_BY
	case "reference_of":
		role = irisv1.LinkRole_LINK_ROLE_REFERENCE_OF
	case "conditioning_frame_of":
		role = irisv1.LinkRole_LINK_ROLE_CONDITIONING_FRAME_OF
	case "derived_from":
		role = irisv1.LinkRole_LINK_ROLE_DERIVED_FROM
	case "used_in_take":
		role = irisv1.LinkRole_LINK_ROLE_USED_IN_TAKE
	}
	return &irisv1.AssetLink{FromVersionId: l.FromVersionID, ToEntityId: l.ToEntityID, Role: role}
}
