package driver

// DirID is an INF directory identifier ("dirid"), the numeric ID used by the
// [DestinationDirs] section (and elsewhere in an INF, via a "%dirid%" token)
// to name one of a fixed set of well-known target directories. See "Using
// Dirids",
// https://learn.microsoft.com/windows-hardware/drivers/install/using-dirids.
type DirID int32

// Well-known DIRID values relevant to driver packages, from the "Using
// Dirids" table (values not listed here, e.g. shell special folders in the
// 16384-32767 range, are preserved numerically by this package but have no
// named constant).
const (
	// DirIDSourceDir (01) is the directory the INF file was installed from.
	DirIDSourceDir DirID = 1
	// DirIDWindows (10) is the Windows directory (%SystemRoot%).
	DirIDWindows DirID = 10
	// DirIDSystem (11) is the system directory (%SystemRoot%\system32).
	DirIDSystem DirID = 11
	// DirIDDrivers (12) is the drivers directory
	// (%SystemRoot%\system32\drivers), the traditional destination for
	// kernel driver .sys files prior to per-package DriverStore isolation.
	DirIDDrivers DirID = 12
	// DirIDDriverStore (13) is the driver package's own Driver Store
	// directory. Windows 8.1 and later recommend DIRID 13 for all driver
	// package files ("run from driver store"); this package does not
	// compute the actual DriverStore path (see the package doc's stated
	// non-goals) - the caller supplies it.
	DirIDDriverStore DirID = 13
	// DirIDInfDirectory (17) is the directory containing INF files.
	DirIDInfDirectory DirID = 17
	// DirIDAbsolute (-1) marks an absolute path rather than a directory
	// relative to a known root; a dirid of 65535 is documented as
	// synonymous with -1.
	DirIDAbsolute DirID = -1
)

// normalizeDirID maps the documented alias of DIRID 65535 to DirIDAbsolute
// (-1), per "Using Dirids": "a dirid with a value of 65535 is considered
// synonymous with a dirid with a value of -1, although the latter is
// preferred."
func normalizeDirID(id int64) DirID {
	if id == 65535 {
		return DirIDAbsolute
	}
	return DirID(id)
}
