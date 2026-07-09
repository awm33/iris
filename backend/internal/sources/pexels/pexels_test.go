package pexels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateDownloadURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://images.pexels.com/photos/1/a.jpg", true},
		{"https://pexels.com/x.jpg", true},
		{"https://www.pexels.com/x.jpg", true},
		{"http://images.pexels.com/photos/1/a.jpg", false},  // https only
		{"https://evil.com/x.jpg", false},                   // wrong host
		{"https://images.pexels.com.evil.com/x.jpg", false}, // suffix spoof
		{"https://notpexels.com/x.jpg", false},              // substring != suffix
		{"http://169.254.169.254/latest/meta-data/", false}, // metadata endpoint
		{"https://images.pexels.com:8443/x.jpg", true},      // port irrelevant, host checked
	}
	for _, c := range cases {
		u, err := url.Parse(c.url)
		if err != nil {
			t.Fatalf("parse %q: %v", c.url, err)
		}
		if got := validateDownloadURL(u) == nil; got != c.ok {
			t.Errorf("validateDownloadURL(%q) allowed=%v, want %v", c.url, got, c.ok)
		}
	}
}

// A compromised API response pointing Download at an arbitrary server must be
// refused before any connection is made.
func TestDownloadRefusesNonPexelsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("Download connected to a non-pexels host")
	}))
	defer srv.Close()

	c := New("test-key")
	_, _, err := c.Download(context.Background(), Photo{ID: 1, downloadURL: srv.URL + "/x.jpg"})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("want refusal error, got %v", err)
	}
}

func TestHTTPOnly(t *testing.T) {
	if httpOnly("javascript:alert(1)") != "" {
		t.Error("javascript: URL survived httpOnly")
	}
	if httpOnly("data:text/html,x") != "" {
		t.Error("data: URL survived httpOnly")
	}
	if httpOnly("https://www.pexels.com/@someone") == "" {
		t.Error("https URL was blanked")
	}
	if httpOnly("http://example.com") == "" {
		t.Error("http URL was blanked")
	}
}

func TestReadCapped(t *testing.T) {
	if _, err := readCapped(strings.NewReader("123456789"), 8); err == nil {
		t.Error("stream past the cap must error, not truncate silently")
	}
	data, err := readCapped(strings.NewReader("12345678"), 8)
	if err != nil || string(data) != "12345678" {
		t.Errorf("exact-limit stream should pass whole: %q, %v", data, err)
	}
	data, err = readCapped(strings.NewReader("1234"), 8)
	if err != nil || string(data) != "1234" {
		t.Errorf("under-limit stream should pass whole: %q, %v", data, err)
	}
}
