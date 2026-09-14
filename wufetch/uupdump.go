// Package wufetch locates and downloads a Windows Feature-on-Demand
// ("Capability", e.g. `OpenSSH.Server~~~~0.0.1.0`) payload for a specific
// Windows build/architecture, from a Linux host, without a running Windows
// Update Agent. See README.md for the full research trail (what was tried,
// what worked, what didn't, and the evidence grading) behind this package's
// design. In short: the Microsoft Update Catalog does not index Capabilities
// by name (verified empirically, see README's "Route 1"); the officially
// documented "Languages and Optional Features ISO" is real but gated behind
// VLSC for the exact build/edition needed (README's "Route 2"); the route
// that actually works, used here, is the public UUP dump API
// (api.uupdump.net), a third-party service that reimplements Microsoft's
// real (and otherwise undocumented) Windows Update SOAP sync protocol on
// the caller's behalf and re-exposes it as a plain JSON/HTTPS API (README's
// "Route 3"). Every file this package returns is downloaded directly from
// Microsoft's own update CDN (`*.dl.delivery.mp.microsoft.com`); only the
// *metadata lookup* (which UUID/URL corresponds to a given build+capability)
// goes through uupdump.net.
package wufetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// DefaultAPIBase is the public UUP dump JSON API's base URL, as documented
// (endpoint names and parameters) by its own server-side source at
// https://git.uupdump.net/uup-dump/json-api (fetched and read directly,
// 2026-09-14) and confirmed against the live server by this package's own
// requests (see README.md's "Route 3" for the exact request/response shapes
// observed). uupdump.net is a long-running, widely-used community project
// (see README for citations) but is not a Microsoft or gowim-controlled
// service; callers who need stronger guarantees can point Client.APIBase at
// a self-hosted instance of the same (also open-source, same repository)
// server.
const DefaultAPIBase = "https://api.uupdump.net"

// Client talks to a UUP dump JSON API instance.
type Client struct {
	// APIBase is the API's base URL, no trailing slash. Defaults to
	// DefaultAPIBase.
	APIBase string

	// HTTPClient is used for all requests. Defaults to http.DefaultClient.
	HTTPClient *http.Client
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return c.APIBase
	}
	return DefaultAPIBase
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := c.apiBase() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("wufetch: build request for %s: %w", u, err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("wufetch: request %s: %w", u, err)
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("wufetch: decode response from %s (HTTP %d): %w", u, resp.StatusCode, err)
	}
	if len(envelope.Response) == 0 {
		return fmt.Errorf("wufetch: empty response from %s (HTTP %d)", u, resp.StatusCode)
	}

	// The API reports its own errors with HTTP 200 and a JSON
	// {"error": "SOME_CODE"} body inside "response" (confirmed empirically,
	// e.g. querying an unsupported lang/edition combination returns
	// {"response":{"error":"UNSUPPORTED_COMBINATION"}} with HTTP 200) as
	// well as via non-2xx HTTP statuses for some failure modes (get.php's
	// own source sets http_response_code(500)/(400) for others). Surface
	// both uniformly as an *APIError.
	var apiErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(envelope.Response, &apiErr); err == nil && apiErr.Error != "" {
		return &APIError{Code: apiErr.Error}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wufetch: %s: HTTP %d", u, resp.StatusCode)
	}

	if err := json.Unmarshal(envelope.Response, out); err != nil {
		return fmt.Errorf("wufetch: decode response body from %s: %w", u, err)
	}
	return nil
}

// APIError is returned when the UUP dump API itself reports a named error
// (its "error" codes, e.g. "UNSUPPORTED_COMBINATION", "NO_UPDATE_FOUND" --
// see the api and json-api repositories' PHP source for the full set this
// package observed while researching this).
type APIError struct{ Code string }

func (e *APIError) Error() string { return "wufetch: uupdump API error: " + e.Code }

