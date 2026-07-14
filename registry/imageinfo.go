package registry

import (
	"fmt"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/service"
)

// DefaultUILanguage reads an offline image's default UI language, as the
// 4-hex-digit LCID string Windows itself stores (e.g. "0409" for
// en-US) -- not "0409"'s human-readable name, since resolving an LCID to a
// language tag/name is a locale-data concern well outside this package's
// scope.
//
// Per this repo's own "research first" convention (see TODO.md's "Image
// metadata extras" entry, which originally assumed this value lived in the
// SOFTWARE hive), this was verified, not assumed: systemRoot is the SYSTEM
// hive's root key (e.g. HiveSet.Hives[HiveSystem].Hive.Root), NOT the
// SOFTWARE hive, confirmed 2026-07-14 by directly extracting
// CurrentControlSet\Control\Nls\Language from a real, pristine (never
// booted) Windows 11 23H2 factory install.esd's own SYSTEM hive (via
// wimlib-imagex extract + hivexregedit --export) and finding
// "InstallLanguage" already populated with "0409" pre-boot. The registry
// path and its meaning are also documented via Microsoft's own support
// content describing HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\
// Nls\Language\Default and \InstallLanguage as LCID-valued (see the
// Microsoft Q&A pages on changing a Windows installation's default UI
// language, e.g.
// https://learn.microsoft.com/en-us/answers/questions/4281742/).
//
// DISM's own /Get-Intl and /Get-WimInfo report several related-but-distinct
// language settings (system locale, UI language, input locale, etc) that
// this function does not attempt to reproduce; "InstallLanguage" (as
// opposed to "Default", which can differ after a UI language change on a
// booted system - not relevant to an offline factory image, but recorded
// here for completeness) is the one this function reads, since it best
// matches Get-WimInfo's notion of the image's built-in default.
func DefaultUILanguage(systemRoot *regf.Key) (string, error) {
	ccs, err := service.CurrentControlSet(systemRoot)
	if err != nil {
		return "", fmt.Errorf("registry: default UI language: %w", err)
	}

	nlsLanguage := ccs.OpenPath(`Control\Nls\Language`)
	if nlsLanguage == nil {
		return "", fmt.Errorf(`registry: default UI language: no "Control\Nls\Language" key`)
	}
	v := nlsLanguage.Value("InstallLanguage")
	if v == nil {
		return "", fmt.Errorf(`registry: default UI language: no "InstallLanguage" value`)
	}
	return v.SZ(), nil
}

// ProcessorArchitecture reads an offline image's processor architecture, as
// the string Windows itself stores in the PROCESSOR_ARCHITECTURE
// environment variable (e.g. "AMD64", "ARM64", "x86").
//
// Per this repo's own "research first" convention (see TODO.md's "Image
// metadata extras" entry, which originally assumed this value lived in the
// SOFTWARE hive), this was verified, not assumed: systemRoot is the SYSTEM
// hive's root key, NOT the SOFTWARE hive, confirmed 2026-07-14 the same way
// as DefaultUILanguage - CurrentControlSet\Control\Session Manager\
// Environment\PROCESSOR_ARCHITECTURE was already populated with "amd64" in
// a real, pristine (never booted) factory install.esd's SYSTEM hive. The
// registry path is also officially documented: see Microsoft's "Determine
// the type of processor" support article
// (https://learn.microsoft.com/en-us/troubleshoot/windows-server/setup-upgrade-and-drivers/determine-the-type-of-processor),
// which notes PROCESSOR_ARCHITECTURE (unlike the same key's
// PROCESSOR_IDENTIFIER) reflects only the instruction-set architecture
// (e.g. always "AMD64" on any x64 chip, Intel or AMD alike) - exactly the
// distinction this function's callers care about (an image's architecture),
// not the exact CPU model.
func ProcessorArchitecture(systemRoot *regf.Key) (string, error) {
	ccs, err := service.CurrentControlSet(systemRoot)
	if err != nil {
		return "", fmt.Errorf("registry: processor architecture: %w", err)
	}

	environment := ccs.OpenPath(`Control\Session Manager\Environment`)
	if environment == nil {
		return "", fmt.Errorf(`registry: processor architecture: no "Control\Session Manager\Environment" key`)
	}
	v := environment.Value("PROCESSOR_ARCHITECTURE")
	if v == nil {
		return "", fmt.Errorf(`registry: processor architecture: no "PROCESSOR_ARCHITECTURE" value`)
	}
	return v.SZ(), nil
}
