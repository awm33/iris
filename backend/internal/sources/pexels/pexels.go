// Package pexels is the stock-photo source connector for Pexels
// (https://www.pexels.com/api/). Imports are by photo ID — the server
// resolves download URLs from the Pexels API itself, so no user-supplied URL
// is ever fetched. The API is still only semi-trusted: resolved URLs must be
// https on *.pexels.com, re-validated on every redirect hop, and downloads
// are size-capped with truncation detected (not silently stored).
package pexels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL     = "https://api.pexels.com/v1"
	perPage     = 24
	maxDownload = 40 << 20 // largest curated Pexels renditions are well under 40MB
)

// Sentinel errors let the API layer map to accurate codes (a dead key must
// not be retried as if it were transient).
var (
	ErrAuth          = errors.New("pexels rejected the API key")
	ErrRateLimited   = errors.New("pexels rate limit reached — try again shortly")
	ErrPhotoNotFound = errors.New("photo not found on pexels")
)

type Client struct {
	key      string
	http     *http.Client
	download *http.Client
}

// New returns nil when no key is configured — callers treat nil as
// "source unavailable" and surface a setup hint.
func New(key string) *Client {
	if key == "" {
		return nil
	}
	return &Client{
		key:  key,
		http: &http.Client{Timeout: 30 * time.Second},
		download: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return validateDownloadURL(req.URL)
			},
		},
	}
}

type Photo struct {
	ID              int64
	Width, Height   int
	Alt             string
	Photographer    string
	PhotographerURL string
	ThumbURL        string
	downloadURL     string
}

type apiPhoto struct {
	ID              int64  `json:"id"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Alt             string `json:"alt"`
	Photographer    string `json:"photographer"`
	PhotographerURL string `json:"photographer_url"`
	Src             struct {
		Large2x string `json:"large2x"`
		Large   string `json:"large"`
		Medium  string `json:"medium"`
	} `json:"src"`
}

func (p apiPhoto) toPhoto() Photo {
	dl := p.Src.Large2x
	if dl == "" {
		dl = p.Src.Large
	}
	return Photo{
		ID: p.ID, Width: p.Width, Height: p.Height, Alt: p.Alt,
		Photographer: p.Photographer,
		// API-supplied URLs flow into <a href> and stored metadata — only
		// http(s) survives (a javascript: URL must never reach the client).
		PhotographerURL: httpOnly(p.PhotographerURL),
		ThumbURL:        httpOnly(p.Src.Medium),
		downloadURL:     dl,
	}
}

func httpOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return raw
}

// validateDownloadURL constrains where the server will connect for photo
// bytes: https, pexels-owned hosts only. Applied to the resolved URL AND
// every redirect hop.
func validateDownloadURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https download url")
	}
	host := u.Hostname()
	if host != "pexels.com" && !strings.HasSuffix(host, ".pexels.com") {
		return fmt.Errorf("refusing download from non-pexels host %q", host)
	}
	return nil
}

func (c *Client) Search(ctx context.Context, query string, page int) (photos []Photo, hasMore bool, err error) {
	if page < 1 {
		page = 1
	}
	u := fmt.Sprintf("%s/search?query=%s&page=%d&per_page=%d",
		baseURL, url.QueryEscape(query), page, perPage)
	var out struct {
		Photos   []apiPhoto `json:"photos"`
		NextPage string     `json:"next_page"`
	}
	if err := c.get(ctx, u, &out); err != nil {
		return nil, false, err
	}
	for _, p := range out.Photos {
		photos = append(photos, p.toPhoto())
	}
	return photos, out.NextPage != "", nil
}

// Resolve fetches a photo's metadata (and its server-chosen download URL).
func (c *Client) Resolve(ctx context.Context, id int64) (Photo, error) {
	var p apiPhoto
	if err := c.get(ctx, baseURL+"/photos/"+strconv.FormatInt(id, 10), &p); err != nil {
		return Photo{}, err
	}
	return p.toPhoto(), nil
}

// Download fetches the photo's bytes into memory (hard-capped at maxDownload,
// with truncation detected — an oversized photo fails, it never imports
// corrupt). The URL comes exclusively from the Pexels API response, never
// from the client, and is still validated: https + pexels-owned host,
// re-checked on every redirect hop.
func (c *Client) Download(ctx context.Context, p Photo) (data []byte, contentType string, err error) {
	if p.downloadURL == "" {
		return nil, "", fmt.Errorf("photo %d has no downloadable rendition", p.ID)
	}
	u, err := url.Parse(p.downloadURL)
	if err != nil {
		return nil, "", fmt.Errorf("bad download url: %w", err)
	}
	if err := validateDownloadURL(u); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.download.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("pexels download: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxDownload {
		return nil, "", fmt.Errorf("photo is %dMB — exceeds the %dMB import limit",
			resp.ContentLength>>20, maxDownload>>20)
	}
	data, err = readCapped(resp.Body, maxDownload)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// readCapped reads at most limit bytes. It reads one byte past the limit so
// truncation is distinguishable from a clean EOF exactly at it — an oversized
// stream is an explicit error, never a silently-stored partial file.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, io.LimitReader(r, limit+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > limit {
		return nil, fmt.Errorf("photo exceeds the %dMB import limit", limit>>20)
	}
	return buf.Bytes(), nil
}

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case 200:
	case 401, 403:
		return ErrAuth
	case 404:
		return ErrPhotoNotFound
	case 429:
		return ErrRateLimited
	default:
		return fmt.Errorf("pexels: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}
