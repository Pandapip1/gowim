package driver

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Pandapip1/gowim/inf"
)

// spsvcinstAssocService is the SPSVCINST_ASSOCSERVICE bit (0x00000002) of an
// AddService directive's flags field: "Assign the named service as the PnP
// function driver (or legacy driver) for the device being installed by this
// INF file." See "INF AddService Directive",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-addservice-directive
// (the "flags" entry).
const spsvcinstAssocService = 0x00000002

// ServiceInstall is one service-install-section resolved from an AddService
// directive within an install section's (platform-decorated) .Services
// section. See "INF DDInstall.Services Section",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-ddinstall-services-section,
// and "INF AddService Directive",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-addservice-directive.
type ServiceInstall struct {
	// Name is the AddService directive's ServiceName field.
	Name string
	// AssocService reports whether the AddService directive's flags field
	// has the SPSVCINST_ASSOCSERVICE bit (0x00000002) set: "assign the
	// named service as the PnP function driver (or legacy driver) for the
	// device being installed by this INF file." Every device driver INF is
	// documented to have exactly one such service.
	AssocService bool
	// ServiceType is the service-install-section's ServiceType value (e.g.
	// 1 = SERVICE_KERNEL_DRIVER, 2 = SERVICE_FILE_SYSTEM_DRIVER, 0x10 =
	// SERVICE_WIN32_OWN_PROCESS, 0x20 = SERVICE_WIN32_SHARE_PROCESS).
	ServiceType uint32
	// StartType is the service-install-section's StartType value (0 =
	// SERVICE_BOOT_START, 1 = SERVICE_SYSTEM_START, 2 = SERVICE_AUTO_START,
	// 3 = SERVICE_DEMAND_START, 4 = SERVICE_DISABLED).
	StartType uint32
	// ErrorControl is the service-install-section's ErrorControl value (0 =
	// SERVICE_ERROR_IGNORE, 1 = SERVICE_ERROR_NORMAL, 2 =
	// SERVICE_ERROR_SEVERE, 3 = SERVICE_ERROR_CRITICAL).
	ErrorControl uint32
	// BinaryDirID is the DIRID parsed from the service-install-section's
	// ServiceBinary="%dirid%\path" entry.
	BinaryDirID DirID
	// BinaryPath is the slash-separated path components after the %dirid%
	// token in ServiceBinary, relative to BinaryDirID.
	BinaryPath string
	// LoadOrderGroup is the service-install-section's optional
	// LoadOrderGroup value, or "" if absent.
	LoadOrderGroup string
	// DependOnService lists the plain service names from the
	// service-install-section's optional Dependencies entry (the
	// depend-on-item-name entries that do not start with '+').
	DependOnService []string
	// DependOnGroup lists the load-order-group names from Dependencies (the
	// depend-on-item-name entries prefixed with '+', with the '+' stripped).
	DependOnGroup []string
	// InstallSection is the undecorated install-section-name this service
	// was reached through (the Models entry's install-section-name field),
	// for diagnostics.
	InstallSection string
}

// parseUintField parses a ServiceType/StartType/ErrorControl/flags field,
// which the documentation allows to be expressed "either in decimal or...
// in hexadecimal notation" (a "0x"-prefixed hex literal). An empty field is
// treated as 0 (the documented default for an AddService flags field left
// blank, e.g. "AddService=ExampleUpperFilter,,filter_ServiceInstallSection").
func parseUintField(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// parseDiridToken parses a ServiceBinary-style "%dirid%\path" token (see
// "INF AddService Directive"'s ServiceBinary entry: "Specifies the path of
// the binary for the service, expressed as %dirid%\filename"), returning the
// parsed DirID and the remaining path (with any single leading separator
// stripped), normalized to '/'.
func parseDiridToken(s string) (DirID, string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "%") {
		return 0, "", fmt.Errorf("ServiceBinary %q does not start with a %%dirid%% token", s)
	}
	end := strings.IndexByte(s[1:], '%')
	if end < 0 {
		return 0, "", fmt.Errorf("ServiceBinary %q has unterminated %%dirid%% token", s)
	}
	end++ // index within s
	numStr := s[1:end]
	id, err := strconv.ParseInt(numStr, 10, 32)
	if err != nil {
		return 0, "", fmt.Errorf("ServiceBinary %q: bad dirid %q: %w", s, numStr, err)
	}
	rest := toSlash(s[end+1:])
	rest = strings.TrimPrefix(rest, "/")
	return normalizeDirID(id), rest, nil
}

// splitDependencies parses a Dependencies entry's already comma-split fields
// (inf.Entry.Fields) into group vs. service dependencies, per "INF
// AddService Directive": "A depend-on-item-name can specify a load order
// group on which this device/driver depends... Precede the group name with
// a plus sign (+)."
func splitDependencies(fields []string) (services []string, groups []string) {
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "+") {
			groups = append(groups, strings.TrimPrefix(f, "+"))
		} else {
			services = append(services, f)
		}
	}
	return services, groups
}

