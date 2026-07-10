// Package service models the Windows Service Control Manager's (SCM)
// registry schema - the HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services
// key tree that CreateService documents itself as writing to - and merges an
// already-resolved Service registration into a caller-supplied *regf.Key
// tree, using the sibling regf package
// (github.com/Pandapip1/gowim/regf) purely as a plain Key/Value struct tree,
// exactly as the sibling driver package's install.go builds *wim.DirEntry
// trees by hand with no special "tree API" from the wim package.
//
// This package depends on nothing but the Go standard library and regf, so
// that "add a service registration to a registry hive" is usable entirely on
// its own - without pulling in the much heavier dependency set (inf, cat, pe,
// wim) the sibling driver package needs purely to parse driver packages, not
// to define what a Windows service registration looks like in the registry.
//
// The values Install writes (Type/Start/ErrorControl/ImagePath/Group/
// DependOnGroup/DependOnService) and the Type/Start/ErrorControl constants
// below are cross-checked against the official Win32 CreateService function
// documentation on Microsoft Learn - see Install's and the constants' doc
// comments for the precise citations - as of 2026-07-10.
//
// It deliberately does NOT implement:
//
//   - Any live SCM API semantics: starting, stopping, querying, or otherwise
//     controlling a running service (what CreateService/OpenService/
//     StartService/ControlService/QueryServiceStatus etc. do against a live
//     service control manager). This package only reads/writes the on-disk
//     registry shape those APIs are documented to persist; it never talks to
//     a running SCM.
//   - The SYSTEM hive's DriverDatabase key tree, the Enum device-instance
//     tree, or INFCACHE.1 - none of these are a "service" concept at all;
//     see the sibling driver package's README
//     (https://github.com/Pandapip1/gowim/tree/main/driver#readme) for why
//     driver does not implement them either.
//   - CriticalDeviceDatabase: registering a hardware ID against a service
//     before PnP ever sees the device is a PnP/driver-install concept, not a
//     generic "service" concept, and stays entirely in the sibling driver
//     package (see driver/criticaldevicedatabase.go).
//   - Registry-hive file I/O (deciding which hive file to open, backing it
//     up, replacing it): Install only produces/merges regf.Key/regf.Value
//     structures given an already-loaded (or freshly-built) *regf.Key tree;
//     the caller handles file I/O, exactly as regf itself does not read/
//     write files directly.
//   - Resolving *where a service's binary comes from*: an INF's
//     "%dirid%\path" token (see the driver package's DirID/ServiceInstall),
//     a plain absolute path, or anything else. That resolution is entirely
//     the caller's job - this package only writes an already-resolved
//     Service.ImagePath string into the registry.
package service

import "fmt"

// Service is a fully-resolved Windows service registration: every field a
// caller would pass to CreateService's dwServiceType/dwStartType/
// dwErrorControl/lpBinaryPathName/lpLoadOrderGroup/lpDependencies
// parameters, and hence every value Install writes under a
// Services\<Name> registry key (see Install's doc comment). Unlike an INF's
// AddService-derived service-install-section (the sibling driver package's
// ServiceInstall type), Service.ImagePath is already a plain, fully-resolved
// path string - this package has no "dirid" concept; that INF-specific
// resolution step is entirely the caller's responsibility (see the package
// doc's non-goals).
type Service struct {
	// Name is the service's name - the Services subkey name Install
	// creates/updates.
	Name string
	// Type is the service type, one of the Type* constants (or another
	// SERVICE_* value CreateService documents that this package has no named
	// constant for).
	Type uint32
	// Start is the service start type, one of the Start* constants.
	Start uint32
	// ErrorControl is the severity of/action taken on a startup failure, one
	// of the Error* constants.
	ErrorControl uint32
	// ImagePath is the fully-resolved path to the service's binary (what
	// CreateService's lpBinaryPathName parameter, and hence the ImagePath
	// registry value, hold) - e.g.
	// `\Windows\System32\DriverStore\FileRepository\contoso.inf_amd64_xxxxxxxx\driver.sys`
	// for a driver service, or a plain absolute path plus arguments for a
	// Win32 service. Must be non-empty.
	ImagePath string
	// Group is the load ordering group this service belongs to (
	// CreateService's lpLoadOrderGroup parameter), or "" for none.
	Group string
	// DependOnGroup lists the load-order-group names this service depends on
	// (the group-name entries of CreateService's lpDependencies parameter,
	// conventionally prefixed with SC_GROUP_IDENTIFIER on the wire - see
	// Install's doc comment), or nil for none.
	DependOnGroup []string
	// DependOnService lists the plain service names this service depends on
	// (the service-name entries of CreateService's lpDependencies parameter),
	// or nil for none.
	DependOnService []string
}

