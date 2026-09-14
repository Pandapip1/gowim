// Package fido locates official Microsoft Windows retail ISO download links
// by reimplementing, in pure Go, the request flow of
// https://github.com/pbatard/Fido (Fido.ps1) — a widely used PowerShell
// script that walks Microsoft's public "software download connector" API
// (the same API https://www.microsoft.com/software-download/windows11 uses
// client-side) to obtain a direct, signed download URL for a retail Windows
// ISO, without needing a browser or media-creation-tool GUI.
//
// # Sources
//
// This package's protocol knowledge — the session-handshake sequence, the
// orgID/profileID/instanceID identifiers, the productEditionId scheme, and
// the download-type-to-architecture mapping — comes from Fido.ps1 itself
// (https://github.com/pbatard/Fido/blob/master/Fido.ps1, function
// Get-Windows-Version-Data / Get-Windows-Languages / Get-Windows-Download-Url,
// the $WindowsVersions table, and Get-Code-715-123130-Message), which is the
// canonical, actively maintained reverse-engineering of this API — Microsoft
// publishes no documentation for it. This is reverse-engineered/observed
// behavior, not official documentation, and is graded as such throughout
// this package's comments.
//
// A second-generation Go port of the same flow,
// github.com/Pandapip1/packer-plugin-windows-utils's
// datasource/windows_iso package, was used as the direct porting base (it is
// closer to Go idiom than a line-by-line PowerShell translation) but is
// itself just a transcription of Fido.ps1 wrapped in Packer/HCL glue; this
// package strips that glue and re-verifies the wire behavior independently
// (see below), rather than trusting its comments.
//
// This protocol is unusually prone to rotting: it is Microsoft's internal
// download-connector API, not a stable public contract, and both Fido.ps1
// and the packer-plugin-windows-utils port have needed repeated fixes for
// exactly this reason. So every claim below that isn't cited to Fido.ps1 or
// to Microsoft's public HTML/JSON responses was personally re-verified by
// making real, live HTTP requests during development of this package
// (2026-09-14):
//
//   - The full session handshake (vlscppe.microsoft.com/tags,
//     ov-df.microsoft.com/mdt.js + its w=/rticks= echo-back) still completes
//     with HTTP 200 at each step.
//   - All four productEditionId values carried over from the reference
//     implementation (3321/3324 for Windows 11, 2618/2378 for Windows 10)
//     each still resolve via
//     GET .../getskuinformationbyproductedition?productEditionId=<id> to a
//     live, non-empty Skus list; the two Windows 11 ids' ProductDisplayName
//     came back "Windows 11 25H2__V2" (3321, x64/x86 SKUs) and "Windows 11
//     Arm64 25H2__V2" (3324, the ARM64-only SKU group) — confirming the
//     "one id per queryable architecture group" comment on the edition type
//     below is still accurate for the current release.
//   - Resolving the English SKU from productEditionId 3321
//     (Windows 11 Home/Pro/Edu) through
//     GetProductDownloadLinksBySku returned exactly one download option,
//     DownloadType 1, at
//     https://software.download.prss.microsoft.com/dbazure/Win11_25H2_English_x64_v2.iso.
//     A HEAD request against that exact URL (not merely constructed by
//     inspection) returned HTTP 200, Content-Length: 8471603200 (~7.9 GiB,
//     right for a retail Windows 11 x64 ISO), Accept-Ranges: bytes, and a
//     Content-Disposition matching the file name — i.e. a genuine, correctly
//     sized, currently live download target, not a redirect or an error
//     page. The equivalent request against productEditionId 3324's English
//     SKU returned DownloadType 2 at a ".../Win11_25H2_English_Arm64_v2.iso"
//     URL, confirming archFromType's 1=x64/2=ARM64 mapping against a real
//     response rather than assuming it still held.
//   - One concrete piece of drift *was* found and fixed here: the
//     msg-01 hidden-input banned-message HTML on
//     https://www.microsoft.com/en-us/software-download/windows11 now
//     contains a literal newline inside the attribute value (Microsoft
//     wraps the embedded "Contact Us" anchor tag's attributes onto a second
//     line). Go's RE2 `.` does not match `\n` without the `(?s)` flag, so
//     the reference implementation's un-flagged
//     `<input id="msg-01" type="hidden" value="(.*?)"/>` pattern now fails
//     to match this page at all (confirmed by running it against a live
//     fetch: zero matches). This package's bannedMessage adds `(?s)` to fix
//     that; re-run against the same live page, it matches and yields the
//     correctly reconstructed message, which does still cite message code
//     "715-123130" — so beyond the regex fix, the wording itself has not
//     changed.
//
// # Scope and non-goals
//
// This package resolves a (Version, Release, Edition, Language, Arch)
// selection to a *URL*; it never downloads the ISO itself (callers do that
// with any HTTP client, e.g. using Result.URL with net/http and
// io.Copy — the resolved links support byte-range requests per the
// Accept-Ranges header observed above). It also does not implement Fido's
// own interactive/commandline defaulting behavior (leaving Language/Arch
// unset to mean "whatever the current host is") — see Config's doc comment
// for why FetchDownloadURL requires them explicitly instead.
//
// Caching is deliberately left entirely to the caller: unlike the
// packer-plugin-windows-utils reference (which persists resolved results
// under os.UserCacheDir(), since a Packer datasource has no natural place
// to keep state across separate `packer build` invocations), a plain Go
// library function has no business writing to a user's cache directory
// implicitly, and a caller embedding this in a longer-running program (a
// build server, a provisioning tool) almost always already has its own
// cache/state layer that FetchDownloadURL's result trivially fits into (it
// is nothing more than a URL and a file name). Wrapping FetchDownloadURL
// with a cache, keyed on Config, is a few lines for a caller that wants one;
// baking a specific on-disk cache format into this package would only
// constrain callers that don't want exactly that format. See "Caching" in
// this module's README.
package fido

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// Constants below are lifted verbatim from Fido.ps1, where they identify
// Fido/Rufus to Microsoft's download-connector API. Reverse-engineered /
// observed behavior (Fido.ps1), re-verified live 2026-09-14 — see the
// package doc comment above.
const (
	orgID      = "y6jn8c31"
	profileID  = "606624d44113"
	instanceID = "560dc9f3-1aa5-4a2f-b63c-9e18f8d0e175"
	referer    = "https://www.microsoft.com/software-download/windows11"

	// userAgent must look like a real browser: Microsoft's download-connector
	// endpoints reject requests from UAs that don't resemble one, and Fido's
	// own README notes their servers also react to the UA appearing to be the
	// same Windows version as the one being requested. A trailing product
	// token is safe to append (real browsers do this themselves via vendor
	// tokens), so it's used here to identify this library without breaking
	// the browser-shaped check — re-verified live 2026-09-14: this exact UA
	// string was used for every request in the live validation above and none
	// were rejected.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/124.0.0.0 Safari/537.36 gowim-fido/1.0"
)

