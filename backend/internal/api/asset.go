package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/sources/pexels"
	"github.com/awm33/iris/backend/internal/store"
)

type AssetServer struct {
	Store  *store.Store
	Blob   *blob.Store
	Pexels *pexels.Client // nil = source not configured
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
	// Video/audio need the ffprobe pass (duration/fps/dims, poster); the job
	// is enqueued in the same transaction as the asset rows.
	needsProbe := a.Kind == "video" || a.Kind == "audio"
	if err := s.Store.CreateAssetWithVersion(ctx, a, v, needsProbe, nil); err != nil {
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
	info, err := s.Store.GetVersionObjectInfo(ctx, req.Msg.VersionId)
	if err != nil {
		return nil, connectErr(err)
	}
	var key, contentType string
	switch req.Msg.Variant {
	case "":
		key, contentType = blob.ContentKey(info.SHA256), info.ContentType
	case "poster":
		if info.PosterKey == "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("poster not generated yet"))
		}
		key, contentType = info.PosterKey, "image/jpeg"
	case "proxy", "filmstrip", "first_frame", "last_frame", "waveform":
		k, ok := info.DerivedKeys[req.Msg.Variant]
		if !ok {
			return nil, connect.NewError(connect.CodeNotFound,
				errors.New(req.Msg.Variant+" not generated yet"))
		}
		key = k
		switch req.Msg.Variant {
		case "proxy":
			contentType = "video/mp4"
		case "filmstrip":
			contentType = "image/jpeg"
		case "waveform":
			contentType = "application/json"
		default:
			contentType = "image/png"
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown variant: "+req.Msg.Variant))
	}
	url, err := s.Blob.PresignGet(ctx, key, contentType, getExpiry)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.SignDownloadResponse{
		Url:         url,
		ExpiresUnix: time.Now().Add(getExpiry).Unix(),
	}), nil
}

// ── stock sources ─────────────────────────────────────────────────────────────

const maxStockQueryLen = 200

func (s *AssetServer) SearchStock(ctx context.Context, req *connect.Request[irisv1.SearchStockRequest]) (*connect.Response[irisv1.SearchStockResponse], error) {
	if s.Pexels == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("no stock source configured — set IRIS_PEXELS_API_KEY and restart the api"))
	}
	q := strings.TrimSpace(req.Msg.Query)
	if q == "" || len([]rune(q)) > maxStockQueryLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is required (max 200 chars)"))
	}
	photos, hasMore, err := s.Pexels.Search(ctx, q, int(req.Msg.Page))
	if err != nil {
		return nil, pexelsErr(err)
	}
	resp := &irisv1.SearchStockResponse{HasMore: hasMore}
	for _, p := range photos {
		resp.Photos = append(resp.Photos, &irisv1.StockPhoto{
			Source: "pexels", Id: strconv.FormatInt(p.ID, 10),
			ThumbUrl: p.ThumbURL, Width: int32(p.Width), Height: int32(p.Height),
			Alt: p.Alt, Photographer: p.Photographer, PhotographerUrl: p.PhotographerURL,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *AssetServer) ImportStock(ctx context.Context, req *connect.Request[irisv1.ImportStockRequest]) (*connect.Response[irisv1.ImportStockResponse], error) {
	if s.Pexels == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("no stock source configured — set IRIS_PEXELS_API_KEY and restart the api"))
	}
	if req.Msg.Source != "pexels" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown stock source"))
	}
	id, err := strconv.ParseInt(req.Msg.Id, 10, 64)
	if err != nil || id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad photo id"))
	}

	// Idempotent: re-importing a photo returns the existing asset (matched on
	// provenance meta) instead of a duplicate row — and skips the Pexels
	// quota entirely.
	if existingID, err := s.Store.FindStockAsset(ctx,
		store.DevWorkspaceID, req.Msg.ProjectId, "pexels", req.Msg.Id); err == nil {
		a, versions, err := s.Store.GetAsset(ctx, existingID)
		if err == nil && len(versions) > 0 {
			return connect.NewResponse(&irisv1.ImportStockResponse{
				Asset:   assetPB(a),
				Version: versionPB(versions[0]),
			}), nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, connectErr(err)
	}

	// The server resolves the download URL from the Pexels API itself — the
	// client only names an id; no user-supplied URL is ever fetched (and the
	// pexels client re-validates the resolved URL: https, *.pexels.com only).
	photo, err := s.Pexels.Resolve(ctx, id)
	if err != nil {
		return nil, pexelsErr(err)
	}
	data, contentType, err := s.Pexels.Download(ctx, photo)
	if err != nil {
		return nil, pexelsErr(err)
	}

	// Trust neither the header nor the bytes alone: normalize the reported
	// type, sniff when it isn't an image, and require a successful decode —
	// a CDN error page must fail the import, not land as a "healthy" asset.
	contentType, _, _ = strings.Cut(contentType, ";")
	contentType = strings.TrimSpace(contentType)
	if !strings.HasPrefix(contentType, "image/") {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(contentType, "image/") {
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("pexels returned non-image content (%s)", contentType))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("downloaded photo did not decode as an image: %w", err))
	}

	tempKey := "uploads/stock/" + ids.New("imp")
	if err := s.Blob.PutObject(ctx, tempKey, contentType, bytes.NewReader(data), int64(len(data))); err != nil {
		return nil, connectErr(err)
	}
	hash, size, _, err := s.Blob.HashAndPromote(ctx, tempKey)
	if err != nil {
		return nil, connectErr(err)
	}

	v := &store.AssetVersion{
		SHA256: hash, ContentType: contentType, SizeBytes: size,
		Width: int32(cfg.Width), Height: int32(cfg.Height),
	}
	name := photo.Alt
	if name == "" {
		name = "pexels " + req.Msg.Id
	}
	if photo.Photographer != "" {
		name = fmt.Sprintf("%s — %s", name, photo.Photographer)
	}
	a := &store.Asset{
		WorkspaceID: store.DevWorkspaceID,
		ProjectID:   req.Msg.ProjectId,
		Kind:        "image",
		Name:        truncateRunes(name, 200),
	}
	// Attribution rides on the version, written in the same transaction as
	// the asset rows (Pexels guidelines: credit the photographer and Pexels).
	meta := map[string]any{
		"source":           "pexels",
		"source_id":        req.Msg.Id,
		"photographer":     photo.Photographer,
		"photographer_url": photo.PhotographerURL,
	}
	if err := s.Store.CreateAssetWithVersion(ctx, a, v, false, meta); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.ImportStockResponse{
		Asset:   assetPB(a),
		Version: versionPB(v),
	}), nil
}

// pexelsErr maps connector sentinels to accurate codes — a dead key is a
// configuration problem, not a retryable outage.
func pexelsErr(err error) *connect.Error {
	switch {
	case errors.Is(err, pexels.ErrAuth):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, pexels.ErrRateLimited):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, pexels.ErrPhotoNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
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
	"image":    irisv1.AssetKind_ASSET_KIND_IMAGE,
	"video":    irisv1.AssetKind_ASSET_KIND_VIDEO,
	"audio":    irisv1.AssetKind_ASSET_KIND_AUDIO,
	"model_3d": irisv1.AssetKind_ASSET_KIND_MODEL_3D,
	"lut":      irisv1.AssetKind_ASSET_KIND_LUT,
	"font":     irisv1.AssetKind_ASSET_KIND_FONT,
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
