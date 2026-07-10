package driver

import (
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/gavin-john/gowim/cat"
	"github.com/gavin-john/gowim/inf"
)

// PayloadFile is one file a driver package's [Manufacturer]/Models/CopyFiles
// directive chain says should be installed.
type PayloadFile struct {
	// DestName is the destination file name (the name the file should have
	// once installed), from the CopyFiles file-list-section's
	// destination-file-name field (or the bare name in an "@filename" direct
	// copy).
	DestName string
	// SourceName is the file's name on the source media; equal to DestName
	// unless the file-list-section entry gave an explicit, different
	// source-file-name.
	SourceName string
	// SourcePath is the slash-separated path, within the Package's fs.FS,
	// from which SourceName's bytes should be read: the INF's own directory,
	// plus any [SourceDisksFiles] subdir, plus SourceName. This package does
	// not resolve the multi-disk [SourceDisksNames] mechanism (disk root,
	// tag files) - see the package doc.
	SourcePath string
	// DirID is the destination directory ID this file resolves to via
	// [DestinationDirs] (either a specific file-list-section entry, or the
	// DefaultDestDir fallback).
	DirID DirID
	// DirSubdir is the optional subdirectory (under DirID) from the
	// resolved [DestinationDirs] entry.
	DirSubdir string
	// InstallSection is the undecorated install-section-name (the Models
	// entry's install-section-name field) this file was reached through, for
	// diagnostics.
	InstallSection string
}

// Package is a loaded driver package: a parsed INF, its resolved catalog (if
// any), and the enumerated set of payload files its CopyFiles directives
// name.
type Package struct {
	// FSys is the filesystem the package's files were loaded from.
	FSys fs.FS
	// Dir is the slash-separated directory within FSys containing the INF
	// file ("." if the INF is at the root of FSys). Payload and catalog
	// files are resolved relative to it, per the documented convention that
	// they sit alongside the INF.
	Dir string
	// INFName is the base file name of the INF (no directory component).
	INFName string
	// INF is the parsed INF file.
	INF *inf.File
	// Platform is the architecture token passed to LoadPackage (e.g.
	// "amd64", "x86", "arm64", or "" for none), used to select
	// platform-decorated section variants.
	Platform string
	// CatalogName is the base file name of the resolved catalog (from the
	// [Version] section's CatalogFile/CatalogFile.<platform> entry), or ""
	// if the INF declares none.
	CatalogName string
	// Catalog is the parsed catalog contents, or nil if CatalogName is "".
	Catalog *cat.SignedData
	// Files lists the enumerated payload files, in the order first
	// encountered, deduplicated by DestName.
	Files []PayloadFile
}

// LoadPackage parses the INF file at infPath within fsys, resolves and loads
// its declared catalog file (if any), and enumerates its payload files by
// walking [Manufacturer] -> Models -> install-section.CopyFiles ->
// [SourceDisksFiles] / [DestinationDirs], as described in the package doc.
//
// platform is an architecture token such as "amd64", "x86", "arm64", "arm",
// or "ia64" (matching the [SourceDisksFiles.<arch>] suffix convention), or ""
// to consider only undecorated/".NT"-decorated sections. It is used to widen
// section-name lookups (".NT<platform>", ".NT", and the bare name are all
// unioned - see the package doc for why this does not attempt full
// TargetOSVersion selection).
func LoadPackage(fsys fs.FS, infPath string, platform string) (*Package, error) {
	infPath = toSlash(infPath)
	dir, name := path.Split(infPath)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}

	data, err := fs.ReadFile(fsys, infPath)
	if err != nil {
		return nil, wrapErr("read inf", err)
	}
	f, err := inf.ParseFile(data)
	if err != nil {
		return nil, wrapErr("parse inf", err)
	}

	p := &Package{
		FSys:     fsys,
		Dir:      dir,
		INFName:  name,
		INF:      f,
		Platform: platform,
	}

	if catName, ok := f.CatalogFileForPlatform("nt" + platform); ok && catName != "" {
		p.CatalogName = catName
		catPath := path.Join(dir, toSlash(catName))
		catBytes, err := fs.ReadFile(fsys, catPath)
		if err != nil {
			return nil, wrapErr("read catalog file "+catName, err)
		}
		ci, err := cat.ParseContentInfo(catBytes)
		if err != nil {
			return nil, wrapErr("parse catalog file "+catName, err)
		}
		if ci.SignedData == nil {
			return nil, wrapErr("catalog file "+catName, errNotSignedData)
		}
		p.Catalog = ci.SignedData
	}

	files, err := enumeratePayloadFiles(f, dir, platform)
	if err != nil {
		return nil, err
	}
	p.Files = files
	return p, nil
}

