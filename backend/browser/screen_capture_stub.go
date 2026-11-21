//go:build !windows

package browser

import "errors"

func captureWindowsScreenRegionPNG(_, _, _, _ int) ([]byte, error) {
	return nil, errors.New("Windows 区域截图仅支持 Windows")
}
