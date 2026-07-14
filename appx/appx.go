// Package appx implements enough of the AppX/MSIX package-identity model,
// and Windows' offline (un-booted, pre-OOBE) provisioned-package
// bookkeeping, to remove a provisioned package from a factory Windows image
// without booting it or shelling out to real DISM.
//
// # AppxManifest.xml
//
// Identity parses just the <Identity> element of a package's
// AppxManifest.xml - Name/Publisher/Version/ProcessorArchitecture/
// ResourceId, under namespace
// "http://schemas.microsoft.com/appx/manifest/foundation/windows10". This
// part is officially documented: see the "Package manifest schema
// reference for Windows 10" Microsoft Learn page
// (https://learn.microsoft.com/en-us/uwp/schemas/appxpackage/uapmanifestschema/schema-root)
// and its "Identity" element reference
// (https://learn.microsoft.com/en-us/uwp/schemas/appxpackage/uapmanifestschema/element-identity).
// Elements outside Identity are not modeled - this package only needs enough
// to identify a package, not to reproduce Windows' app-activation/manifest
// semantics.
//
// # PackageFamilyName derivation
//
// PackageFamilyName computes "<Name>_<PublisherID>" from a package's Name
// and Publisher, matching the (not officially documented in prose, only
// exposed via the PackageFamilyNameFromId Win32 API - see
// https://learn.microsoft.com/en-us/windows/win32/api/appmodel/nf-appmodel-packagefamilynamefromid)
// algorithm: SHA-256 the UTF-16LE-encoded Publisher string, Crockford
// Base32-encode the first 8 bytes (65 bits: the 64 hash bits followed by one
// zero padding bit, split into 13 groups of 5 bits), lowercase. This
// package's implementation was written by directly reading (not guessing
// from prose descriptions) the reference Rust reimplementation
// github.com/russellbanks/package-family-name (cloned to /tmp/package-family-name
// during development, 2026-07-14) - see familyname.go's doc comments for
// exactly which functions were consulted. Cross-checked against real data:
// Microsoft's well-known publisher string ("CN=Microsoft Corporation, ...")
// produces "8wekyb3d8bbwe", matching every Microsoft-published package
// folder name observed in a real Windows 11 23H2 image (e.g.
// Microsoft.MicrosoftStickyNotes_4.0.6104.0_x64__8wekyb3d8bbwe's own
// AppxManifest.xml Identity, extracted 2026-07-14 via guestmount).
//
// # AppxProvisioning.xml and offline removal
//
// See provisioning.go and remove.go for the offline provisioned-package
// list format and removal procedure - both reverse-engineered by direct
// inspection of a real Windows 11 23H2 image (not documented by Microsoft
// under this filename), recorded in this repo's top-level TODO.md's "AppX
// provisioned-package subsystem" entry, and re-confirmed 2026-07-14 against
// a fresh guestmount extraction (ProgramData\Microsoft\Windows\
// AppxProvisioning.xml, plus the SOFTWARE hive's
// Microsoft\Windows\CurrentVersion\Appx\AppxAllUserStore tree via
// hivexregedit --export).
//
// # Explicit non-goals
//
//   - Anything requiring the runtime StateRepository-Machine.srd SQLite
//     database: confirmed absent on a pristine, un-booted image (only
//     created at first boot/specialize), so out of scope for offline
//     servicing entirely - see TODO.md's research trail.
//   - Full AppxManifest.xml parsing (Properties, Applications, Extensions,
//     Dependencies, etc): only Identity is modeled, since that is all a
//     provisioned-package removal needs to identify a package.
//   - Authenticode/package signature verification.
package appx
