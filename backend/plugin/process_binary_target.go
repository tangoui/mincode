package plugin

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"runtime"
	"strings"
)

// executableTargets reports the GOOS/GOARCH pairs a native executable was built
// for, or nil when the format is not recognised. A universal Mach-O lists each slice.
func executableTargets(path string) []string {
	if file, err := macho.OpenFat(path); err == nil {
		defer file.Close()
		targets := make([]string, 0, len(file.Arches))
		for _, arch := range file.Arches {
			targets = append(targets, "darwin/"+machoArch(arch.Cpu))
		}
		return targets
	}
	if file, err := macho.Open(path); err == nil {
		defer file.Close()
		return []string{"darwin/" + machoArch(file.Cpu)}
	}
	if file, err := elf.Open(path); err == nil {
		defer file.Close()
		arch := file.Machine.String()
		switch file.Machine {
		case elf.EM_X86_64:
			arch = "amd64"
		case elf.EM_AARCH64:
			arch = "arm64"
		}
		return []string{"linux/" + arch}
	}
	if file, err := pe.Open(path); err == nil {
		defer file.Close()
		arch := fmt.Sprintf("0x%x", file.Machine)
		switch file.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			arch = "amd64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			arch = "arm64"
		}
		return []string{"windows/" + arch}
	}
	return nil
}

func machoArch(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	}
	return cpu.String()
}

// executableTargetMismatch explains a plugin binary built for another platform,
// which otherwise surfaces only as an opaque "exec format error".
func executableTargetMismatch(path, entry string) error {
	targets := executableTargets(path)
	if len(targets) == 0 {
		return nil
	}
	host := runtime.GOOS + "/" + runtime.GOARCH
	for _, target := range targets {
		if target == host {
			return nil
		}
	}
	return fmt.Errorf("plugin executable %s is built for %s, but this host is %s; reinstall a %s build of the plugin",
		entry, strings.Join(targets, ", "), host, host)
}

// verifyPackageExecutableTarget rejects a package whose native entry binary was
// built for another platform before installation, development pairing, or a
// generation switch, so the mismatch is reported up front instead of only when
// the process runtime fails to fork/exec. Missing entries, escaped paths, and
// non-native entries (Node scripts, unknown formats) stay nil here; entry
// existence and path safety are enforced by manifest validation and startup.
func verifyPackageExecutableTarget(root string, manifest Manifest) error {
	if manifest.isDeclarative() {
		return nil
	}
	entry, err := manifest.Runtime.selectedEntry()
	if err != nil {
		return nil
	}
	entryPath, err := resolvePluginPath(root, entry)
	if err != nil {
		return nil
	}
	return executableTargetMismatch(entryPath, entry)
}