// Service type constants (the dwServiceType parameter/Type registry value),
// from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwServiceType" section's table (verified 2026-07-10).
const (
	// TypeKernelDriver (0x00000001) is SERVICE_KERNEL_DRIVER: "Driver
	// service."
	TypeKernelDriver uint32 = 0x00000001
	// TypeFileSystemDriver (0x00000002) is SERVICE_FILE_SYSTEM_DRIVER: "File
	// system driver service."
	TypeFileSystemDriver uint32 = 0x00000002
	// TypeWin32OwnProcess (0x00000010) is SERVICE_WIN32_OWN_PROCESS:
	// "Service that runs in its own process."
	TypeWin32OwnProcess uint32 = 0x00000010
	// TypeWin32ShareProcess (0x00000020) is SERVICE_WIN32_SHARE_PROCESS:
	// "Service that shares a process with one or more other services."
	TypeWin32ShareProcess uint32 = 0x00000020
)

// Service start-type constants (the dwStartType parameter/Start registry
// value), from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwStartType" section's table (verified 2026-07-10).
const (
	// StartBoot (0x00000000) is SERVICE_BOOT_START: "A device driver started
	// by the system loader. This value is valid only for driver services."
	StartBoot uint32 = 0x00000000
	// StartSystem (0x00000001) is SERVICE_SYSTEM_START: "A device driver
	// started by the IoInitSystem function. This value is valid only for
	// driver services."
	StartSystem uint32 = 0x00000001
	// StartAuto (0x00000002) is SERVICE_AUTO_START: "A service started
	// automatically by the service control manager during system startup."
	StartAuto uint32 = 0x00000002
	// StartDemand (0x00000003) is SERVICE_DEMAND_START: "A service started
	// by the service control manager when a process calls the StartService
	// function."
	StartDemand uint32 = 0x00000003
	// StartDisabled (0x00000004) is SERVICE_DISABLED: "A service that cannot
	// be started."
	StartDisabled uint32 = 0x00000004
)

// Service error-control constants (the dwErrorControl parameter/
// ErrorControl registry value), from "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// the "[in] dwErrorControl" section's table (verified 2026-07-10).
const (
	// ErrorIgnore (0x00000000) is SERVICE_ERROR_IGNORE: "The startup program
	// ignores the error and continues the startup operation."
	ErrorIgnore uint32 = 0x00000000
	// ErrorNormal (0x00000001) is SERVICE_ERROR_NORMAL: "The startup program
	// logs the error in the event log but continues the startup operation."
	ErrorNormal uint32 = 0x00000001
	// ErrorSevere (0x00000002) is SERVICE_ERROR_SEVERE: "The startup program
	// logs the error in the event log. If the last-known-good configuration
	// is being started, the startup operation continues. Otherwise, the
	// system is restarted with the last-known-good configuration."
	ErrorSevere uint32 = 0x00000002
	// ErrorCritical (0x00000003) is SERVICE_ERROR_CRITICAL: "...If the
	// last-known-good configuration is being started, the startup operation
	// fails. Otherwise, the system is restarted with the last-known good
	// configuration."
	ErrorCritical uint32 = 0x00000003
)

// wrapErr is a small helper for adding context to errors without pulling in
// a dependency, mirroring the sibling regf/driver packages' own wrapErr.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("service: %s: %w", what, err)
}