// Build is one entry from listid.php: a specific Windows build+architecture
// combination UUP dump has already indexed.
type Build struct {
	UUID    string `json:"uuid"`
	Title   string `json:"title"`
	Build   string `json:"build"`
	Arch    string `json:"arch"`
	Created int64  `json:"created"`
}

// ListBuilds wraps listid.php: it lists every build UUP dump has already
// resolved an update ID for, optionally filtered by a substring/regex
// search term (the same "search" semantics listid.php itself implements --
// see api/listid.php's uupListIds, read directly from
// https://git.uupdump.net/uup-dump/api). This only returns builds already
// present in UUP dump's own database. UUP dump's own `fetchupd.php` can
// trigger a live Windows Update sync for a build it hasn't indexed yet, but
// this package deliberately doesn't wrap that endpoint: every build this
// package's own real target (Windows 11 25H2/26200) and its near neighbors
// need is already indexed (confirmed empirically), and fetchupd.php's own
// request shape is materially more involved (ring/flight/branch/sku
// parameters with no single obviously-correct default) than justified for
// a package whose scope is "download a Capability for a build the caller
// already knows the number of". A caller who needs an unindexed build can
// hit fetchupd.php directly (its own JSON API mirrors ListBuilds' shape)
// and pass the resulting UUID to GetFiles.
func (c *Client) ListBuilds(ctx context.Context, search string) ([]Build, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	// The live API's "builds" field is a JSON *object* keyed by an opaque,
	// server-assigned numeric-string index (confirmed empirically against
	// the real https://api.uupdump.net/listid.php endpoint, 2026-09-14 --
	// not documented as such anywhere found), not an array -- decode it as
	// a map and discard the keys, which carry no meaning to this package's
	// callers (ListBuilds imposes its own, documented ordering below
	// instead of relying on the server's key order).
	var out struct {
		Builds map[string]Build `json:"builds"`
	}
	if err := c.getJSON(ctx, "/listid.php", q, &out); err != nil {
		return nil, err
	}

	builds := make([]Build, 0, len(out.Builds))
	for _, b := range out.Builds {
		builds = append(builds, b)
	}

	// Deterministic order for callers/tests: newest build number first,
	// then by arch, then by UUID (listid.php's own ordering depends on a
	// server-side cache and its "sortByDate" flag, neither of which this
	// package's callers should have to depend on).
	sort.Slice(builds, func(i, j int) bool {
		bi, bj := builds[i].Build, builds[j].Build
		if bi != bj {
			return buildLess(bj, bi) // descending
		}
		if builds[i].Arch != builds[j].Arch {
			return builds[i].Arch < builds[j].Arch
		}
		return builds[i].UUID < builds[j].UUID
	})
	return builds, nil
}

func buildLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

// FindBuild returns the single newest indexed Build matching arch (e.g.
// "amd64") whose Build string starts with buildPrefix (e.g. "26200" or
// "26200.9457"). It is a thin, opinionated convenience over ListBuilds for
// the common "I know the build number and arch" case; use ListBuilds
// directly for anything more general.
func (c *Client) FindBuild(ctx context.Context, buildPrefix, arch string) (Build, error) {
	builds, err := c.ListBuilds(ctx, buildPrefix)
	if err != nil {
		return Build{}, err
	}
	for _, b := range builds {
		if strings.EqualFold(b.Arch, arch) && strings.HasPrefix(b.Build, buildPrefix) {
			return b, nil
		}
	}
	return Build{}, fmt.Errorf("wufetch: no indexed build matching prefix %q arch %q (searched %d builds)", buildPrefix, arch, len(builds))
}

