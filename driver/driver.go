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
//
//   - The per-manufacturer Models section(s) it points to
//     ("device-description=install-section-name,hw-id[,compatible-id...]"),
//     see https://learn.microsoft.com/windows-hardware/drivers/install/inf-models-section.
//
//   - The install-section-name.CopyFiles directive (an install section may be
//     decorated ".NT"/".NT<arch>" for a target platform), whose value is
//     either "@filename" (a direct copy using DefaultDestDir) or a list of
//     file-list-section names, see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-copyfiles-directive.
//
//   - The [SourceDisksFiles] (and platform-decorated
//     [SourceDisksFiles.<arch>]) section, mapping a source file name to a
//     disk ID and optional subdirectory, see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-sourcedisksfiles-section.
//
//   - The [DestinationDirs] section (a file-list-section name, or
//     DefaultDestDir, mapped to a numeric dirid and optional subdir), and the
//     standard DIRID directory-ID values (DirID* constants in dirid.go), see
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-destinationdirs-section
//     and https://learn.microsoft.com/windows-hardware/drivers/install/using-dirids.
//
//   - The [Version] section's CatalogFile / CatalogFile.<platform> entry
//     (resolved by inf.File.CatalogFileForPlatform), naming the .cat file
//     that normally sits alongside the .inf.
//
//   - The AddService directive chain: an install section's (platform-
//     decorated) "<install-section-name>.Services" section
//     (see "INF DDInstall.Services Section",
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-ddinstall-services-section)
//     containing one or more AddService directives (see "INF AddService
//     Directive",
//     https://learn.microsoft.com/windows-hardware/drivers/install/inf-addservice-directive),
//     each naming a service-install-section with ServiceType, StartType,
//     ErrorControl, ServiceBinary ("%dirid%\path", resolved via the same
//     DirID model as dirid.go/PayloadFile), and optional LoadOrderGroup and
//     Dependencies entries (service.go's ServiceInstall,
//     (*Package).Services).
//
//   - The documented well-known Services registry key schema
//     (HKLM\SYSTEM\CurrentControlSet\Services\<name>'s Type/Start/
//     ErrorControl/ImagePath/Group/DependOnGroup/DependOnService values) and
//     the Select\Default -> ControlSetNNN resolution a SYSTEM hive's
//     "current control set" conventionally requires: both are generic,
//     INF-independent Windows-service-registry concepts, so they live in the
//     sibling service package (github.com/Pandapip1/gowim/service -
//     service.Install, service.CurrentControlSet; see that package's
//     citations). registryinstall.go's InstallRegistry resolves each
//     ServiceInstall's BinaryDirID+BinaryPath (via the same DirID model as
//     dirid.go/PayloadFile) into a plain ImagePath string and delegates the
//     rest to service.Install.
//
//   - The CriticalDeviceDatabase mechanism for registering a device's
//     hardware ID against a service before PnP ever sees the device (see
//     criticaldevicedatabase.go's citations) - unlike Services, this *is*
//     PnP/driver-install-specific, so it stays here, written into a
//     caller-supplied *regf.Key tree (registryinstall.go's
//     mergeCriticalDeviceDatabase) via the sibling regf package
//     (github.com/Pandapip1/gowim/regf) for the on-disk hive shape, reusing
//     the service package's exported FindOrCreateSubkey/SetValue navigation
//     helpers rather than a second private copy of that logic.
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
//   - The SYSTEM hive's DriverDatabase key tree
//     (SYSTEM\DriverDatabase\DriverPackages, DeviceIds, etc), the internal
//     driver-ranking database PnP uses to choose among multiple matching
//     drivers for a device. Checked, not just assumed: a documentation
//     search turned up no authoritative Microsoft Learn (or equivalent)
//     page describing this schema - unlike CriticalDeviceDatabase and the
//     Services key schema, both of which are documented (see
//     registryinstall.go's and the sibling service package's citation
//     trails and reasoning, mirroring the empirical-verification rigor of
//     the DriverStore path-hash non-goal above).
//   - The Enum device-instance tree
//     (SYSTEM\CurrentControlSet\Enum\<enumerator>\<device-id>\<instance-id>).
//     Registering an actual device instance requires a real, live hardware
//     instance ID discovered by PnP enumeration at boot/setup time, which an
//     offline image-prep tool does not have.
//   - INFCACHE.1 (the binary cache of parsed INF metadata SetupAPI
//     maintains under %SystemRoot%\INF): a distinct, undocumented binary
//     format in its own right.
//   - AddReg/DelReg directives in general, beyond the specific Services and
//     CriticalDeviceDatabase registry values enumerated above: a generic,
//     much broader directive mechanism this package does not take on.
//   - PnP class-installer semantics and driver ranking/selection among
//     multiple matching drivers for the same device (that is exactly what
//     the undocumented DriverDatabase non-goal above exists to select
//     among).
//   - Registry-hive writing back to disk (deciding which hive file to open,
//     backing it up, replacing it) - this package only produces/merges
//     regf.Key/regf.Value structures given an already-loaded *regf.Hive/
//     *regf.Key; the caller handles file I/O, exactly as Install only
//     returns in-memory nodes and blob bytes rather than writing a WIM file
//     itself.
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
