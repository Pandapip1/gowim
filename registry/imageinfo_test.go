package registry

import (
	"testing"

	"github.com/Pandapip1/gowim/regf"
)

// buildTestSystemHive builds a minimal but structurally realistic SYSTEM
// hive tree: Select\Default pointing at ControlSet001, whose
// Control\Nls\Language\InstallLanguage and Control\Session Manager\
// Environment\PROCESSOR_ARCHITECTURE values are populated -- mirroring the
// real, pristine factory install.esd SYSTEM hive's shape confirmed
// 2026-07-14 (see imageinfo.go's doc comments).
func buildTestSystemHive(t *testing.T, lcid, arch string) *regf.Key {
	t.Helper()
	root := &regf.Key{}
	root.FindOrCreateSubkey("Select").SetValue("Default", regf.RegDWORD, regf.EncodeDWORD(1))

	ccs := root.FindOrCreatePath(`ControlSet001`)
	ccs.FindOrCreatePath(`Control\Nls\Language`).SetValue("InstallLanguage", regf.RegSZ, regf.EncodeSZ(lcid))
	ccs.FindOrCreatePath(`Control\Session Manager\Environment`).SetValue("PROCESSOR_ARCHITECTURE", regf.RegSZ, regf.EncodeSZ(arch))
	return root
}

func TestDefaultUILanguage(t *testing.T) {
	root := buildTestSystemHive(t, "0409", "AMD64")

	got, err := DefaultUILanguage(root)
	if err != nil {
		t.Fatalf("DefaultUILanguage: %v", err)
	}
	if got != "0409" {
		t.Errorf("DefaultUILanguage = %q, want %q", got, "0409")
	}
}

func TestProcessorArchitecture(t *testing.T) {
	root := buildTestSystemHive(t, "0409", "AMD64")

	got, err := ProcessorArchitecture(root)
	if err != nil {
		t.Fatalf("ProcessorArchitecture: %v", err)
	}
	if got != "AMD64" {
		t.Errorf("ProcessorArchitecture = %q, want %q", got, "AMD64")
	}
}

func TestDefaultUILanguageMissingKey(t *testing.T) {
	root := &regf.Key{}
	root.FindOrCreateSubkey("Select").SetValue("Default", regf.RegDWORD, regf.EncodeDWORD(1))
	root.FindOrCreatePath("ControlSet001") // no Control\Nls\Language

	if _, err := DefaultUILanguage(root); err == nil {
		t.Error("expected an error when Control\\Nls\\Language is missing")
	}
}

func TestProcessorArchitectureMissingValue(t *testing.T) {
	root := &regf.Key{}
	root.FindOrCreateSubkey("Select").SetValue("Default", regf.RegDWORD, regf.EncodeDWORD(1))
	root.FindOrCreatePath(`ControlSet001\Control\Session Manager\Environment`) // no PROCESSOR_ARCHITECTURE value

	if _, err := ProcessorArchitecture(root); err == nil {
		t.Error("expected an error when PROCESSOR_ARCHITECTURE is missing")
	}
}
