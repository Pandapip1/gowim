// Package driver ties together the sibling gowim format packages (wim, inf,
// cat, pe) to support installing a Windows driver package - an .inf plus its
// accompanying .cat catalog and payload files (.sys, .dll, ...) - into a WIM
// image's in-memory directory-entry tree.
//
// # What this package models
//
// Given a driver package's files (accessed through an fs.FS), LoadPackage
// parses the .inf (via package inf) and chases just enough of the documented
// INF directive semantics to enumerate the package's payload files - the
// files a real installation would copy onto the target machine. Specifically,
// it interprets (each cited to the Microsoft Learn "Windows Hardware /
// drivers / install" documentation, as of 2026-07-10):
//
//   - The [Manufacturer] section (one or more "manufacturer-name" or
//     "%strkey%=models-section-name[,TargetOSVersion...]" entries), see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-manufacturer-section.
//   - The per-manufacturer Models section(s) it points to
//     ("device-description=install-section-name,hw-id[,compatible-id...]"),
//     see https://learn.microsoft.com/windows-hardware/drivers/install/inf-models-section.
//   - The install-section-name.CopyFiles directive (an install section may be
//     decorated ".NT"/".NT<arch>" for a target platform), whose value is
//     either "@filename" (a direct copy using DefaultDestDir) or a list of
//     file-list-section names, see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-copyfiles-directive.
//   - The [SourceDisksFiles] (and platform-decorated
//     [SourceDisksFiles.<arch>]) section, mapping a source file name to a
//     disk ID and optional subdirectory, see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-sourcedisksfiles-section.
//   - The [DestinationDirs] section (a file-list-section name, or
//     DefaultDestDir, mapped to a numeric dirid and optional subdir), and the
//     standard DIRID directory-ID values (DirID* constants in dirid.go), see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-destinationdirs-section
//     and https://learn.microsoft.com/windows-hardware/drivers/install/using-dirids.
//   - The [Version] section's CatalogFile / CatalogFile.<platform> entry
//     (resolved by inf.File.CatalogFileForPlatform), naming the .cat file
//     that normally sits alongside the .inf.
//
// Deliberate simplifications of the above, since this package's job is only
// to enumerate a package's payload *files* faithfully enough to hash and
// install them, not to reproduce Windows' exact install-time section
// selection: it does not evaluate TargetOSVersion decorations (OS
// major/minor version, product type, suite mask, build number) when
// choosing among repeated Manufacturer/Models/DDInstall section variants;
// instead it unions the entries of every "<name>", "<name>.NT", and
// "<name>.NT<platform>" variant for the single caller-supplied architecture
// token. It also does not resolve the multi-disk [SourceDisksNames] "disk
// root" or tag-file mechanism - a payload file's location is always taken to
// be the optional SourceDisksFiles subdir underneath the INF's own
// directory, i.e. it assumes a single, already-unpacked driver source tree
// rather than removable distribution media.
//
// # Explicit non-goals
//
// This package deliberately does NOT implement:
//
//   - The Windows DriverStore's FileRepository path-hashing scheme (the
//     "<infname>_<hash>" folder naming under
//     \Windows\System32\DriverStore\FileRepository\). That scheme is
//     undocumented/reverse-engineered, not sourced from an authoritative
//     spec, and this repo's policy is to never speculate about undocumented
//     internals. This was checked empirically, not just assumed: extracting
//     a real Windows 11 23H2 install.esd's FileRepository and INF
//     directories showed byte-identical copies of e.g. "1394.inf" stored
//     under "1394.inf_amd64_f05cd2933ff9e649", but MD5, SHA-1, and SHA-256 of
//     that exact INF file (full digest and both truncated ends) all disagree
//     with the folder's 16 hex-character suffix, for every package checked -
//     so the suffix is not a simple hash of the INF's bytes, and reproducing
//     it would require reverse-engineering (or replicating unknown internal
//     state of) setupapi.dll/drvstore.dll, which is out of scope here.
//     Instead, Install takes the destination directory path(s) for each
//     DIRID used by the package as an explicit parameter.
//   - Editing or constructing Windows registry hives (the SYSTEM hive's
//     DriverDatabase keys, INFCACHE.1, etc). No registry-hive parser exists
//     anywhere in this repo; building one is a separate, large piece of
//     work.
//   - PnP class-installer semantics, driver ranking/selection among multiple
//     matching drivers, or AddService/AddReg directive interpretation beyond
//     what is needed to enumerate a package's payload files (CopyFiles,
//     SourceDisksFiles, and DestinationDirs are interpreted; AddService,
//     AddReg, and the rest of the install graph are not).
//   - Authenticode signature verification or X.509 certificate validation,
//     relying entirely on the cat package's own non-goals here: Verify only
//     performs structural hash comparison against the catalog's recorded
//     digests.
//   - The final WIM-file writer (resource offset assignment, header/blob
//     table serialization into a new output file). That belongs in a future
//     addition to the wim package, not here: Install only returns in-memory
//     *wim.DirEntry nodes and a slice of new blob content to add, given an
//     existing *wim.ImageMetadata / *wim.BlobTable to extend.
package driver

import "fmt"

// wrapErr is a small helper for adding context to errors, matching the
// sibling packages' convention.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("driver: %s: %w", what, err)
}