// resolveServiceInstallSection reads a named service-install-section (the
// section an AddService directive's service-install-section field names)
// into a ServiceInstall.
func resolveServiceInstallSection(f *inf.File, sectionName, serviceName string, assocService bool, installSection string) (ServiceInstall, error) {
	si := ServiceInstall{
		Name:           serviceName,
		AssocService:   assocService,
		InstallSection: installSection,
	}

	var haveType, haveStart, haveError, haveBinary bool
	for _, e := range f.MergedEntries(sectionName) {
		if !e.HasKey || len(e.Fields) == 0 {
			continue
		}
		switch {
		case equalFold(e.Key, "ServiceType"):
			v, err := parseUintField(e.Fields[0])
			if err != nil {
				return ServiceInstall{}, wrapErr("service "+serviceName+" ServiceType", err)
			}
			si.ServiceType, haveType = v, true
		case equalFold(e.Key, "StartType"):
			v, err := parseUintField(e.Fields[0])
			if err != nil {
				return ServiceInstall{}, wrapErr("service "+serviceName+" StartType", err)
			}
			si.StartType, haveStart = v, true
		case equalFold(e.Key, "ErrorControl"):
			v, err := parseUintField(e.Fields[0])
			if err != nil {
				return ServiceInstall{}, wrapErr("service "+serviceName+" ErrorControl", err)
			}
			si.ErrorControl, haveError = v, true
		case equalFold(e.Key, "ServiceBinary"):
			dirID, path, err := parseDiridToken(e.Fields[0])
			if err != nil {
				return ServiceInstall{}, wrapErr("service "+serviceName+" ServiceBinary", err)
			}
			si.BinaryDirID, si.BinaryPath, haveBinary = dirID, path, true
		case equalFold(e.Key, "LoadOrderGroup"):
			si.LoadOrderGroup = e.Fields[0]
		case equalFold(e.Key, "Dependencies"):
			services, groups := splitDependencies(e.Fields)
			si.DependOnService = append(si.DependOnService, services...)
			si.DependOnGroup = append(si.DependOnGroup, groups...)
		}
	}

	if !haveType || !haveStart || !haveError || !haveBinary {
		return ServiceInstall{}, wrapErr("service install section "+sectionName,
			errors.New("missing one or more required entries (ServiceType, StartType, ErrorControl, ServiceBinary)"))
	}

	return si, nil
}

// Services enumerates the service-install-sections named by AddService
// directives reachable from pkg's [Manufacturer] -> Models -> install
// section chain, via each install section's (platform-decorated)
// ".Services" section - see "INF DDInstall.Services Section":
//
//	[install-section-name.Services] |
//	[install-section-name.nt.Services] |
//	[install-section-name.nt<platform>.Services]
//	AddService=ServiceName,[flags],service-install-section[,...]
//
// (the platform decoration sits between install-section-name and
// ".Services", not after it). platform selects candidate section variants
// exactly like LoadPackage's platform parameter (see candidateSectionNames);
// AddReg/DelReg/EventLog sub-directives of the service-install-section are
// not interpreted (see the package doc's non-goals). Services deduplicates
// by ServiceName (case-insensitive), keeping the first occurrence, mirroring
// enumeratePayloadFiles's dedup-by-DestName behavior.
func (p *Package) Services(platform string) ([]ServiceInstall, error) {
	var out []ServiceInstall
	seen := make(map[string]bool)

	for _, ms := range enumerateModelSections(p.INF, platform) {
		var svcSectionNames []string
		for _, n := range candidateSectionNames(ms.InstallSection, platform) {
			svcSectionNames = append(svcSectionNames, n+".Services")
		}

		for _, e := range mergedEntriesUnion(p.INF, svcSectionNames) {
			if !e.HasKey || !equalFold(e.Key, "AddService") || len(e.Fields) < 3 {
				continue
			}
			name := strings.TrimSpace(e.Fields[0])
			if name == "" {
				continue // "AddService = ,2" (NULL driver): nothing to resolve
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}

			flags, err := parseUintField(e.Fields[1])
			if err != nil {
				return nil, wrapErr("AddService "+name+" flags", err)
			}
			sectionName := strings.TrimSpace(e.Fields[2])
			if sectionName == "" {
				continue
			}

			si, err := resolveServiceInstallSection(p.INF, sectionName, name,
				flags&spsvcinstAssocService != 0, ms.InstallSection)
			if err != nil {
				return nil, err
			}
			seen[key] = true
			out = append(out, si)
		}
	}

	return out, nil
}
