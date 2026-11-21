package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ReadAllLimited 严格读取最多 max 字节；超限时返回明确错误，不返回截断协议数据。
func ReadAllLimited(r io.Reader, max int64) ([]byte, error) {
	if max < 0 {
		return nil, errors.New("读取上限不能为负数")
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("响应大小超过上限(%d 字节)", max)
	}
	return data, nil
}

// CappedWriter 只保留前 max 字节，但持续接受后续写入以避免阻塞子进程管道。
type CappedWriter struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func NewCappedWriter(max int) *CappedWriter {
	if max < 0 {
		max = 0
	}
	return &CappedWriter{max: max, buf: make([]byte, 0, min(max, 64*1024))}
}

func (w *CappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) < w.max {
		take := min(w.max-len(w.buf), len(p))
		w.buf = append(w.buf, p[:take]...)
		w.truncated = take < len(p)
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *CappedWriter) Bytes() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]byte(nil), w.buf...)
	return out, w.truncated
}

// IdentityBrand 是设备指纹与派生密钥使用的品牌前缀，跨 crypto/device/license 域共享。
const IdentityBrand = "app"

// GitErr 是一个可作为 error 使用的字符串错误类型（跨 git/sessions 域共享）。
type GitErr string

func (e GitErr) Error() string { return string(e) }

// ErrString 用给定文案构造一个 error。
func ErrString(s string) error { return GitErr(s) }

// TakeChars 保留字符串开头 max 个字符（按 rune 计数）。
func TakeChars(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// ReadCapped 从 r 读取至多 cap 字节，超出则截断并返回 truncated=true（跨 ai/plugin 域共享）。
func ReadCapped(r interface{ Read([]byte) (int, error) }, cap int) ([]byte, bool) {
	buf := make([]byte, 0, min(cap, 64*1024))
	chunk := make([]byte, 8192)
	truncated := false
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			if len(buf) < cap {
				take := min(cap-len(buf), n)
				buf = append(buf, chunk[:take]...)
				if take < n {
					truncated = true
				}
			} else {
				truncated = true
			}
		}
		if err != nil {
			break
		}
	}
	return buf, truncated
}

// ─── 设备指纹与用户数据目录 ──────────────────────────────────────────────────
// 与 Tauri src-tauri/src/device_id.rs 对齐：
//
//	fingerprint = sha256_hex("app:<machineId>:<username>")
//	machineId：macOS=IOPlatformUUID，Linux=/etc/machine-id，Windows=MachineGuid

var (
	deviceFpOnce sync.Once
	deviceFpVal  string
)

// DeviceFingerprint 返回稳定的设备指纹（首次计算后缓存）。
func DeviceFingerprint() string {
	deviceFpOnce.Do(func() {
		raw := IdentityBrand + ":" + readMachineID() + ":" + whoamiUsername()
		sum := sha256.Sum256([]byte(raw))
		deviceFpVal = hex.EncodeToString(sum[:])
	})
	return deviceFpVal
}

func whoamiUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	return "unknown"
}

func readMachineID() string {
	switch runtime.GOOS {
	case "darwin":
		cmd, cancel := CommandWithTimeout(5*time.Second, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
		defer cancel()
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "IOPlatformUUID") {
				parts := strings.Split(line, "\"")
				if len(parts) >= 4 {
					return strings.TrimSpace(parts[3])
				}
			}
		}
		return ""
	case "linux":
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				id := strings.TrimSpace(string(b))
				if id != "" {
					return id
				}
			}
		}
		return ""
	case "windows":
		cmd, cancel := CommandWithTimeout(5*time.Second, "reg", "query",
			`HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Cryptography`,
			"/v", "MachineGuid")
		defer cancel()
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "MachineGuid") {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					return fields[len(fields)-1]
				}
			}
		}
		return ""
	default:
		return ""
	}
}

// userDataDirEnv, when set to an absolute path, replaces the platform data
// directory for every backend package: another host process (the Viva
// prototype, a test harness) then keeps all of its data apart from the user's
// MinCode. Unset or relative, the platform directory below is used.
const userDataDirEnv = "MINCODE_DATA_DIR"

// UserDataDir 返回平台数据目录（对齐 Rust dirs::data_dir）：
//
//	macOS   => ~/Library/Application Support
//	Linux   => $XDG_DATA_HOME 或 ~/.local/share
//	Windows => %APPDATA%
func UserDataDir() string {
	if dir := os.Getenv(userDataDirEnv); dir != "" && filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support")
		}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return appData
		}
	default: // linux 及其它
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return xdg
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share")
		}
	}
	return ""
}

// ─── 请求 URL 安全校验（http / mcp 域共享）──────────────────────────────────
// 对齐 Tauri src-tauri/src/http_client.rs：仅允许 http/https，且拒绝云厂商元数据端点。

// IsCloudMetadataHost 判断 host 是否为云厂商实例元数据端点（SSRF 防护）。
func IsCloudMetadataHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	switch h {
	case "169.254.169.254",
		"fd00:ec2::254",
		"100.100.100.200",
		"192.0.0.192",
		"metadata.google.internal",
		"metadata.goog",
		"metadata.azure.com",
		"metadata":
		return true
	}
	return false
}

// ValidateRequestURL 校验一个出站请求 URL：仅允许 http/https，拒绝云元数据端点。
func ValidateRequestURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效 URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("不支持的协议: %s(仅允许 http/https)", scheme)
	}
	if h := parsed.Hostname(); h != "" && IsCloudMetadataHost(h) {
		return errors.New("拒绝访问云厂商元数据端点")
	}
	return nil
}
