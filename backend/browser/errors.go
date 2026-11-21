package browser

import "fmt"

// errMountFailed 由平台层在挂载失败时构造，携带平台返回码便于排查。
func errMountFailed(code int) error {
	return fmt.Errorf("embedded browser mount failed (native code %d)", code)
}

// errDevtoolsFailed preserves the native failure code without exposing platform internals to the UI.
func errDevtoolsFailed(code int) error {
	return fmt.Errorf("embedded browser developer tools failed (native code %d)", code)
}
