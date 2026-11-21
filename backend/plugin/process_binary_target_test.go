package plugin

import (
	"bytes"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestExecutable(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalPE(machine uint16) []byte {
	var buffer bytes.Buffer
	dos := make([]byte, 0x40)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x40)
	buffer.Write(dos)
	buffer.WriteString("PE\x00\x00")
	_ = binary.Write(&buffer, binary.LittleEndian, pe.FileHeader{Machine: machine})
	buffer.Write(make([]byte, 64))
	return buffer.Bytes()
}

func minimalELF(machine elf.Machine) []byte {
	header := make([]byte, 64)
	copy(header, elf.ELFMAG)
	header[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(header[52:], 64)
	return header
}

func TestExecutableTargetMismatchExplainsForeignBinaries(t *testing.T) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	for name, test := range map[string]struct {
		data   []byte
		target string
	}{
		"windows-arm64": {minimalPE(pe.IMAGE_FILE_MACHINE_ARM64), "windows/arm64"},
		"windows-amd64": {minimalPE(pe.IMAGE_FILE_MACHINE_AMD64), "windows/amd64"},
		"linux-arm64":   {minimalELF(elf.EM_AARCH64), "linux/arm64"},
		"linux-amd64":   {minimalELF(elf.EM_X86_64), "linux/amd64"},
	} {
		path := writeTestExecutable(t, name, test.data)
		if got := executableTargets(path); len(got) != 1 || got[0] != test.target {
			t.Fatalf("%s: targets = %v, want %s", name, got, test.target)
		}
		err := executableTargetMismatch(path, "bin/plugin.exe")
		if test.target == host {
			if err != nil {
				t.Fatalf("%s: host binary reported as mismatch: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "built for "+test.target) || !strings.Contains(err.Error(), "this host is "+host) {
			t.Fatalf("%s: mismatch error = %v", name, err)
		}
	}
}

func TestExecutableTargetMismatchAcceptsHostAndUnknownFiles(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := executableTargetMismatch(self, "bin/plugin"); err != nil {
		t.Fatalf("host test binary reported as mismatch: %v", err)
	}
	script := writeTestExecutable(t, "plugin.sh", []byte("#!/bin/sh\nexit 0\n"))
	if err := executableTargetMismatch(script, "bin/plugin.sh"); err != nil {
		t.Fatalf("unknown format reported as mismatch: %v", err)
	}
}

// foreignBinaryForHost returns executable bytes for a platform that is never
// the host, so mismatch assertions hold on every CI and developer machine.
func foreignBinaryForHost() []byte {
	if runtime.GOOS == "windows" {
		return minimalELF(elf.EM_AARCH64) // linux/arm64
	}
	return minimalPE(pe.IMAGE_FILE_MACHINE_AMD64) // windows/amd64
}

func TestVerifyPackageExecutableTargetSkipsDeclarativeAndMissingEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin"), foreignBinaryForHost(), 0o700); err != nil {
		t.Fatal(err)
	}
	missing := Manifest{Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/absent"}}
	if err := verifyPackageExecutableTarget(root, missing); err != nil {
		t.Fatalf("missing entry reported as mismatch: %v", err)
	}
	declarative := Manifest{Runtime: ProcessRuntimeManifest{Type: pluginRuntimeDeclarative}}
	if err := verifyPackageExecutableTarget(root, declarative); err != nil {
		t.Fatalf("declarative package reported as mismatch: %v", err)
	}
	foreign := Manifest{Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"}}
	if err := verifyPackageExecutableTarget(root, foreign); err == nil {
		t.Fatal("foreign entry binary accepted")
	}
}

func TestInspectPackageDirectoryRejectsForeignEntryBinary(t *testing.T) {
	root := writeTestManifest(t, nil)
	entry := "plugin-runtime"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	if err := os.WriteFile(filepath.Join(root, entry), foreignBinaryForHost(), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := inspectPackageDirectory(root)
	if err == nil || !strings.Contains(err.Error(), "built for") {
		t.Fatalf("foreign entry binary accepted by inspection: %v", err)
	}
	// A non-native entry (script/unknown format) keeps passing inspection; the
	// host-format acceptance path is covered by the mismatch unit tests above.
	if err := os.WriteFile(filepath.Join(root, entry), []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectPackageDirectory(root); err != nil {
		t.Fatalf("non-native entry rejected: %v", err)
	}
}

func TestInstallLocalRejectsForeignEntryBinary(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	entry := "plugin-runtime"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	if err := os.WriteFile(filepath.Join(source, entry), foreignBinaryForHost(), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := manager.InstallLocal(source)
	if err == nil || !strings.Contains(err.Error(), "built for") {
		t.Fatalf("InstallLocal accepted a foreign entry binary: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(pluginsDirectory(), "example.plugin")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected plugin left files behind: %v", err)
	}
}
