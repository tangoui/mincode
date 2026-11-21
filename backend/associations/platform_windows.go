package associations

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"mincode/app/plugins/api/externalopen"
)

func progID(t externalopen.Type) string { return "MinCode.External." + t.ID }
func query(t externalopen.Type) (Status, error) {
	path := `Software\Microsoft\Windows\Shell\Associations\UrlAssociations\` + t.Value + `\UserChoice`
	if t.Kind == "file" {
		path = `Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\.` + t.Value + `\UserChoice`
	}
	k, e := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	app := ""
	if e == nil {
		app, _, _ = k.GetStringValue("ProgId")
		k.Close()
	}
	// AssocQueryString also sees inherited defaults and MSIX ProgIDs, not just
	// Explorer's optional UserChoice key. Never write the protected key.
	value := t.Value
	if t.Kind == "file" {
		value = "." + value
	}
	resolved, err := defaultExecutable(value)
	executable, _ := os.Executable()
	ours := err == nil && strings.EqualFold(filepath.Clean(resolved), filepath.Clean(executable))
	if app == "" && err == nil {
		app = resolved
	}
	return Status{Supported: true, Default: ours || strings.EqualFold(app, progID(t)), Application: app, Manual: true}, nil
}
func set(t externalopen.Type, _ string, _ bool) error {
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	executable, e = filepath.Abs(executable)
	if e != nil || strings.ContainsAny(executable, "\"\r\n") {
		return fmt.Errorf("invalid application executable")
	}
	write := func(path, name, value string) error {
		k, _, e := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
		if e != nil {
			return e
		}
		defer k.Close()
		return k.SetStringValue(name, value)
	}
	base := `Software\Classes\` + progID(t)
	for _, v := range [][3]string{{base, "", t.Name}, {base + `\shell\open\command`, "", `"` + executable + `" "%1"`}, {`Software\RegisteredApplications`, "MinCode", `Software\MinCode\Capabilities`}, {`Software\MinCode\Capabilities`, "ApplicationName", "MinCode"}} {
		if e = write(v[0], v[1], v[2]); e != nil {
			return e
		}
	}
	key := "URLAssociations"
	value := t.Value
	if t.Kind == "file" {
		key = "FileAssociations"
		value = "." + value
	} else {
		if e = write(base, "URL Protocol", ""); e != nil {
			return e
		}
	}
	if e = write(`Software\MinCode\Capabilities\`+key, value, progID(t)); e != nil {
		return e
	}
	// Protected UserChoice is deliberately untouched; Windows owns selection.
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	return exec.Command(filepath.Join(systemDir, "rundll32.exe"), "url.dll,FileProtocolHandler", "ms-settings:defaultapps?registeredAppUser=MinCode").Run()
}

func defaultExecutable(value string) (string, error) {
	proc := windows.NewLazySystemDLL("shlwapi.dll").NewProc("AssocQueryStringW")
	arg, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return "", err
	}
	var size uint32 = 32768
	buf := make([]uint16, size)
	result, _, _ := proc.Call(0, 2, uintptr(unsafe.Pointer(arg)), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if result != 0 {
		return "", fmt.Errorf("cannot query default application")
	}
	return windows.UTF16ToString(buf), nil
}
