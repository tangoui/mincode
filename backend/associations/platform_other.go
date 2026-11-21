//go:build (!darwin && !windows) || (darwin && !cgo)

package associations

import (
	"errors"

	"mincode/app/plugins/api/externalopen"
)

func query(externalopen.Type) (Status, error) { return Status{}, nil }
func set(externalopen.Type, string, bool) error {
	return errors.New("system associations are unavailable on this platform/build")
}
