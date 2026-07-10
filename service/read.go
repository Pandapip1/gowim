package service

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// Read parses an existing Services\<name> subkey under servicesKey back
// into a Service - the reverse of Install/Modify's merge. It returns an
// error wrapping ErrNotFound (checkable with errors.Is) if no such subkey
// exists.
//
// Type/Start/ErrorControl are decoded as REG_DWORD (exactly 4 little-endian
// bytes; any other length is an error, since a well-formed value written by
// Install/Modify is always exactly 4 bytes). ImagePath is decoded as
// UTF-16LE and must be present, since Install/Modify always write it, so a
// Services\<name> subkey without one is treated as malformed rather than
// silently read back with ImagePath == "". Group/DisplayName/Description/
// ObjectName are decoded as UTF-16LE, with an absent value decoding to ""
// (the same "not set" convention Install/Modify use on the way in).
// DependOnGroup/DependOnService are decoded as REG_MULTI_SZ, with an absent
// value decoding to nil.
//
// Note that DependOnGroup and DependOnService are read back as two entirely
// separate REG_MULTI_SZ values, with no "+"-prefix parsing: the
// SC_GROUP_IDENTIFIER ('+') convention CreateService's/ChangeServiceConfig's
// lpDependencies parameter uses on the wire to distinguish group names from
// service names within a single combined list is not part of how
// Install/Modify persist this to the registry - they already split
// group-vs-service dependencies into the two separate DependOnGroup/
// DependOnService values before writing, so Read has no '+' prefixes left
// to strip.
func Read(servicesKey *regf.Key, name string) (Service, error) {
	if servicesKey == nil {
		return Service{}, wrapErr("read", errors.New("nil services key"))
	}
	if name == "" {
		return Service{}, wrapErr("read", errors.New("no service name given"))
	}

	key := FindSubkey(servicesKey, name)
	if key == nil {
		return Service{}, wrapErr("read", fmt.Errorf("service %q: %w", name, ErrNotFound))
	}

	svc := Service{Name: name}

	typ, err := readDWORD(key, "Type")
	if err != nil {
		return Service{}, wrapErr("read", fmt.Errorf("service %q: %w", name, err))
	}
	svc.Type = typ

	start, err := readDWORD(key, "Start")
	if err != nil {
		return Service{}, wrapErr("read", fmt.Errorf("service %q: %w", name, err))
	}
	svc.Start = start

	errCtl, err := readDWORD(key, "ErrorControl")
	if err != nil {
		return Service{}, wrapErr("read", fmt.Errorf("service %q: %w", name, err))
	}
	svc.ErrorControl = errCtl

	imagePath := FindValue(key, "ImagePath")
	if imagePath == nil {
		return Service{}, wrapErr("read", fmt.Errorf("service %q: no ImagePath value", name))
	}
	svc.ImagePath = utf16LEToString(imagePath.Data)

	svc.Group = readSZ(key, "Group")
	svc.DisplayName = readSZ(key, "DisplayName")
	svc.Description = readSZ(key, "Description")
	svc.ObjectName = readSZ(key, "ObjectName")

	svc.DependOnGroup = readMultiSZ(key, "DependOnGroup")
	svc.DependOnService = readMultiSZ(key, "DependOnService")

	return svc, nil
}
