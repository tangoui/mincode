// 设备指纹命令。设备指纹/用户数据目录的实现已迁至 backend/core（见 core/helpers.go），
// 本文件仅保留命令注册与处理器，经别名 core.DeviceFingerprint() 调用 core.DeviceFingerprint。
package device

import "mincode/app/backend/core"

func init() {
	core.Register("get_device_fingerprint", getDeviceFingerprintHandler)
}

func getDeviceFingerprintHandler(_, _ string) (string, error) {
	return core.EncodeResult(core.DeviceFingerprint())
}
