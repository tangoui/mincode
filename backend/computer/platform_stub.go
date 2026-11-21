//go:build !darwin && !windows

package computer

import (
	"errors"
	"runtime"
	"time"
)

func platformStatus() Status {
	return Status{Supported: false, Platform: runtime.GOOS}
}

func platformRequestPermissions(_, _ bool) Status { return platformStatus() }
func platformCaptureScreen(_ uint32, _ time.Duration) (Capture, error) {
	return Capture{}, errors.New("desktop Computer Use is currently supported only on macOS and Windows")
}
func platformCursorPosition() (Point, error) {
	return Point{}, errors.New("desktop Computer Use is unavailable")
}
func platformKeyEvent(_ string, _ bool) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformMovePointer(_, _ float64) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformClick(_, _ float64, _ string, _ int) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformMouseDown(_, _ float64, _ string) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformMouseDrag(_, _ float64, _ string) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformMouseUp(_, _ float64, _ string) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformScroll(_, _ float64) error { return errors.New("desktop Computer Use is unavailable") }
func platformTypeText(_ string) error   { return errors.New("desktop Computer Use is unavailable") }
func platformPressKey(_ string, _ uint32) error {
	return errors.New("desktop Computer Use is unavailable")
}
func platformOpenApp(_ string) error    { return errors.New("desktop Computer Use is unavailable") }
func platformOpenURL(_, _ string) error { return errors.New("desktop Computer Use is unavailable") }
