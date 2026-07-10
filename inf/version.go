package inf

// VersionInfo is a convenience, typed view of the [Version] section fields
// most relevant to installing a driver package (see "INF Version Section",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-version-section).
// Values are copied verbatim from their entries: %token% references (for
// example, a Provider of "%Msft%") are not expanded - call File.Expand if
// expansion is wanted.
type VersionInfo struct {
	// Signature is "$Windows NT$" or "$Chicago$" (case-insensitive, per
	// the grammar), identifying the file as a valid INF.
	Signature string
	// Class is the device setup class name (e.g. "Net", "Display"), or ""
	// if absent.
	Class string
	// ClassGuid is the device setup class GUID, formatted as
	// "{nnnnnnnn-nnnn-nnnn-nnnn-nnnnnnnnnnnn}", or "" if absent.
	ClassGuid string
	// Provider identifies the INF's author/publisher, or "" if absent.
	Provider string
	// DriverVer is the "mm/dd/yyyy,w.x.y.z" driver date/version, or "" if
	// absent.
	DriverVer string
	// CatalogFile names the accompanying, uncompressed .cat catalog file
	// that carries the driver package's digital signature. It is a bare
	// filename, expected to sit alongside the INF on the distribution
	// media; this package does not read or verify the catalog itself.
	// Empty if the [Version] section has no (undecorated) CatalogFile
	// entry - see CatalogFileForPlatform for the platform-decorated
	// CatalogFile.ntXXX entries.
	CatalogFile string
}

// Version returns a VersionInfo built from the file's (first) [Version]
// section. Fields with no corresponding entry are left as the empty string;
// Version never fails, since a missing or incomplete [Version] section is a
// semantic validity concern for a higher-level installer, not a structural
// parse error.
func (f *File) Version() VersionInfo {
	var v VersionInfo
	sec, ok := f.Section("Version")
	if !ok {
		return v
	}
	for _, e := range sec.Entries {
		if !e.HasKey || len(e.Fields) == 0 {
			continue
		}
		val := e.Fields[0]
		switch {
		case equalFold(e.Key, "Signature"):
			v.Signature = val
		case equalFold(e.Key, "Class"):
			v.Class = val
		case equalFold(e.Key, "ClassGuid"):
			v.ClassGuid = val
		case equalFold(e.Key, "Provider"):
			v.Provider = val
		case equalFold(e.Key, "DriverVer"):
			// DriverVer's value is "mm/dd/yyyy,w.x.y.z": both fields
			// together, not just the date.
			v.DriverVer = strJoinComma(e.Fields)
		case equalFold(e.Key, "CatalogFile"):
			v.CatalogFile = val
		}
	}
	return v
}

// CatalogFileForPlatform returns the [Version] section's platform-decorated
// "CatalogFile.<platform>" entry (platform is one of "nt", "ntx86",
// "ntia64", "ntamd64", "ntarm", "ntarm64" per the documentation), falling
// back to the undecorated CatalogFile if no decorated entry matches, and
// reports whether either was found.
func (f *File) CatalogFileForPlatform(platform string) (string, bool) {
	sec, ok := f.Section("Version")
	if !ok {
		return "", false
	}
	key := "CatalogFile." + platform
	fallback, haveFallback := "", false
	for _, e := range sec.Entries {
		if !e.HasKey || len(e.Fields) == 0 {
			continue
		}
		switch {
		case equalFold(e.Key, key):
			return e.Fields[0], true
		case equalFold(e.Key, "CatalogFile"):
			fallback, haveFallback = e.Fields[0], true
		}
	}
	return fallback, haveFallback
}

func strJoinComma(fields []string) string {
	out := fields[0]
	for _, f := range fields[1:] {
		out += "," + f
	}
	return out
}
