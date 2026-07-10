// Package inf implements parsing and serialization of the on-disk structure
// of Windows INF files: the plain-text section/key/value format used to
// describe driver installation packages (.inf, typically alongside a .cat
// catalog and one or more .sys/.dll payload files).
//
// It follows Microsoft's published grammar for INF files (Windows Hardware
// documentation, "General Syntax Rules for INF Files",
// https://learn.microsoft.com/windows-hardware/drivers/install/general-syntax-rules-for-inf-files,
// and "INF Strings Section",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-strings-section):
// section headers in brackets, "key = field, field, ..." entries and bare
// (keyless) directive lines, ';' end-of-line comments, '\' line
// continuation, double-quoted fields, and the [Strings] /
// [Strings.LanguageID] token-substitution mechanism.
//
// Scope: this package models the *structure* of an INF file - an ordered
// list of sections, each an ordered list of key/field entries - faithfully
// enough to read, edit, and re-serialize a driver INF, including the fields
// of the [Version] section a driver-installation tool cares about most
// (Signature, Class, ClassGuid, Provider, DriverVer, CatalogFile; see
// version.go) and the [Strings] token table (see strings.go). It
// deliberately does NOT implement:
//
//   - the full INF directive semantic engine: there is no interpretation of
//     what AddService, CopyFiles, AddReg, Include/Needs, or any other
//     directive *means*, beyond exposing their key and comma-separated
//     fields structurally. Chasing directives across [Manufacturer] /
//     [Models] / [DDInstall] sections to decide what a given piece of
//     hardware installs is the job of a higher-level "driver install"
//     package built on top of this one.
//   - automatic %token% substitution during Parse. [Strings] /
//     [Strings.LanguageID] entries are parsed like any other section; use
//     Lookup or Expand (strings.go) to resolve tokens on demand.
//   - INF-writer codepage/DBCS-aware text handling for non-Unicode files:
//     non-Unicode input bytes are treated as an opaque byte string and
//     round-tripped verbatim (see encoding.go); only the ASCII punctuation
//     significant to the grammar ('[', ']', '"', ';', ',', '=', '\\', '%')
//     is interpreted, so a double-byte codepage whose trail bytes alias
//     that punctuation is out of scope.
//   - INF file size/field-length limits (the documented 4096-character
//     field limit) or the "Layout File" / "Include" / "Needs" resolution
//     across multiple INFs.
//   - digital signature or .cat catalog verification; CatalogFile is
//     exposed only as a filename string (see version.go).
//
// # Round-trip contract
//
// Parse followed by (*File).AppendTo reproduces a file that is
// semantically faithful to the input but not necessarily byte-identical.
// Specifically, AppendTo canonicalizes:
//
//   - Line endings: Parse accepts bare "\n" or "\r\n"; AppendTo always
//     emits "\r\n" (the native INF convention).
//   - Line continuation: a physical line ending in an (unquoted) trailing
//     '\' is joined with the following physical line into one logical
//     entry before parsing. AppendTo never re-wraps an entry across
//     multiple physical lines, even if the original was continued. If any
//     of the continued physical lines carried its own trailing comment,
//     those fragments are concatenated (in order, space-separated) into a
//     single trailing comment on the reassembled entry; the original
//     per-line placement of such interior comments is not preserved (this
//     situation is rare in practice - the common case is a continuation
//     with no interior comment, which round-trips exactly aside from the
//     line join itself).
//   - Whitespace: leading/trailing whitespace around section names, entry
//     keys, and comma-separated fields is trimmed; AppendTo re-emits a
//     single canonical spacing ("[Name]", "Key = f1, f2, f3").
//   - Quoting: a field or section name is re-quoted with double quotes
//     (doubling any internal '"') on output only if it needs to be to
//     preserve its value (it is empty, has leading/trailing whitespace, or
//     contains '"', ';', ',', or '[' / ']' for section names) - regardless
//     of whether the original was quoted. An originally-quoted field whose
//     value doesn't require quoting round-trips as an equivalent unquoted
//     field.
//   - Comments and blank lines are preserved as their own ordered pseudo
//     entries within a section (see Entry.Blank / Entry.CommentOnly) and
//     re-emitted verbatim (text only; see whitespace/spacing note above),
//     so a file's comment banners and paragraph breaks survive a round
//     trip, just not with byte-identical spacing.
//
// Everything else - section order, duplicate section names, duplicate
// keys within a section, entry order, key text, and field values - is
// preserved exactly.
package inf

import "fmt"

// wrapErr adds context to a parse error without pulling in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("inf: %s: %w", what, err)
}

// File is the in-memory form of a parsed INF file: an ordered list of
// sections, exactly as they appeared on disk (including duplicate section
// names, which Windows treats as one logical section with merged entries -
// see Sections and MergedEntries).
type File struct {
	// Unicode records whether the source was detected as UTF-16LE with a
	// byte-order mark (see encoding.go). AppendTo uses it to choose the
	// output encoding, so a file parsed as Unicode round-trips as Unicode.
	Unicode bool
	// Sections holds every section in on-disk order. A Section with an
	// empty Name is the implicit "preamble": any comment/blank lines that
	// appear before the first "[Section]" header in the file. It is only
	// ever the first element (if present) and is written without a
	// "[...]" header line.
	Sections []Section
}

// Section returns the first section whose Name matches (case-insensitively,
// per the INF grammar) and reports whether one was found. Use Sections for
// every occurrence of a repeated section name.
func (f *File) Section(name string) (*Section, bool) {
	for i := range f.Sections {
		if equalFold(f.Sections[i].Name, name) {
			return &f.Sections[i], true
		}
	}
	return nil, false
}

// SectionsNamed returns every section whose Name matches name
// case-insensitively, in on-disk order. Windows merges repeated section
// names into one logical section; this package preserves them as distinct
// Section values so that order and provenance are not lost, and leaves
// merging to the caller (see MergedEntries for the merged view).
func (f *File) SectionsNamed(name string) []*Section {
	var out []*Section
	for i := range f.Sections {
		if equalFold(f.Sections[i].Name, name) {
			out = append(out, &f.Sections[i])
		}
	}
	return out
}

// MergedEntries returns the concatenated, in-order Entries of every section
// named name, mirroring how Windows treats repeated section names as one
// logical section.
func (f *File) MergedEntries(name string) []Entry {
	var out []Entry
	for _, s := range f.SectionsNamed(name) {
		out = append(out, s.Entries...)
	}
	return out
}

// equalFold reports whether a and b are equal under the INF grammar's
// case-insensitivity rule for section/entry names (ASCII case-fold; INF
// files are not documented to rely on non-ASCII case folding for these).
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
