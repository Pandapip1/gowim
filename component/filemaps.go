package component

import "errors"

// FileMapsDir is `Windows\WinSxS\FileMaps`, the one part of the component
// store this package knowingly does not maintain.
//
// # What it is, as far as this project has established
//
// A real Windows 11 image (build 10.0.26200, the same one every other
// measurement in this package cites) carries 3764 `*.cdf-ms` files there,
// one per *destination directory* rather than per component -- the file name
// encodes the destination path with `_` separators and a 16-hex suffix, e.g.
// `$$_appcompat_appraiser_33781004733ffeee.cdf-ms` for
// `%SystemRoot%\AppCompat\Appraiser` (`$$` being CBS's usual token for the
// Windows directory, as it is in `.mum` payload paths).
//
// Each file is a small binary structure -- the smallest in that image is 544
// bytes -- beginning with the ASCII magic `PcmH` followed by a `01 00 00 00`
// version word, a 16-byte identifier, and a table of counts and offsets into
// what is plainly a string pool: the pool contains assembly-identity
// attribute *names* and *values* interleaved as bare NUL-padded ASCII
// (`ProcessorArchitecture`, `NonSxS`, `31bf3856ad364e35`, `Name`,
// `VersionScope`, `Microsoft-Windows-CoreOS`, `PublicKeyToken`, `amd64`,
// `Version`, `Temp`, `10.0.26100.4202` in one dumped example). So it is
// recognizably an index from a destination directory to the set of component
// identities that own files there.
//
// # What is not established
//
// The format was not reverse-engineered: the header field meanings, the
// index tables, and how the per-file records tie a payload file name to an
// owning identity are all unread. Nor is it known whether CSI *requires*
// these maps to be current -- whether a hand-placed payload missing from
// them causes a scanner finding, a failed later servicing operation, or
// nothing at all. The research pass behind this package flagged FileMaps as
// an unexamined risk and it remains one; it is not known to break anything,
// and it is not known not to.
//
// This package therefore leaves FileMaps untouched and says so, rather than
// pretending the gap does not exist. Callers that care can test for it with
// InstallationTouchesFileMaps.
const FileMapsDir = `Windows\WinSxS\FileMaps`

// ErrFileMapsNotUpdated is never returned by Install or InstallRegistry --
// it exists so that a caller which wants to treat the FileMaps gap as fatal
// for its own use case can raise exactly one well-named error for it, and so
// that the gap has a symbol to grep for rather than only a paragraph of
// prose. See FileMapsDir for what is and is not known.
var ErrFileMapsNotUpdated = errors.New("component: Windows\\WinSxS\\FileMaps *.cdf-ms indexes are not updated by this package (format not reverse-engineered; effect of staleness unknown)")

// InstallationTouchesFileMaps reports whether inst projects any payload file
// into a destination directory outside the component store -- that is,
// whether it creates the kind of file that a `FileMaps` entry would describe
// if this package maintained them.
//
// It is a conservative "is the unresolved FileMaps gap relevant to this
// particular installation?" test, not a claim about what CBS does. An
// installation with no projected payload (a `Type=win32` SxS assembly whose
// payload lives only in `WinSxS\<keyform>\`, say) plausibly has no business
// in any per-destination-directory map at all; one that drops a DLL into
// System32 plausibly does. Both halves of that sentence are inference from
// what the maps evidently index, not from having read the format.
func InstallationTouchesFileMaps(inst *Installation) bool {
	if inst == nil {
		return false
	}
	for _, c := range inst.Components {
		for _, f := range c.Files {
			if len(f.DestDirs) > 0 {
				return true
			}
		}
	}
	return false
}
