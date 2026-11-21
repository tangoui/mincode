//go:build !windows

package browser

import "unsafe"

// Existing non-Windows implementations retain their native command contract.
func platformSetBoundsResult(win unsafe.Pointer, id string, x, y, w, h float64, visible bool) error {
	platformSetBounds(win, id, x, y, w, h, visible)
	return nil
}
func platformActivateResult(win unsafe.Pointer, id string) error {
	platformActivate(win, id)
	return nil
}
func platformNavigateResult(win unsafe.Pointer, id, url string) error {
	platformNavigate(win, id, url)
	return nil
}
func platformReloadResult(win unsafe.Pointer, id string) error {
	platformReload(win, id)
	return nil
}
func platformSetZoomResult(win unsafe.Pointer, id string, zoom float64) error {
	platformSetZoom(win, id, zoom)
	return nil
}
func platformClearDataResult(win unsafe.Pointer) error {
	platformClearData(win)
	return nil
}