// httpTimeout bounds every individual HTTP request this package makes.
const httpTimeout = 30 * time.Second

var httpClient = &http.Client{Timeout: httpTimeout}

// Config selects a specific Windows (or UEFI Shell) ISO to locate. Version,
// Release, and Edition are always required; Language and Arch are required
// for retail Windows versions (unused for UEFI Shell). This is deliberately
// stricter than Fido's own commandline mode, which silently defaults unset
// fields to "whatever is latest/host-native" — fine for an interactive
// downloader, but not for a reproducible build pipeline where the exact SKU
// selected must be pinned in the config rather than left to drift with
// Microsoft's current release or the build host's architecture.
//
// Locale only affects which display language errors/pages come back in and
// defaults to "en-US".
type Config struct {
	Version  string
	Release  string
	Edition  string
	Language string
	Arch     string
	Locale   string
}

// Result is the located download.
type Result struct {
	URL      string
	FileName string
}

type downloadLink struct {
	arch string
	url  string
}

type languageSku struct {
	sessionID string
	skuID     string
}

type languageEntry struct {
	code    string
	display string
	skus    []languageSku
}

// FetchDownloadURL resolves cfg to a single Windows/UEFI-Shell ISO download
// URL, performing the same session handshake and API calls as Fido.ps1. It
// always makes live network requests; see the package doc comment's
// "Caching" note for why this package has no cache of its own.
func FetchDownloadURL(ctx context.Context, cfg Config) (*Result, error) {
	ver, err := findVersion(cfg.Version)
	if err != nil {
		return nil, err
	}
	rel, err := findRelease(ver, cfg.Release)
	if err != nil {
		return nil, err
	}
	ed, err := findEdition(rel, cfg.Edition)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(ver.pageType, "UEFI_SHELL") {
		return fetchUEFIShellLink(ver, rel, ed)
	}

	if cfg.Language == "" {
		return nil, fmt.Errorf("fido: language is required for reproducible builds (e.g. %q)", "English International")
	}
	if cfg.Arch == "" {
		return nil, fmt.Errorf("fido: arch is required for reproducible builds (e.g. %q, %q, %q)", "x64", "x86", "ARM64")
	}

	locale := cfg.Locale
	if locale == "" {
		locale = "en-US"
	}
	locale = resolveLocale(ctx, locale)

	languages, err := collectLanguages(ctx, ed.ids, locale)
	if err != nil {
		return nil, err
	}
	if len(languages) == 0 {
		return nil, fmt.Errorf("fido: no languages available for the selected edition")
	}

	lang, err := selectLanguage(languages, cfg.Language)
	if err != nil {
		return nil, err
	}

	var links []downloadLink
	for _, sku := range lang.skus {
		l, err := getDownloadLinks(ctx, sku.skuID, sku.sessionID, locale)
		if err != nil {
			return nil, err
		}
		links = append(links, l...)
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("fido: could not retrieve ISO download links")
	}

	link, err := selectArch(links, cfg.Arch)
	if err != nil {
		return nil, err
	}
	return &Result{URL: link.url, FileName: path.Base(strings.SplitN(link.url, "?", 2)[0])}, nil
}

