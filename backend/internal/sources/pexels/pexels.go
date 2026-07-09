// Package pexels is the stock-photo source connector for Pexels
// (https://www.pexels.com/api/). Imports are by photo ID — the server
// resolves download URLs from the Pexels API itself, so no user-supplied URL
// is ever fetched (no SSRF surface).
package pexels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	baseURL     = "https://api.pexels.com/v1"
	perPage     = 24
	maxDownload = 40 << 20 // largest curated Pexels photos are well under 40MB
)

type Client struct {
	key  string
	http *http.Client
}

// New returns nil when no key is configured — callers treat nil as
// "source unavailable" and surface a setup hint.
func New(key string) *Client {
	if key == "" {
		return nil
	}
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}}
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
		Photographer: p.Photographer, PhotographerURL: p.PhotographerURL,
		ThumbURL: p.Src.Medium, downloadURL: dl,
	}
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

// Download streams the photo's bytes (size-capped). The URL comes exclusively
// from the Pexels API response, never from the client.
func (c *Client) Download(ctx context.Context, p Photo) (io.ReadCloser, error) {
	if p.downloadURL == "" {
		return nil, fmt.Errorf("photo %d has no downloadable rendition", p.ID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.downloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("pexels download: HTTP %d", resp.StatusCode)
	}
	return newLimitedReadCloser(resp.Body, maxDownload), nil
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
		return fmt.Errorf("pexels rejected the API key")
	case 429:
		return fmt.Errorf("pexels rate limit reached — try again shortly")
	default:
		return fmt.Errorf("pexels: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

type limitedReadCloser struct {
	r      io.Reader
	closer io.Closer
}

func newLimitedReadCloser(rc io.ReadCloser, limit int64) io.ReadCloser {
	return &limitedReadCloser{r: io.LimitReader(rc, limit), closer: rc}
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.closer.Close() }
