package driver

import (
	_ "embed"
	"testing"
)

// real1394INF and realNTPrintINF are real .inf files copied verbatim
// (2026-07-13) from a real Windows 11 23H2 VM's
// `Windows\System32\DriverStore\FileRepository` (identity confirmed via the
// folder names they were extracted from:
// `1394.inf_amd64_f05cd2933ff9e649` and, identically for both architecture
// variants, `ntprint.inf_amd64_0234ee61ba44613e` /
// `ntprint.inf_x86_0234ee61ba44613e` -- see driverstore.go's doc comments
// for why the hash is architecture-independent here specifically).
//
//go:embed testdata/real_1394.inf
var real1394INF []byte

//go:embed testdata/real_ntprint.inf
var realNTPrintINF []byte

// TestDriverStoreHashRealSamples reproduces the exact real DriverStore
// folder-name hash for real .inf files, cross-validated against a real
// Windows 11 23H2 image (see driverStoreHash's doc comment for the full
// disassembly/validation trail -- this is 2 of the 102 real samples
// checked there, kept here as a permanent regression fixture).
func TestDriverStoreHashRealSamples(t *testing.T) {
	tests := []struct {
		name     string
		infName  string
		data     []byte
		platform string
		want     string // the real observed FileRepository folder name
	}{
		{"1394.inf", "1394.inf", real1394INF, "amd64", "1394.inf_amd64_f05cd2933ff9e649"},
		{"ntprint.inf amd64", "ntprint.inf", realNTPrintINF, "amd64", "ntprint.inf_amd64_0234ee61ba44613e"},
		// The real x86 ntprint.inf copy is byte-identical to the amd64 one
		// (confirmed during reverse engineering: one shared multi-platform
		// INF, not arch-specific variants), so re-using realNTPrintINF here
		// with a different platform token directly demonstrates that the
		// hash itself does not depend on the platform argument -- only the
		// formatted folder name's middle component does.
		{"ntprint.inf x86 (same INF bytes)", "ntprint.inf", realNTPrintINF, "x86", "ntprint.inf_x86_0234ee61ba44613e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FileRepositoryDirName(tt.infName, tt.data, tt.platform); got != tt.want {
				t.Errorf("FileRepositoryDirName(%q, ..., %q) = %q, want %q", tt.infName, tt.platform, got, tt.want)
			}
		})
	}
}
