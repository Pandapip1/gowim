# gowim/fido

A Go library that locates official Microsoft **Windows retail ISO download
links**, by reimplementing (in pure Go, independent of any HTTP framework)
the request flow of [Fido.ps1](https://github.com/pbatard/Fido), the
well-known PowerShell script that scrapes Microsoft's public "software
download connector" API — the same API
`https://www.microsoft.com/software-download/windows11` itself uses
client-side — to obtain a direct, signed ISO download URL without a browser
or the Media Creation Tool GUI.

Given a `(Version, Release, Edition, Language, Arch)` selection,
`fido.FetchDownloadURL` performs the same session handshake and API calls
Fido.ps1 does and returns a `Result{URL, FileName}`. It never downloads the
ISO itself — that's for the caller, with any HTTP client (the resolved URLs
support byte-range requests; see "Live verification" below).

## Evidence and sourcing

Per this repo's convention (see the root README and e.g. `mum/README.md`),
claims here are graded by source:

- **Reverse-engineered / observed behavior (not documented by Microsoft
  anywhere)**: the entire protocol — the `orgID`/`profileID`/`instanceID`
  identifiers, the `vlscppe.microsoft.com` + `ov-df.microsoft.com` session
  handshake, the `productEditionId` → SKU → download-link request chain, and
  the `DownloadType` → architecture mapping. This is exactly what
  [Fido.ps1](https://github.com/pbatard/Fido/blob/master/Fido.ps1) is: an
  actively maintained reverse-engineering of Microsoft's internal API, and
  it is the source this package's protocol logic is ported from (function
  names referenced in code comments — `Get-Windows-Version-Data`,
  `Get-Windows-Languages`, `Get-Windows-Download-Url`,
  `Get-Code-715-123130-Message` — correspond to Fido.ps1's own).
- A second-generation Go port,
  [`github.com/Pandapip1/packer-plugin-windows-utils`](https://github.com/Pandapip1/packer-plugin-windows-utils)'s
  `datasource/windows_iso` package, was used as the direct porting base (it
  is closer to Go idiom than translating PowerShell line-by-line), but it is
  itself just Fido.ps1's logic wrapped in Packer/HCL glue — every claim
  below not cited to Fido.ps1 or to a live Microsoft response was
  independently re-verified rather than trusted from that port's comments.
- **Personally observed, live** (2026-09-14, this package's development):
  every network-facing constant, endpoint, and response shape below was
  exercised against Microsoft's real servers, not assumed from either
  source's comments. See `fido.go`'s package doc comment for the full
  request/response detail; summary:
  - The full session handshake (`vlscppe.microsoft.com/tags`,
    `ov-df.microsoft.com/mdt.js` + its `w=`/`rticks=` echo-back) completed
    with HTTP 200 at every step.
  - All four `productEditionId` values carried over from the reference
    implementation (`3321`/`3324` for Windows 11 25H2 v2, `2618`/`2378` for
    Windows 10 22H2) each resolved to a live, non-empty, correctly labeled
    SKU list via `getskuinformationbyproductedition`.
  - Resolving the English SKU under `productEditionId` 3321 (the
    Home/Pro/Edu x64/x86 group) through `GetProductDownloadLinksBySku`
    returned one `DownloadType: 1` link at
    `https://software.download.prss.microsoft.com/dbazure/Win11_25H2_English_x64_v2.iso?...`.
    A `HEAD` request against that **exact, live** URL — not a URL merely
    constructed by inspection — returned `200 OK`,
    `Content-Length: 8471603200` (≈7.9 GiB, right for a retail Windows 11
    x64 ISO), `Accept-Ranges: bytes`, and a `Content-Disposition` naming the
    same file. The equivalent request under `productEditionId` 3324 (the
    ARM64-only group) returned `DownloadType: 2` at a
    `Win11_25H2_English_Arm64_v2.iso` URL, independently confirming the
    `DownloadType` 1↔x64 / 2↔ARM64 mapping against a real response.
  - **One real piece of protocol drift was found and fixed.** The `msg-01`
    hidden-input banned-message HTML on
    `https://www.microsoft.com/en-us/software-download/windows11` now
    contains a literal newline inside the attribute value (an embedded
    anchor tag's attributes wrap onto a second line). Go's RE2 `.` does not
    match `\n` without the `(?s)` flag, so the reference implementation's
    un-flagged pattern was confirmed, by running it against a live fetch of
    that exact page, to match **zero** times. `api.go`'s `msgPattern` adds
    `(?s)`; re-run against the same live page, it matches, and the
    reconstructed message still cites message code `715-123130` unchanged —
    so only the regex needed fixing, not the fallback wording.
    `TestBannedMessageRegexHandlesEmbeddedNewline` in `fido_test.go` pins
    this down with a fixture modeled on the real HTML, and additionally
    asserts the pre-fix pattern really does fail on it (so this is a
    verified regression test, not just a shape check).

## What was changed relative to the reference implementation, and why

- **No `github.com/google/uuid` dependency.** Every other gowim module has
  zero third-party dependencies; a session identifier only needs "128
  random bits with the UUID v4 version/variant bits set", which
  `crypto/rand` plus two byte masks (`newSessionID` in `fido.go`) gives
  directly, without pulling in an external package for this one call.
- **`context.Context` throughout.** `FetchDownloadURL` and every internal
  HTTP call take a `context.Context`, so a caller embedding this in a larger
  program can cancel or time-bound the whole resolution (a build pipeline
  waiting on this shouldn't hang indefinitely on a stalled Microsoft
  endpoint). The reference implementation, constrained by Packer's
  `Datasource.Execute() (cty.Value, error)` signature, had no way to accept
  one.
- **The `msg-01` regex fix above** — genuine drift found via live testing,
  not a stylistic change.
- **No cache.** See "Caching" below.

Everything else — the constants, the handshake sequence, the
`productEditionId` table, the `DownloadType` mapping, the UEFI Shell static
GitHub-release path — is carried over essentially unchanged, because it was
verified live to still be correct; there was no concrete reason found to
diverge further.

## Caching

This package deliberately has **no built-in cache**, unlike the
`packer-plugin-windows-utils` reference (which persists resolved results
under `os.UserCacheDir()`, since a Packer datasource has no natural place to
keep state across separate `packer build` invocations). Reasons:

- A resolved URL here is nothing more than a `{URL, FileName}` pair; wrapping
  `FetchDownloadURL` with a cache keyed on `Config` is a few lines for any
  caller that wants one, using whatever storage/TTL policy fits that
  caller — a build server, a provisioning tool, or a one-shot CLI all want
  different things from a cache, and a plain library function has no basis
  to pick one for all of them.
  A plain Go library function also has no business writing to a user's
  cache directory implicitly, the way the reference implementation's
  `cache.go` does, without the caller opting in.
- The download URLs Microsoft hands back are themselves short-lived signed
  links (the `DownloadExpirationDatetime` observed live above was about 24
  hours out), so caching mainly matters as a way to avoid repeating the
  handshake (and risking a `715-123130` IP ban from doing so too often) —
  not as a way to reuse the URL itself for long. A caller that wants that
  protection can cache the whole `Result` for a short TTL exactly as easily
  outside this package as `cache.go` did inside it.

## Scope and non-goals

- Resolves a selection to a URL; does not download the ISO.
- Does not implement Fido's own interactive/commandline "leave it unset to
  mean latest/host-native" defaulting — `Config`'s `Version`/`Release`/
  `Edition` are always required, and `Language`/`Arch` are required for
  retail Windows selections, so a caller's build pipeline can't silently
  drift onto a different SKU than it was pinned to.
- The `productEditionId`/release tables in `versions.go` are a point-in-time
  snapshot (current as of 2026-09-14, matching the reference implementation
  at time of porting) of what Microsoft currently serves — see the package
  doc comment and above for exactly what was re-verified live. Microsoft
  only keeps the latest release of each Windows version downloadable, so
  these will need updating again as new releases ship; this is inherent to
  the protocol (Fido.ps1 itself is updated for the same reason), not a gap
  specific to this port.

## Usage

```go
ctx := context.Background()
result, err := fido.FetchDownloadURL(ctx, fido.Config{
    Version:  "Windows 11",
    Release:  "Latest",
    Edition:  "Home/Pro/Edu",
    Language: "English",
    Arch:     "x64",
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.URL, result.FileName)
```

## Tests

```
go test ./...

# Additionally exercise the real network path (resolves and HEADs a live,
# current retail Windows 11 ISO and a live UEFI Shell release asset):
GOWIM_TEST_NETWORK=1 go test -run TestNetwork -v ./...
```

Plain `go test ./...` never touches the network — `network_test.go`'s tests
are skipped unless `GOWIM_TEST_NETWORK=1` is set, mirroring the sibling
`component` module's `GOWIM_TEST_IMAGE` env-var-gated pattern for its
real-image tests. The gated tests were run for real during development (see
"Evidence and sourcing" above for the exact response observed) and resolved
a genuine, live, correctly sized (`Content-Length: 8471603200`) Windows 11
25H2 x64 English retail ISO, plus a live UEFI Shell 2.2 24H2 release asset.

## License

MIT OR Apache-2.0.
