package wufetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// Download fetches f's URL and writes it to w, verifying the downloaded
// bytes' SHA-256 against f.SHA256 (as reported by UUP dump's get.php,
// itself taken from the real Windows Update SOAP response -- see
// uupdump.go's File doc comment) if f.SHA256 is non-empty. It returns an
// error without partial data being trusted if the hash doesn't match.
func Download(ctx context.Context, client *http.Client, f File, w io.Writer) error {
	if f.URL == "" {
		return fmt.Errorf("wufetch: file %q has no download URL", f.Name)
	}
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return fmt.Errorf("wufetch: build download request for %q: %w", f.Name, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("wufetch: download %q: %w", f.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wufetch: download %q: HTTP %d", f.Name, resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return fmt.Errorf("wufetch: download %q: %w", f.Name, err)
	}

	if f.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != f.SHA256 {
			return fmt.Errorf("wufetch: download %q: SHA-256 mismatch: got %s, want %s", f.Name, got, f.SHA256)
		}
	}
	return nil
}

// DownloadCapability is the high-level, one-call path this package exists
// for: given a build (see Client.FindBuild/ListBuilds), a capability name,
// and an architecture, it locates and downloads that capability's payload
// .cab, verifying its SHA-256 against Microsoft's own declared hash for the
// file.
//
// packageFamily is passed through to FindCapabilityFile; pass "" to use
// KnownCapabilityPackages.
func (c *Client) DownloadCapability(ctx context.Context, build Build, capabilityName, arch, packageFamily string, w io.Writer) (File, error) {
	files, err := c.GetFiles(ctx, build.UUID, "neutral", "FOD")
	if err != nil {
		return File{}, fmt.Errorf("wufetch: list FOD files for build %s (%s): %w", build.Build, build.UUID, err)
	}

	f, err := FindCapabilityFile(files, capabilityName, arch, packageFamily)
	if err != nil {
		return File{}, err
	}

	if err := Download(ctx, c.httpClient(), f, w); err != nil {
		return File{}, err
	}
	return f, nil
}