// File is one entry from get.php's file list: a single downloadable file
// (an OS component .cab/.esd, a language pack, a Feature-on-Demand
// package, ...) for a specific build.
type File struct {
	// Name is the file's cleaned-up name (get.php's own normalization --
	// see api/get.php's uupCleanSha256/uupCleanName, read directly from
	// https://git.uupdump.net/uup-dump/api -- strips the
	// "~31bf3856ad364e35" public key token and collapses "~~." to "."),
	// e.g. "OpenSSH-Server-Package-amd64.cab". This is NOT necessarily the
	// exact on-the-wire CBS package name (see README's "Naming" section).
	Name string

	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	// Size is the file size in bytes as reported by get.php. Its own JSON
	// output has been observed to encode this as either a JSON number or a
	// numeric string (PHP's json_encode does this for values it computed as
	// a float -- e.g. get.php's own estimated-size fallback path,
	// `$size = ($temp - 1) * 31457280`, for a file whose real size wasn't
	// in UUP dump's cached metadata yet -- confirmed empirically, 2026-09-14,
	// against a real build's real file list), so Size has a custom
	// UnmarshalJSON accepting both.
	Size int64 `json:"size"`
	// URL is a direct, time-limited download link to Microsoft's real
	// delivery CDN (typically a `*.dl.delivery.mp.microsoft.com` host),
	// already resolved server-side by UUP dump's `WU_composeFileGetRequest`
	// equivalent -- no further UUP dump involvement is needed to fetch the
	// bytes.
	URL string `json:"url"`
}

// UnmarshalJSON implements the Size string-or-number tolerance documented
// on the Size field above: "size" is decoded as raw JSON, then parsed as a
// float regardless of whether it arrived quoted or bare (a bare JSON
// number's raw bytes parse as a float just as well as a quoted one's
// unquoted bytes do).
func (f *File) UnmarshalJSON(data []byte) error {
	var raw struct {
		SHA1   string          `json:"sha1"`
		SHA256 string          `json:"sha256"`
		Size   json.RawMessage `json:"size"`
		URL    string          `json:"url"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.SHA1 = raw.SHA1
	f.SHA256 = raw.SHA256
	f.URL = raw.URL

	sizeStr := strings.Trim(string(raw.Size), `"`)
	if sizeStr != "" && sizeStr != "null" {
		sz, err := strconv.ParseFloat(sizeStr, 64)
		if err != nil {
			return fmt.Errorf("wufetch: parse file size %q: %w", sizeStr, err)
		}
		f.Size = int64(sz)
	}
	return nil
}

// filesResponse mirrors get.php's JSON shape; File.Name isn't itself a JSON
// field (it's the map key), so decoding happens in two passes.
type filesResponse struct {
	UpdateName string                     `json:"updateName"`
	Arch       string                     `json:"arch"`
	Build      string                     `json:"build"`
	Files      map[string]json.RawMessage `json:"files"`
}

// GetFiles wraps get.php: given a build's update ID (Build.UUID, optionally
// with a "_rev.N" suffix as UUP dump itself uses for superseded revisions),
// a language pack ("neutral" for language-independent content -- which is
// where FOD/Capability packages live, per this package's own empirical
// testing, see README), and an edition selector, returns every file UUP
// dump can resolve for that combination. edition "FOD" (Features on Demand)
// is the one this package's own DownloadCapability uses; it is intentionally
// left undocumented in listeditions.php's own output (see api/listeditions.php)
// but is a real, working value -- confirmed directly against the live API
// (README's "Route 3").
func (c *Client) GetFiles(ctx context.Context, updateID, lang, edition string) (map[string]File, error) {
	q := url.Values{}
	q.Set("id", updateID)
	if lang == "" {
		lang = "neutral"
	}
	q.Set("lang", lang)
	if edition != "" {
		q.Set("edition", edition)
	}

	var raw filesResponse
	if err := c.getJSON(ctx, "/get.php", q, &raw); err != nil {
		return nil, err
	}

	files := make(map[string]File, len(raw.Files))
	for name, msg := range raw.Files {
		var f File
		if err := json.Unmarshal(msg, &f); err != nil {
			return nil, fmt.Errorf("wufetch: decode file entry %q: %w", name, err)
		}
		f.Name = name
		files[name] = f
	}
	return files, nil
}
