// Package component implements a queryable model of an offline Windows
// image's servicing component store: it ties together the sibling `mum`
// package's parsed manifests (both package-level `.mum` files and, once
// decompressed by the sibling `pa30` package, component-level WinSxS
// `.manifest` files) into a single index that can be searched by name
// pattern, KB identifier, or processor architecture, and whose
// package/component dependency edges can be resolved against each other.
//
// This is the "actual Windows component module" referenced throughout this
// repo's top-level TODO.md: it does not itself parse XML or decompress
// PA30 (see `mum` and `pa30` for that), and it does not mutate an image
// (see TODO.md's removal item, not yet implemented) -- it only builds a
// read-only, in-memory view over already-parsed manifests.
//
// # Scope and known limitations
//
// Dependency-edge resolution only follows AssemblyIdentity-based
// references (Package.Parent, Update.Package/Update.Component,
// Dependency.DependentAssembly) -- it does not follow
// DeclareCapability-based capability tokens (a different identity
// namespace; see `mum`'s CapabilityIdentity), which are simply not
// modeled as dependency edges here yet.
//
// A real image contains far more `.mum`/`.manifest` files than this
// package's own tests exercise, though `pa30`'s SRC/FULLSRC support
// (confirmed 2026-07-13 against every file in a real image's
// `Windows\WinSxS\Manifests`, all 17189) means a full image's files should
// now decode essentially completely -- see TODO.md and `pa30/README.md`'s
// SRC/FULLSRC section. Build still reports per-file errors rather than
// failing outright, so a Store can still be built from the rest of a
// real image's files if some future edge case isn't covered.
package component

import (
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/mum"
	"github.com/Pandapip1/gowim/wim"
)

// Kind distinguishes a package-level servicing manifest (.mum) from a
// component-level WinSxS manifest (.manifest).
type Kind int

const (
	// KindPackage is a package-level `.mum` file (Windows\servicing\Packages).
	KindPackage Kind = iota
	// KindComponent is a component-level `.manifest` file
	// (Windows\WinSxS\Manifests), decompressed from PA30 by the sibling
	// `pa30` package before being parsed here.
	KindComponent
)

func (k Kind) String() string {
	switch k {
	case KindPackage:
		return "package"
	case KindComponent:
		return "component"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Entry is one parsed (or failed-to-parse) manifest file.
type Entry struct {
	Kind     Kind
	FileName string // the original file's name, e.g. "HyperV-...-Package...mum" or an "amd64_..._....manifest" folder name

	// Identity and Manifest are populated only if parsing succeeded (Err
	// is nil). Identity is a copy of Manifest.Identity, kept alongside it
	// for convenience.
	Identity mum.AssemblyIdentity
	Manifest *mum.Manifest

	// Dependencies lists the AssemblyIdentity values this entry's manifest
	// declares a dependency on (see package docs for exactly which
	// elements feed this). Empty if parsing failed.
	Dependencies []mum.AssemblyIdentity

	// Err is non-nil if this file could not be read as this Kind: e.g. the
	// underlying PA30 decode failed (missing SRC/FULLSRC support, an
	// unsupported rift table, etc. -- see the sibling `pa30` package) or
	// the resulting XML failed to parse. An Entry with a non-nil Err still
	// has FileName and Kind set, but Identity/Manifest/Dependencies are
	// zero-valued.
	Err error
}

// dependenciesOf extracts the AssemblyIdentity values m's modeled elements
// declare a dependency on -- see the package doc comment for exactly which
// relationships this covers.
func dependenciesOf(m *mum.Manifest) []mum.AssemblyIdentity {
	var deps []mum.AssemblyIdentity
	if m.Package != nil {
		if m.Package.Parent != nil {
			deps = append(deps, m.Package.Parent.Identity)
		}
		if m.Package.InstallerAssembly != nil {
			deps = append(deps, *m.Package.InstallerAssembly)
		}
		for _, u := range m.Package.Updates {
			if u.Package != nil {
				deps = append(deps, u.Package.Identity)
			}
			if u.Component != nil {
				deps = append(deps, u.Component.Identity)
			}
		}
	}
	for _, d := range m.Dependencies {
		for _, da := range d.DependentAssembly {
			deps = append(deps, da.Identity)
		}
	}
	return deps
}

// Store is a read-only, in-memory index over a set of parsed manifests. Use
// Build to construct one.
type Store struct {
	Entries []*Entry

	byLowerName map[string][]*Entry
}

// Build indexes entries (typically produced by BuildEntries or assembled by
// a caller) into a queryable Store.
func Build(entries []*Entry) *Store {
	s := &Store{
		Entries:     entries,
		byLowerName: make(map[string][]*Entry),
	}
	for _, e := range entries {
		if e.Err != nil {
			continue
		}
		key := strings.ToLower(e.Identity.Name)
		s.byLowerName[key] = append(s.byLowerName[key], e)
	}
	return s
}

// ByName returns every successfully-parsed entry whose identity name
// matches pattern, a single-component DOS-style glob ('*'/'?'/'[...]',
// case-insensitive -- see the sibling `wim` package's MatchName, which this
// wraps).
func (s *Store) ByName(pattern string) []*Entry {
	var out []*Entry
	for _, e := range s.Entries {
		if e.Err != nil {
			continue
		}
		if wim.MatchName(pattern, e.Identity.Name) {
			out = append(out, e)
		}
	}
	return out
}

// Lookup returns every successfully-parsed entry whose identity name
// exactly equals name (case-insensitive) -- a fast path for the common case
// of ByName without glob metacharacters.
func (s *Store) Lookup(name string) []*Entry {
	return s.byLowerName[strings.ToLower(name)]
}

// ByArchitecture returns every successfully-parsed entry whose identity
// processor architecture exactly equals arch (case-insensitive, e.g.
// "amd64", "wow64", "msil", "neutral").
func (s *Store) ByArchitecture(arch string) []*Entry {
	var out []*Entry
	for _, e := range s.Entries {
		if e.Err != nil {
			continue
		}
		if strings.EqualFold(e.Identity.ProcessorArchitecture, arch) {
			out = append(out, e)
		}
	}
	return out
}

// ByKB returns every successfully-parsed KindPackage entry whose
// Package.Identifier exactly equals kb (case-insensitive; kb is typically
// something like "KB5030219"). Component-level entries never match, since
// they have no Package.Identifier.
func (s *Store) ByKB(kb string) []*Entry {
	var out []*Entry
	for _, e := range s.Entries {
		if e.Err != nil || e.Kind != KindPackage || e.Manifest.Package == nil {
			continue
		}
		if strings.EqualFold(e.Manifest.Package.Identifier, kb) {
			out = append(out, e)
		}
	}
	return out
}

// ResolveDependencies looks up each of e's declared dependencies in s,
// returning the entries found for each (in the same order as
// e.Dependencies; a dependency not found in s is simply omitted, not an
// error -- a Store built from a partial image or a subset of files will
// often not contain every referenced identity). Matching is by identity
// name only (case-insensitive), not full identity equality (version/
// architecture/etc.), since a dependency reference and the entry it
// resolves to are not always byte-identical across real files (e.g. a
// wow64 dependency resolving to an entry recorded under a different
// architecture token) -- callers needing exact-identity matching should
// filter the returned entries themselves.
func (s *Store) ResolveDependencies(e *Entry) [][]*Entry {
	out := make([][]*Entry, len(e.Dependencies))
	for i, dep := range e.Dependencies {
		out[i] = s.Lookup(dep.Name)
	}
	return out
}
