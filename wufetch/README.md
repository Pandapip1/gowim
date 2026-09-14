# gowim/wufetch

Locates and downloads the actual installable payload of a Windows optional
feature -- a "Capability" in DISM's terminology (`OpenSSH.Server~~~~0.0.1.0`,
RSAT tools, etc.; Settings calls these "Optional Features") -- for a specific
Windows build and architecture, from a Linux host, over plain outbound
HTTPS, with **no running Windows Update Agent, no Windows host, and no
Administrator rights**. This is the "obtain a Capability's payload in the
first place" half of installing one into an offline WIM image; the sibling
`component` package (`component.Install`/`InstallRegistry`) is the other
half, once the files are in hand.

## Why this exists

Retail and volume-licensed Windows install media deliberately excludes most
Capabilities -- they're normally fetched on demand from Windows Update, or
from a separately published "Features on Demand" / "Languages and Optional
Features" ISO. Neither of those is directly usable from a build pipeline
that has network access but no real Windows Update Agent and (for the ISO
route) no Volume Licensing Service Center entitlement for the exact
build/edition being targeted. This package's job is closing that gap for
one concrete case that motivated it -- `OpenSSH.Server~~~~0.0.1.0` for
Windows 11, version 25H2 (build 26200), amd64 -- while staying general
enough to cover other Capabilities on other builds.

## Research trail (routes tried, in order, each graded by evidence)

Every claim below was checked against the real, live service as of
2026-09-14, not assumed from a stale writeup -- see gowim's own `iso/iso.go`
"from memory or plausibility" policy, which this package follows throughout.
Evidence grades follow this repo's own convention: **(i)** official
Microsoft documentation, **(ii)** widely-used open-source
tooling/reverse-engineering writeups, **(iii)** this package's own empirical
testing against the live endpoint.

### Route 1: Microsoft Update Catalog (`catalog.update.microsoft.com`) -- ruled out for Capabilities

The Catalog's `Search.aspx?q=<term>` endpoint turns out to be a **plain,
unauthenticated GET** that returns real results directly in the HTML body
(no SOAP/session dance needed for search itself, contrary to what some older
writeups about `mscatalog`/`MSCatalogDownload`-style tools imply for the
*download-link-resolution* step -- search alone needed none of that) --
**(iii)**, confirmed by fetching `Search.aspx?q=Windows+11` and getting 100+
real driver-update rows back with a bare `curl`.