var errNotSignedData = errors.New("outer PKCS #7 ContentInfo is not a SignedData value")

// toSlash converts a path that may use either separator convention (INF
// files, being a Windows format, often use '\') to the forward-slash form
// fs.FS requires.
func toSlash(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}

// candidateSectionNames returns, in priority order, the platform-decorated
// and undecorated variants of a section name that this package considers for
// Manufacturer/Models/DDInstall section resolution: "<base>.NT<platform>",
// "<base>.NT", and "<base>", deduplicated. See the package doc for why these
// are unioned rather than exclusively selected.
func candidateSectionNames(base, platform string) []string {
	seen := make(map[string]bool, 3)
	var out []string
	add := func(s string) {
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	if platform != "" {
		add(base + ".NT" + platform)
	}
	add(base + ".NT")
	add(base)
	return out
}

// mergedEntriesUnion returns the concatenation of f.MergedEntries(name) for
// every candidate name, in candidate-then-entry order.
func mergedEntriesUnion(f *inf.File, names []string) []inf.Entry {
	var out []inf.Entry
	for _, n := range names {
		out = append(out, f.MergedEntries(n)...)
	}
	return out
}

// enumeratePayloadFiles walks [Manufacturer] -> Models -> install-section ->
// CopyFiles -> [SourceDisksFiles]/[DestinationDirs] to build the package's
// payload file list.
func enumeratePayloadFiles(f *inf.File, dir, platform string) ([]PayloadFile, error) {
	destDirs := f.MergedEntries("DestinationDirs")

	var files []PayloadFile
	seen := make(map[string]bool)
	addFile := func(pf PayloadFile) {
		key := strings.ToLower(pf.DestName)
		if seen[key] {
			return
		}
		seen[key] = true
		files = append(files, pf)
	}

	for _, mfgEntry := range f.MergedEntries("Manufacturer") {
		if len(mfgEntry.Fields) == 0 {
			continue
		}
		modelsSection := mfgEntry.Fields[0]

		modelEntries := mergedEntriesUnion(f, candidateSectionNames(modelsSection, platform))
		for _, modelEntry := range modelEntries {
			if !modelEntry.HasKey || len(modelEntry.Fields) == 0 {
				continue
			}
			installSection := modelEntry.Fields[0]

			ddEntries := mergedEntriesUnion(f, candidateSectionNames(installSection, platform))
			for _, dd := range ddEntries {
				if !dd.HasKey || !equalFold(dd.Key, "CopyFiles") {
					continue
				}
				pfs, err := resolveCopyFiles(f, dir, platform, installSection, dd.Fields, destDirs)
				if err != nil {
					return nil, err
				}
				for _, pf := range pfs {
					addFile(pf)
				}
			}
		}
	}

	return files, nil
}

// resolveCopyFiles resolves one CopyFiles entry's fields (either a single
// "@filename" direct-copy token, or a list of file-list-section names) into
// PayloadFiles.
func resolveCopyFiles(f *inf.File, dir, platform, installSection string, fields []string, destDirs []inf.Entry) ([]PayloadFile, error) {
	var out []PayloadFile

	if len(fields) == 1 && strings.HasPrefix(fields[0], "@") {
		name := fields[0][1:]
		pf, err := buildPayloadFile(f, dir, platform, installSection, name, name, "", destDirs)
		if err != nil {
			return nil, err
		}
		out = append(out, pf)
		return out, nil
	}

	for _, listSection := range fields {
		listSection = strings.TrimSpace(listSection)
		if listSection == "" {
			continue
		}
		for _, entry := range f.MergedEntries(listSection) {
			if entry.HasKey || len(entry.Fields) == 0 {
				continue // not a "destfile[,srcfile]" directive line
			}
			destName := entry.Fields[0]
			if destName == "" {
				continue
			}
			srcName := destName
			if len(entry.Fields) > 1 && entry.Fields[1] != "" {
				srcName = entry.Fields[1]
			}
			pf, err := buildPayloadFile(f, dir, platform, installSection, destName, srcName, listSection, destDirs)
			if err != nil {
				return nil, err
			}
			out = append(out, pf)
		}
	}
	return out, nil
}

// buildPayloadFile resolves a single (destName, srcName) pair's source
// location (via [SourceDisksFiles]) and destination directory (via
// [DestinationDirs], keyed by listSection, or DefaultDestDir if listSection
// is "" or has no entry of its own).
func buildPayloadFile(f *inf.File, dir, platform, installSection, destName, srcName, listSection string, destDirs []inf.Entry) (PayloadFile, error) {
	subdir := resolveSourceDisksFilesSubdir(f, platform, srcName)

	dirID, dirSubdir, err := resolveDestinationDir(destDirs, listSection)
	if err != nil {
		return PayloadFile{}, err
	}

	sourcePath := path.Join(dir, toSlash(subdir), srcName)

	return PayloadFile{
		DestName:       destName,
		SourceName:     srcName,
		SourcePath:     sourcePath,
		DirID:          dirID,
		DirSubdir:      dirSubdir,
		InstallSection: installSection,
	}, nil
}

// resolveSourceDisksFilesSubdir looks up srcName in the platform-specific
// [SourceDisksFiles.<arch>] section, falling back to the undecorated
// [SourceDisksFiles], and returns its subdir field (empty if the file has no
// entry at all, or the entry has no subdir - both are treated as "same
// directory as the INF", a deliberate simplification; see the package doc).
func resolveSourceDisksFilesSubdir(f *inf.File, platform, srcName string) string {
	var names []string
	if platform != "" {
		names = append(names, "SourceDisksFiles."+platform)
	}
	names = append(names, "SourceDisksFiles")

	for _, n := range names {
		for _, e := range f.MergedEntries(n) {
			if !e.HasKey || !equalFold(e.Key, srcName) {
				continue
			}
			if len(e.Fields) > 1 {
				return e.Fields[1]
			}
			return ""
		}
	}
	return ""
}

// resolveDestinationDir looks up listSection in the [DestinationDirs]
// entries, falling back to DefaultDestDir, and parses its dirid[,subdir]
// fields.
func resolveDestinationDir(destDirs []inf.Entry, listSection string) (DirID, string, error) {
	var chosen *inf.Entry
	for i := range destDirs {
		e := &destDirs[i]
		if e.HasKey && listSection != "" && equalFold(e.Key, listSection) {
			chosen = e
			break
		}
	}
	if chosen == nil {
		for i := range destDirs {
			e := &destDirs[i]
			if e.HasKey && equalFold(e.Key, "DefaultDestDir") {
				chosen = e
				break
			}
		}
	}
	if chosen == nil || len(chosen.Fields) == 0 {
		return 0, "", wrapErr("destination dirs", errors.New(
			"no [DestinationDirs] entry for "+listSection+" and no DefaultDestDir"))
	}

	id, err := strconv.ParseInt(strings.TrimSpace(chosen.Fields[0]), 10, 32)
	if err != nil {
		return 0, "", wrapErr("destination dirs", err)
	}
	subdir := ""
	if len(chosen.Fields) > 1 {
		subdir = chosen.Fields[1]
	}
	return normalizeDirID(id), subdir, nil
}

// modelSection is one [Manufacturer] -> Models entry: the install-section-name
// it points to, plus the hardware/compatible IDs listed after it
// ("device-description=install-section-name,hw-id[,compatible-id,...]"; see
// "INF Models Section",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-models-section).
type modelSection struct {
	// InstallSection is the undecorated install-section-name field.
	InstallSection string
	// HardwareIDs holds the hw-id and any compatible-id fields, in order.
	HardwareIDs []string
}

// enumerateModelSections walks [Manufacturer] -> Models exactly like
// enumeratePayloadFiles does, but returns each entry's install-section-name
// paired with its hardware/compatible ID list rather than resolving
// CopyFiles. This lets service.go's Services and
// criticaldevicedatabase.go's CriticalDeviceDatabaseEntries reuse the same
// Manufacturer/Models traversal enumeratePayloadFiles already performs,
// without disturbing its existing CopyFiles-resolution behavior (or the
// tests exercising it).
func enumerateModelSections(f *inf.File, platform string) []modelSection {
	var out []modelSection
	for _, mfgEntry := range f.MergedEntries("Manufacturer") {
		if len(mfgEntry.Fields) == 0 {
			continue
		}
		modelsSection := mfgEntry.Fields[0]

		modelEntries := mergedEntriesUnion(f, candidateSectionNames(modelsSection, platform))
		for _, modelEntry := range modelEntries {
			if !modelEntry.HasKey || len(modelEntry.Fields) == 0 {
				continue
			}
			out = append(out, modelSection{
				InstallSection: modelEntry.Fields[0],
				HardwareIDs:    append([]string(nil), modelEntry.Fields[1:]...),
			})
		}
	}
	return out
}

// equalFold is the same ASCII case-fold comparison the inf package uses for
// section/key names; duplicated here since it is unexported in inf.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
