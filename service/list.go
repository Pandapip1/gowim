package service

import (
	"errors"

	"github.com/Pandapip1/gowim/regf"
)

// List returns the name of every service currently registered directly
// under servicesKey - i.e. every immediate Services\<name> subkey - in
// subkey order. It does not attempt to Read (and so cannot fail on) any
// individual service's values, so a malformed entry that would make Read
// error still shows up here; call Read(servicesKey, name) per name for the
// full Service, skipping any that error if a hive has malformed entries.
func List(servicesKey *regf.Key) ([]string, error) {
	if servicesKey == nil {
		return nil, wrapErr("list", errors.New("nil services key"))
	}

	names := make([]string, 0, len(servicesKey.Subkeys))
	for _, k := range servicesKey.Subkeys {
		names = append(names, k.NameUTF8())
	}
	return names, nil
}
