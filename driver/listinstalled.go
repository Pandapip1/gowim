package driver

import (
	"errors"

	"github.com/Pandapip1/gowim/wim"
)

// InstalledPackage is one already-installed driver package folder found by
// ListInstalled directly under a DriverStore FileRepository directory: its
// folder name (Name) and the folder's own *wim.DirEntry (Dir), so a caller
// can inspect its contents (e.g. find the .inf file within it) without a
// second directory walk of fileRepository to re-find the same child by
// name - this is why ListInstalled returns this richer pair rather than a
// plain []string: it costs nothing extra (fileRepository.Children is
// already being walked) but saves every caller a second findChildFold-style
// lookup before it can do anything useful with a package's contents (e.g.
// Uninstall's driverStoreDirName parameter is exactly one of these Names,
// resolved back against the same parent).
type InstalledPackage struct {
	// Name is the DriverStore package folder's name verbatim, e.g.
	// "contoso.inf_amd64_deadbeef" - shaped like
	// "<infname>.inf_<arch>_<hash>", but ListInstalled makes no attempt to
	// parse that shape (see the package doc's stated non-goal regarding the
	// DriverStore's own path-hashing scheme); it is returned exactly as
	// found.
	Name string
	// Dir is the folder's own directory-entry node (fileRepository's child
	// named Name).
	Dir *wim.DirEntry
}

// ListInstalled enumerates the driver packages already present in an
// image's DriverStore, i.e. the immediate subdirectories of fileRepository -
// the already-navigated *wim.DirEntry for the image's
// Windows\System32\DriverStore\FileRepository directory. This package still
// does not compute or need that path itself (see the package doc's stated
// non-goal regarding the DriverStore's own path-hashing scheme, and
// Install's destDirs parameter, which is the same deliberate omission on the
// install side) - the caller resolves fileRepository the same way it
// resolves any other path into an image's tree.
//
// This mirrors nano11builder.ps1's
// `Get-ChildItem -Path $driverRepo -Directory`: only direct children
// flagged as directories are returned (in fileRepository.Children order); a
// non-directory child (which should never legitimately occur directly
// under FileRepository, but this package does not assume a well-formed
// tree) is silently skipped rather than treated as an error.
func ListInstalled(fileRepository *wim.DirEntry) ([]InstalledPackage, error) {
	if fileRepository == nil {
		return nil, wrapErr("list installed", errors.New("nil FileRepository directory"))
	}

	var out []InstalledPackage
	for _, c := range fileRepository.Children {
		if !c.IsDirectory() {
			continue
		}
		out = append(out, InstalledPackage{Name: c.NameUTF8(), Dir: c})
	}
	return out, nil
}
