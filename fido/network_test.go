package fido

// Network-dependent tests: they perform real requests against Microsoft's
// (and, for UEFI Shell, GitHub's) live servers, so they're gated behind
// GOWIM_TEST_NETWORK=1 and skipped otherwise — plain `go test ./...` for
// this module never touches the network. Run with:
//
//	GOWIM_TEST_NETWORK=1 go test -run TestNetwork -v ./...
//
// This mirrors the env-var-gated pattern the sibling component module uses
// for its real-image tests (GOWIM_TEST_IMAGE), rather than a build tag.

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func skipUnlessNetworkTestsEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("GOWIM_TEST_NETWORK") != "1" {
		t.Skip("set GOWIM_TEST_NETWORK=1 to run tests that make real network requests")
	}
}

// TestNetworkFetchWindows11RetailISO resolves a real, current, retail
// Windows 11 x64 English ISO download URL and confirms — via a live HEAD
// request, not just URL shape — that it is a genuine, correctly sized,
// currently downloadable file. This is the same combination whose
// resolution was manually verified during development; see the package doc
// comment for the literal URL and headers observed then.
func TestNetworkFetchWindows11RetailISO(t *testing.T) {
	skipUnlessNetworkTestsEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := FetchDownloadURL(ctx, Config{
		Version:  "Windows 11",
		Release:  "Latest",
		Edition:  "Home/Pro/Edu",
		Language: "English",
		Arch:     "x64",
	})
	if err != nil {
		t.Fatalf("FetchDownloadURL: %v", err)
	}
	if result.URL == "" {
		t.Fatal("got an empty URL")
	}
	t.Logf("resolved URL: %s", result.URL)
	t.Logf("resolved file name: %s", result.FileName)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, result.URL, nil)
	if err != nil {
		t.Fatalf("building HEAD request: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD %s: %v", result.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD %s: got status %s, want 200", result.URL, resp.Status)
	}
	length, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		t.Fatalf("parsing Content-Length %q: %v", resp.Header.Get("Content-Length"), err)
	}
	// A retail Windows 11 x64 ISO is multiple GiB; a few hundred MiB would
	// indicate an error page or a truncated/placeholder response instead of
	// the real media.
	const minPlausibleSize = 2 << 30 // 2 GiB
	if length < minPlausibleSize {
		t.Fatalf("Content-Length %d is implausibly small for a Windows 11 ISO", length)
	}
	t.Logf("HEAD %s: status=%s Content-Length=%d Accept-Ranges=%q", result.URL, resp.Status, length, resp.Header.Get("Accept-Ranges"))
}

// TestNetworkFetchUEFIShell resolves a UEFI Shell release, which is a static
// GitHub release asset rather than a Microsoft download-connector lookup,
// and confirms it too resolves to a real, live file.
func TestNetworkFetchUEFIShell(t *testing.T) {
	skipUnlessNetworkTestsEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := FetchDownloadURL(ctx, Config{
		Version: "UEFI Shell 2.2",
		Release: "24H2",
		Edition: "Release",
	})
	if err != nil {
		t.Fatalf("FetchDownloadURL: %v", err)
	}
	t.Logf("resolved URL: %s", result.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, result.URL, nil)
	if err != nil {
		t.Fatalf("building HEAD request: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD %s: %v", result.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD %s: got status %s, want 200", result.URL, resp.Status)
	}
}