But searching for `OpenSSH`, `OpenSSH.Server`, `SSH`, `Features on Demand`,
and `Windows Feature On Demand` all returned the Catalog's own
"We did not find any results for ..." response -- **(iii)**. Searching
`RSAT` *does* return real hits, but every one of them is a legacy,
standalone, KB-numbered RSAT installer for Windows Vista/XP/Server
2003/old Windows 10 LTSB builds (e.g. "2024-01 Security Update for Windows
10 Version 1607 for RSAT for x64-based Systems (KB5035238)") -- i.e. the
pre-Capability-model RSAT package, not the modern `Rsat.*.Tools` FOD --
**(iii)**. Conclusion: the Catalog indexes conventional KB/driver updates,
not the FOD/Capability content stream. This route is closed for Capabilities
specifically, not attempted further.

### Route 2: the "Languages and Optional Features" / Features on Demand ISO -- real, documented, but gated for this exact target

Microsoft's own documentation confirms FODs (including Capabilities) are
distributed as `.cab` files on this ISO and names the exact file for this
package's own target: `OpenSSH-Server-Package~31bf3856ad364e35~amd64~~.cab`
-- **(i)**,
[Available features on demand -- Microsoft Learn](https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/features-on-demand-non-language-fod?view=windows-11)
(fetched and read in full, 2026-09-14; this page is also the source of
`capability.go`'s `KnownCapabilityPackages` table -- see below),
[Features On Demand -- Microsoft Learn](https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/features-on-demand-v2--capabilities?view=windows-11).
That same documentation, and independent secondary coverage, is explicit
that **the Enterprise-edition ISO for a given release is only obtainable
through the Volume Licensing Service Center** (or a Visual Studio
subscription) -- **(i)**/**(ii)**. There is no public, unauthenticated,
permanent download URL for it; public consumer download pages generate
only time-limited (24-hour) links for the *base* OS ISO, and those pages'
own text does not offer the Languages-and-Optional-Features ISO at all.
This route is real and would be the cleanest answer if the target were an
Enterprise/VLSC-licensed build, but was not pursued further for this
package's actual target since it can't be automated without credentials
this environment doesn't have.

### Route 3: UUP dump (`api.uupdump.net`) -- what this package actually uses

[UUP dump](https://uupdump.net/) is a long-running, widely used community
project (its own downloader is referenced across Windows-enthusiast tooling
and forums) whose server-side already reimplements Microsoft's real,
otherwise-undocumented Windows Update SOAP sync protocol (`clientSecured`
composer, `GetExtendedUpdateInfo2` requests against Microsoft's real WU
endpoints) and re-exposes the result as a plain JSON/HTTPS API -- **(ii)**.
Its PHP source is public at
[git.uupdump.net/uup-dump/api](https://git.uupdump.net/uup-dump/api) and
[git.uupdump.net/uup-dump/json-api](https://git.uupdump.net/uup-dump/json-api);
this package's author fetched and read `listid.php`, `get.php`,
`listeditions.php`, `listlangs.php`, `updateinfo.php`, `fetchupd.php`, and
`shared/packs.php` directly (not a summary of them) before writing
`uupdump.go`/`capability.go` against the real, live instance -- **(ii)**
for the protocol shape, **(iii)** for every specific request/response
observed below.

Confirmed live, end to end, 2026-09-14:

- `GET https://api.uupdump.net/listid.php?search=25H2` returns real,
  currently-indexed Windows 11 25H2 builds for `amd64`/`arm64`, each with a
  UUID (e.g. build `26200.9539`, UUID `b0605a8c-92ae-456c-b47c-81d48eae5f47`).
  The `"builds"` field is a JSON *object* keyed by an opaque server-assigned
  index, not an array -- not documented anywhere found, discovered only by
  looking at the real response (`ListBuilds` decodes this and re-sorts into
  a documented, deterministic order).
- `GET https://api.uupdump.net/get.php?id=<uuid>&lang=neutral&edition=FOD`
  returns that build's full Features-on-Demand file list -- 494 files for
  the build above, including `OpenSSH-Server-Package-amd64.cab` and
  `OpenSSH-Client-Package-amd64.cab`, each with a SHA-1, a SHA-256, a size,
  and a direct download URL. `edition=FOD` is a real, working value that
  `listeditions.php` deliberately omits from its own public edition list
  (`api/listeditions.php`: `if(in_array($edition, ['LXP', 'FOD'])) continue;`)
  -- found only by reading that source and trying the value against the
  live server anyway.
- That URL (`http://tlu.dl.delivery.mp.microsoft.com/filestreamingservice/files/<guid>?P1=...&P2=...&P3=...&P4=...`)
  is **Microsoft's own real delivery CDN**, not uupdump.net -- downloading
  it returns the real file directly from Microsoft, and its SHA-256 matches
  the hash `get.php` reported *exactly*
  (`fc40c1573a6dacd58c57e2d85fb6fe9b75a7883b054cc541aa9929453cf7c5cc`,
  2 329 588 bytes) -- **(iii)**, and is re-verified by
  `TestRealDownload_OpenSSHServer26200Amd64` (network-gated, see Tests
  below) on every real run.
- The downloaded file is a genuine, valid MS-CAB
  ([Microsoft Cabinet File Format](https://learn.microsoft.com/en-us/previous-versions/bb417343(v=msdn.10)),
  **(i)**), extracted successfully by `cabextract` and containing real
  OpenSSH binaries and servicing manifests: `sshd.exe`, `ssh-keygen.exe`,
  `ssh-agent.exe`, `scp.exe`, `sftp-server.exe`, `ssh-shellhost.exe`,
  `moduli`, `sshd_config_default`, several `.mum`/`.cat`/`.manifest` files,
  and `LICENSE.txt`/`NOTICE.txt` -- **(iii)**.
- Every extracted `.mum` file is plain XML and parses through the sibling
  `mum` package's `Parse` directly (e.g.
  `OpenSSH-Server-Package~31bf3856ad364e35~amd64~~10.0.26100.1.mum` parses
  to identity name `OpenSSH-Server-Package`, package identifier `KB777778`).
  Every extracted `.manifest` file is `DCM`-prefixed PA30 (the 4-byte
  `DCM\x01`-style prefix `component.ParseManifest` also strips -- see its
  doc comment), and decodes successfully through the sibling `pa30`
  package's `DecodeWithSource`, using that package's own real
  `testdata/wcp_dictionary.bin` shared dictionary, to plain XML that itself
  parses via `mum.Parse` (e.g.
  `amd64_openssh-server-components-onecore_..._none_....manifest` decodes
  to identity name `OpenSSH-Server-Components-Onecore`) -- **(iii)**, proving
  this package's output is directly consumable by gowim's existing
  `mum`/`pa30`/`component` stack with no format-layer surprises. This
  end-to-end check was done by hand during development (see git history);
  it is not itself one of this package's own automated tests, since doing
  so would pull a `pa30`/`mum` dependency into a package whose own scope is
  deliberately just "locate and download bytes" -- reproducing it is a
  five-line `pa30.DecodeWithSource(manifestBytes[4:], dict)` +
  `mum.Parse(...)` against this package's own real output.

No corpnet/authentication was needed anywhere in this route: every request
above is a plain, unauthenticated HTTPS GET.

### Route not pursued: reimplementing the raw WU SOAP protocol directly

The task's own stretch goal -- reverse-engineering `fe2.update.microsoft.com`'s
`SyncUpdates`/`CookieGetConfig` SOAP protocol directly, the way
`PSWindowsUpdate` and various from-scratch WU client reimplementations
partially have -- was not attempted, because Route 3 already reaches the
same underlying protocol's *output* (UUP dump's server does exactly this
reimplementation already, and its source is public) without gowim itself
needing to hold WU client certificates/cookies or maintain a second,
harder-to-verify reimplementation of an undocumented SOAP surface. This is
recorded as a real trade-off, not a limitation discovered by trying and
failing: a caller who cannot depend on a third-party service
(`api.uupdump.net`) for the metadata-lookup step would need to either
self-host UUP dump's (also open-source) server or write this protocol layer
directly -- genuinely harder, undocumented, stretch-goal-grade work this
package does not attempt.

## Scope

- `Client.ListBuilds`/`FindBuild` wrap UUP dump's `listid.php`: list or find
  an already-indexed Windows build+architecture and its UUID.
- `Client.GetFiles` wraps `get.php`: given a build UUID, a language pack
  selector (`"neutral"` is where FOD/Capability content lives -- see Route 3
  above), and an edition selector (`"FOD"` for Features on Demand), returns
  every file UUP dump can resolve, each with its declared SHA-1/SHA-256/size
  and a direct Microsoft CDN download URL.
- `FindCapabilityFile` maps a DISM Capability name (e.g.
  `OpenSSH.Server~~~~0.0.1.0`) to one of those files, via
  `KnownCapabilityPackages` -- a small, explicit table copied verbatim from
  Microsoft's own published FOD catalog (cited in `capability.go`), **not**
  a generic name-mangling function: the capability name -> package family
  name mapping is not a mechanical transform (compare `OpenSSH.Server` ->
  `OpenSSH-Server-Package` against `Tools.Graphics.DirectX` ->
  `Microsoft-OneCore-Graphics-Tools-Package`), so a capability not yet in
  the table needs its package family name supplied explicitly rather than
  guessed (`ErrCapabilityUnknown` says so rather than silently
  mismatching).
- `Download`/`Client.DownloadCapability` fetch the bytes and verify them
  against the declared SHA-256, refusing to return unverified data on a
  mismatch.
- **Only two capabilities are seeded in `KnownCapabilityPackages` today**
  (`OpenSSH.Server`, `OpenSSH.Client`) -- the concrete target this package
  was built for, plus its natural sibling. Extending coverage to more
  Capabilities is straightforward (copy another row from the same MS Learn
  page) but intentionally left for whoever needs the next one, per this
  repo's general practice of not building out speculative scope.

## Non-goals / known gaps

- **No raw WU SOAP protocol implementation.** See "Route not pursued" above.
  This package depends on `api.uupdump.net` (or a self-hosted instance of
  the same open-source server, via `Client.APIBase`) for the metadata
  lookup step.
- **No FOD-ISO / repository construction.** This package returns a single
  capability's `.cab` bytes; it does not build a `DISM /export-source`-style
  FOD repository or a bootable/mountable ISO. `component.Install` (the
  sibling package) is the intended next step once the `.cab` is extracted.
- **No `.cab` extraction.** Callers get raw `.cab` bytes back; extracting
  them (this package's own network test does a minimal, read-only
  file-list-only parse for verification purposes, not exposed as public
  API) needs an external tool (`cabextract`, `7z`) or a real MS-CAB decoder,
  neither of which exists elsewhere in gowim today.
- **`KnownCapabilityPackages` is a small seed table, not exhaustive.** See
  Scope above.
- **UUP dump availability is out of gowim's control.** It's a third-party
  service; `Client.APIBase` exists specifically so a caller who needs a
  guarantee gowim can't provide can point at their own instance instead.

## Usage

```go
c := &wufetch.Client{} // or &wufetch.Client{APIBase: "https://my-uupdump-mirror"}

build, err := c.FindBuild(ctx, "26200", "amd64") // Windows 11, version 25H2
// ...

var buf bytes.Buffer
file, err := c.DownloadCapability(ctx, build, "OpenSSH.Server~~~~0.0.1.0", "amd64", "", &buf)
// file.SHA256 is already verified against buf's actual contents by DownloadCapability.
// buf.Bytes() is a real OpenSSH-Server-Package-amd64.cab, ready for cabextract/
// component.ParseManifest/component.Install (see the sibling `component` package).
```

Lower-level access, for a capability not yet in `KnownCapabilityPackages`:

```go
files, err := c.GetFiles(ctx, build.UUID, "neutral", "FOD")
f, err := wufetch.FindCapabilityFile(files, "Some.New.Capability~~~~0.0.1.0", "amd64", "Some-New-Package")
var buf bytes.Buffer
err = wufetch.Download(ctx, nil, f, &buf)
```

## Tests

```
go test ./...
```

runs entirely offline: `uupdump_test.go`/`download_test.go` use
`httptest.Server` fixtures built from real observed response shapes, and
`capability_test.go` exercises `FindCapabilityFile` against synthetic file
lists. None of these touch the network, so `go test ./...` from the
workspace root never requires it.

```
GOWIM_TEST_NETWORK=1 go test -run TestRealDownload -v ./...
```

runs `network_test.go`'s real end-to-end proof: resolves Windows 11 25H2
(build 26200) for `amd64` via the live `api.uupdump.net`, downloads the real
`OpenSSH.Server~~~~0.0.1.0` payload from Microsoft's real update CDN,
verifies its SHA-256, and parses it as a real MS-CAB (via a minimal,
header-only `cabFileNames` reader modeled directly on the documented MS-CAB
layout -- see its doc comment) to confirm the file list contains `sshd.exe`,
`ssh-keygen.exe`, and a `.mum` manifest. This is the test that was actually
run, live, to produce every "confirmed live" claim in this README's Route 3
section above -- it is not a simulation of that check.

## License

MIT OR Apache-2.0.
