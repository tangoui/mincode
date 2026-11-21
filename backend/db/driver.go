// 外部数据库驱动检测与安装。
//
// 大多数 database/sql 驱动已随 Go 应用编译。此处仅检测仍依赖本机动态库的客户端：
// Oracle Instant Client 与 YashanDB C Client。db_detect_install_env 复用系统包管理器
// 探测；db_install_driver 因缺少可靠的跨平台自动安装流程，暂返回不支持。
package db

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OdbcDriverStatus 单个驱动检测结果。
type OdbcDriverStatus struct {
	Key        string  `json:"key"`
	DriverName string  `json:"driverName"`
	Installed  bool    `json:"installed"`
	DLLPath    *string `json:"dllPath"`
}

// InstallEnvInfo 安装环境信息。
type InstallEnvInfo struct {
	Platform       string  `json:"platform"`
	PkgManager     *string `json:"pkgManager"`
	PkgManagerPath *string `json:"pkgManagerPath"`
}

// checkODBCDrivers 检测需要外部安装的驱动（Oracle / YashanDB）。
func checkODBCDrivers() []OdbcDriverStatus {
	return []OdbcDriverStatus{
		detectOracle(),
		detectYashanDB(),
	}
}

func detectOracle() OdbcDriverStatus {
	name := "Oracle Instant Client"
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		// 无法在纯 Go 侧可靠查注册表，退化为路径探测
	case "darwin":
		candidates = []string{
			"/usr/local/lib/libclntsh.dylib",
		}
	default: // linux
		candidates = []string{
			"/usr/lib/libclntsh.so",
		}
	}
	for _, p := range candidates {
		if fileExists(p) {
			dp := p
			return OdbcDriverStatus{Key: "oracle", DriverName: name, Installed: true, DLLPath: &dp}
		}
	}
	return OdbcDriverStatus{Key: "oracle", DriverName: name, Installed: false}
}

func detectYashanDB() OdbcDriverStatus {
	name := "YashanDB C Client"
	libName := yashanClientLibraryName(runtime.GOOS)
	if p := findYashanLib(libName); p != "" {
		return OdbcDriverStatus{Key: "yashandb", DriverName: name, Installed: true, DLLPath: &p}
	}
	return OdbcDriverStatus{Key: "yashandb", DriverName: name, Installed: false}
}

// yashandb-go 在 Windows 加载 yascli.dll，在其他平台固定加载 libyascli.so。
// 即使在 macOS 上也不能将候选文件误写为 libyascli.dylib。
func yashanClientLibraryName(goos string) string {
	if goos == "windows" {
		return "yascli.dll"
	}
	return "libyascli.so"
}

func findYashanLib(libName string) string {
	for _, dir := range yashanLibSearchDirs() {
		p := filepath.Join(dir, libName)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func yashanLibSearchDirs() []string {
	dirs := make([]string, 0, 20)
	appendDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	appendPathList := func(value string) {
		for _, dir := range filepath.SplitList(value) {
			appendDir(dir)
		}
	}

	appendPathList(os.Getenv("YASHANDB_LIB_DIR"))
	if home := strings.TrimSpace(os.Getenv("YASHANDB_HOME")); home != "" {
		for _, sub := range []string{"client/lib", "driver/libcli/win64", "driver/libcli/linux64", "lib", "bin", ""} {
			if sub == "" {
				appendDir(home)
			} else {
				appendDir(filepath.Join(home, sub))
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		appendDir(filepath.Join(home, ".yashandb", "client", "lib"))
	}

	switch runtime.GOOS {
	case "windows":
		appendPathList(os.Getenv("PATH"))
	case "darwin":
		appendPathList(os.Getenv("DYLD_LIBRARY_PATH"))
		appendPathList(os.Getenv("LD_LIBRARY_PATH"))
		appendDir("/opt/homebrew/lib")
		appendDir("/usr/local/lib")
	default:
		appendPathList(os.Getenv("LD_LIBRARY_PATH"))
		appendDir("/usr/local/lib")
		appendDir("/usr/lib")
		appendDir("/lib")
	}
	if exe, err := os.Executable(); err == nil {
		appendDir(filepath.Dir(exe))
	}
	return dirs
}

// detectInstallEnv 探测系统包管理器。
func detectInstallEnv() InstallEnvInfo {
	switch runtime.GOOS {
	case "windows":
		if p := lookPath("winget"); p != "" {
			return InstallEnvInfo{Platform: "windows", PkgManager: strPtr("winget"), PkgManagerPath: &p}
		}
		return InstallEnvInfo{Platform: "windows"}
	case "darwin":
		if p := lookPath("brew"); p != "" {
			return InstallEnvInfo{Platform: "macos", PkgManager: strPtr("brew"), PkgManagerPath: &p}
		}
		return InstallEnvInfo{Platform: "macos"}
	case "linux":
		for _, pm := range []string{"dnf", "yum", "apt-get"} {
			if p := lookPath(pm); p != "" {
				return InstallEnvInfo{Platform: "linux", PkgManager: strPtr(pm), PkgManagerPath: &p}
			}
		}
		return InstallEnvInfo{Platform: "linux"}
	default:
		return InstallEnvInfo{Platform: "unknown"}
	}
}

// ── 辅助 ──

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(p)
}

func strPtr(s string) *string { return &s }