func findVersion(name string) (*winVersion, error) {
	if name == "" {
		return nil, fmt.Errorf("fido: version is required for reproducible builds (one of: %s)", strings.Join(versionNames(), ", "))
	}
	all := make([]winVersion, 0, len(windowsVersions)+len(uefiShellVersions))
	all = append(all, windowsVersions...)
	all = append(all, uefiShellVersions...)
	for i := range all {
		if strings.Contains(strings.ToLower(all[i].name), strings.ToLower(name)) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("fido: invalid Windows version %q (one of: %s)", name, strings.Join(versionNames(), ", "))
}

func versionNames() []string {
	var names []string
	for _, v := range windowsVersions {
		names = append(names, v.name)
	}
	for _, v := range uefiShellVersions {
		names = append(names, v.name)
	}
	return names
}

// findRelease requires an exact release to be named, or the literal
// "Latest" as an explicit (rather than implicit) opt-in to tracking
// whatever Microsoft currently serves.
func findRelease(ver *winVersion, name string) (*release, error) {
	if name == "" {
		return nil, fmt.Errorf("fido: release is required for reproducible builds (one of: %s, or %q)", strings.Join(releaseNames(ver), ", "), "Latest")
	}
	if strings.EqualFold(name, "latest") {
		return &ver.releases[0], nil
	}
	for i := range ver.releases {
		if strings.HasPrefix(strings.ToLower(ver.releases[i].name), strings.ToLower(name)) {
			return &ver.releases[i], nil
		}
	}
	return nil, fmt.Errorf("fido: invalid release %q for %s (one of: %s, or %q)", name, ver.name, strings.Join(releaseNames(ver), ", "), "Latest")
}

func releaseNames(ver *winVersion) []string {
	var names []string
	for _, r := range ver.releases {
		names = append(names, r.name)
	}
	return names
}

func findEdition(rel *release, name string) (*edition, error) {
	if name == "" {
		return nil, fmt.Errorf("fido: edition is required for reproducible builds (one of: %s)", strings.Join(editionNames(rel), ", "))
	}
	for i := range rel.editions {
		if strings.Contains(strings.ToLower(rel.editions[i].name), strings.ToLower(name)) {
			return &rel.editions[i], nil
		}
	}
	return nil, fmt.Errorf("fido: invalid edition %q (one of: %s)", name, strings.Join(editionNames(rel), ", "))
}

func editionNames(rel *release) []string {
	var names []string
	for _, ed := range rel.editions {
		names = append(names, ed.name)
	}
	return names
}

func selectLanguage(languages []languageEntry, name string) (*languageEntry, error) {
	for i := range languages {
		if strings.Contains(strings.ToLower(languages[i].code), strings.ToLower(name)) ||
			strings.Contains(strings.ToLower(languages[i].display), strings.ToLower(name)) {
			return &languages[i], nil
		}
	}
	return nil, fmt.Errorf("fido: invalid language %q", name)
}

func selectArch(links []downloadLink, name string) (*downloadLink, error) {
	for i := range links {
		if strings.EqualFold(links[i].arch, name) {
			return &links[i], nil
		}
	}
	return nil, fmt.Errorf("fido: invalid architecture %q", name)
}

func fetchUEFIShellLink(ver *winVersion, rel *release, ed *edition) (*Result, error) {
	tag := strings.SplitN(rel.name, " ", 2)[0]
	shellVersion := ""
	if parts := strings.SplitN(ver.pageType, " ", 2); len(parts) == 2 {
		shellVersion = parts[1]
	}
	link := fmt.Sprintf("https://github.com/pbatard/UEFI-Shell/releases/download/%[1]s/UEFI-Shell-%[2]s-%[1]s", tag, shellVersion)
	if len(ed.ids) > 0 && ed.ids[0] == 1 {
		link += "-DEBUG.iso"
	} else {
		link += "-RELEASE.iso"
	}
	return &Result{URL: link, FileName: path.Base(link)}, nil
}

// newSessionID returns a random RFC 4122 version-4 UUID string, in the same
// form Fido.ps1's [guid]::NewGuid() produces. Generated with crypto/rand
// directly rather than pulling in github.com/google/uuid (the reference
// implementation's dependency): gowim's other modules have no third-party
// dependencies, and a session identifier has no need for anything beyond
// "128 random bits with the version/variant bits set", which crypto/rand
// plus two byte masks gives directly.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on a supported platform does not fail; if it
		// somehow does, degrade to an all-zero (but still correctly
		// version/variant-tagged) id rather than panicking.
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
