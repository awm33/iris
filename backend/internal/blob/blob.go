// Package blob wraps the S3-compatible object store (MinIO in dev):
// presigned PUT/GET and the content-addressing move performed at
// CompleteUpload (temp key → sha256/<hash>).
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint  string // host:port, no scheme
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
	// PublicEndpoint rewrites presigned URL hosts for browser access when the
	// API reaches MinIO via a different host than the browser does. Empty =
	// use Endpoint as-is.
	PublicEndpoint string
}

type Store struct {
	client *minio.Client
	cfg    Config
}

func New(cfg Config) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	return &Store{client: client, cfg: cfg}, nil
}

func (s *Store) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.cfg.Bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return s.public(u), nil
}

func (s *Store) PresignGet(ctx context.Context, key, contentType string, expiry time.Duration) (string, error) {
	params := url.Values{}
	if contentType != "" {
		params.Set("response-content-type", contentType)
	}
	u, err := s.client.PresignedGetObject(ctx, s.cfg.Bucket, key, expiry, params)
	if err != nil {
		return "", err
	}
	return s.public(u), nil
}

// HashAndPromote streams the temp object, computes sha256, copies it to the
// content-addressed key sha256/<hex>, and deletes the temp object. Dedup is
// free: an existing destination is left in place.
func (s *Store) HashAndPromote(ctx context.Context, tempKey string) (hash string, size int64, rdr func() (io.ReadCloser, error), err error) {
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, tempKey, minio.GetObjectOptions{})
	if err != nil {
		return "", 0, nil, err
	}
	h := sha256.New()
	size, err = io.Copy(h, obj)
	obj.Close()
	if err != nil {
		return "", 0, nil, fmt.Errorf("hash temp object: %w", err)
	}
	hash = hex.EncodeToString(h.Sum(nil))
	dst := ContentKey(hash)

	if _, err := s.client.StatObject(ctx, s.cfg.Bucket, dst, minio.StatObjectOptions{}); err != nil {
		// Not already present — server-side copy.
		if _, err := s.client.CopyObject(ctx,
			minio.CopyDestOptions{Bucket: s.cfg.Bucket, Object: dst},
			minio.CopySrcOptions{Bucket: s.cfg.Bucket, Object: tempKey}); err != nil {
			return "", 0, nil, fmt.Errorf("promote to %s: %w", dst, err)
		}
	}
	_ = s.client.RemoveObject(ctx, s.cfg.Bucket, tempKey, minio.RemoveObjectOptions{})

	rdr = func() (io.ReadCloser, error) {
		return s.client.GetObject(ctx, s.cfg.Bucket, dst, minio.GetObjectOptions{})
	}
	return hash, size, rdr, nil
}

// PutObject writes derived artifacts (posters, proxies, waveforms) under
// non-content-addressed keys owned by their producer.
func (s *Store) PutObject(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

func ContentKey(sha string) string { return "sha256/" + sha }

func (s *Store) public(u *url.URL) string {
	if s.cfg.PublicEndpoint != "" {
		u.Host = s.cfg.PublicEndpoint
	}
	return u.String()
}
