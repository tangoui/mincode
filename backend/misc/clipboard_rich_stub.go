//go:build !darwin

package misc

import "errors"

func clipboardWriteRich(_, _, _ string, _ []byte) error {
	return errors.New("rich clipboard images are not supported on this platform")
}
