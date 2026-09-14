package wufetch

import (
	"fmt"
	"strings"
)

// KnownCapabilityPackages maps a Windows Capability name (DISM's
// `/CapabilityName:` value, e.g. "OpenSSH.Server~~~~0.0.1.0" -- the
// `Name~PublicKeyToken~Architecture~Language~Version` shape documented at
// https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/features-on-demand-v2--capabilities,
// "DISM /add-capability" example) to that capability's package "family"
// name -- the file-name prefix shared by every arch/version variant of its
// .cab, with the `~31bf3856ad364e35` public key token, arch, and version
// suffix already stripped (e.g. "OpenSSH-Server-Package").
//
// Every entry here is copied verbatim from the "Sample package name" field
// of Microsoft's own published FOD catalog,
// https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/features-on-demand-non-language-fod
// (fetched and read directly, 2026-09-14 -- an (i)-grade citation), with
// the trailing "~31bf3856ad364e35~amd64~~.cab"/"~31bf3856ad364e35~~.cab"
// boilerplate stripped off each. This is deliberately a small, explicit
// table rather than a generic name-mangling function: the capability name
// -> package family mapping is *not* a mechanical transform of the
// capability name (compare "OpenSSH.Server" -> "OpenSSH-Server-Package"
// against "Tools.Graphics.DirectX" -> "Microsoft-OneCore-Graphics-Tools-Package"
// -- no shared prefix, no consistent casing/word-order rule found), so any
// capability not listed here needs its package family name supplied
// directly via FindCapabilityFile's packageFamily parameter rather than
// guessed. Add more entries here as they're confirmed against that same MS
// Learn page, per this package's citation policy (see README.md).
var KnownCapabilityPackages = map[string]string{
	"OpenSSH.Server~~~~0.0.1.0": "OpenSSH-Server-Package",
	"OpenSSH.Client~~~~0.0.1.0": "OpenSSH-Client-Package",
}

// ErrCapabilityUnknown is returned by FindCapabilityFile when capabilityName
// isn't in KnownCapabilityPackages and no explicit packageFamily override
// was given.
type ErrCapabilityUnknown struct{ CapabilityName string }

func (e *ErrCapabilityUnknown) Error() string {
	return fmt.Sprintf("wufetch: capability %q has no known package family mapping; "+
		"add it to KnownCapabilityPackages (citing Microsoft's FOD catalog page) "+
		"or pass its package family name explicitly", e.CapabilityName)
}

// FindCapabilityFile locates capabilityName's downloadable file within
// files (as returned by Client.GetFiles with edition "FOD"), for the given
// architecture ("amd64", "arm64", "x86").
//
// packageFamily overrides KnownCapabilityPackages -- pass "" to use the
// built-in table, or an explicit family name (e.g. "Foo-Bar-Package") for a
// capability not yet in it.
//
// Matching is deliberately loose (case-insensitive, and tolerant of the
// "FoD"/"FOD"/"Fod" casing variance actually observed across real package
// names -- e.g. "Microsoft-Windows-DNS-Tools-FoD-Package" vs
// "Microsoft-OneCore-DirectX-Database-FOD-Package", both taken verbatim
// from Microsoft's own FOD catalog page): it requires the family name and
// the architecture to both appear, in order, as substrings of a candidate
// file name ending in ".cab", rather than requiring an exact
// family-arch.cab match, because get.php's own file-name cleanup (see
// uupdump.go's File.Name doc comment) has been observed to vary the exact
// separator/suffix shape between builds.
func FindCapabilityFile(files map[string]File, capabilityName, arch, packageFamily string) (File, error) {
	family := packageFamily
	if family == "" {
		var ok bool
		family, ok = KnownCapabilityPackages[capabilityName]
		if !ok {
			return File{}, &ErrCapabilityUnknown{CapabilityName: capabilityName}
		}
	}

	familyLower := strings.ToLower(family)
	archLower := strings.ToLower(arch)

	var matches []File
	for name, f := range files {
		if !strings.HasSuffix(strings.ToLower(name), ".cab") {
			continue
		}
		lower := strings.ToLower(name)
		famIdx := strings.Index(lower, familyLower)
		if famIdx < 0 {
			continue
		}
		if archLower != "" && !strings.Contains(lower[famIdx:], archLower) {
			continue
		}
		matches = append(matches, f)
	}

	switch len(matches) {
	case 0:
		return File{}, fmt.Errorf("wufetch: no file found for capability %q (family %q, arch %q) among %d files",
			capabilityName, family, arch, len(files))
	case 1:
		return matches[0], nil
	default:
		// Prefer the shortest matching name: real package families
		// sometimes have a longer, more specific sibling (e.g. a
		// "-ServerOnly-" or "-Common-" component package alongside the
		// FOD's own top-level package) that also happens to contain the
		// family substring; the top-level FOD package name is the
		// shortest one that matches. This is a heuristic, not a
		// documented rule -- flagged as such rather than silently
		// picking one.
		best := matches[0]
		for _, f := range matches[1:] {
			if len(f.Name) < len(best.Name) {
				best = f
			}
		}
		return best, nil
	}
}
