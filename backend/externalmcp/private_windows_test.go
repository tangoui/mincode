package externalmcp

import (
	"mincode/app/backend/core"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateDescriptor(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, test := range []struct {
		name, sddl string
		valid      bool
	}{
		{"private", "O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"user-only", "O:" + sid + "D:P(A;;FA;;;" + sid + ")", true},
		{"everyone-read", "O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)", false},
		{"users-write", "O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FW;;;BU)", false},
		{"inheritance-enabled", "O:" + sid + "D:(A;;FA;;;" + sid + ")", false},
		{"foreign-owner", "O:SYD:P(A;;FA;;;" + sid + ")", false},
		{"null-dacl", "O:" + sid + "D:PNO_ACCESS_CONTROL", false},
		{"empty-dacl", "O:" + sid + "D:P", false},
		{"read-only-user", "O:" + sid + "D:P(A;;FR;;;" + sid + ")", false},
		{"inherit-only-user", "O:" + sid + "D:P(A;OICIIO;FA;;;" + sid + ")", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyPrivateDescriptor(sd); (err == nil) != test.valid {
				t.Fatalf("verifyPrivateDescriptor = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestWindowsPrivateACESIDBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		size  uint16
		count byte
		valid bool
	}{
		{"truncated-header", 12, 0, false},
		{"truncated-authority", 15, 0, false},
		{"truncated-subauthority", 16, 1, false},
		{"too-many-subauthorities", 80, 16, false},
		{"valid-system-sid", 20, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := make([]uint32, 20)
			ace := (*windows.ACCESS_ALLOWED_ACE)(unsafe.Pointer(&storage[0]))
			ace.Header.AceSize = test.size
			data := unsafe.Slice((*byte)(unsafe.Pointer(&ace.SidStart)), 72)
			data[0] = 1
			data[1] = test.count
			data[7] = 5
			data[8] = 18
			if _, err := privateACESID(ace); (err == nil) != test.valid {
				t.Fatalf("privateACESID = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func setTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// 共享应用数据目录由其他模块以继承 ACL 创建，启用外部 MCP 时必须接受且不改写其权限。
func TestWindowsSharedParentWithInheritedACLIsAccepted(t *testing.T) {
	useTestDataDir(t)
	parent := filepath.Join(dataDir(), core.AppDataNamespace)
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("shared parent with inherited ACL rejected: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("shared parent ACL was modified")
	}
	if err := verifyPrivatePath(filepath.Join(parent, "external-mcp"), true); err != nil {
		t.Fatalf("external-mcp directory is not private: %v", err)
	}
	if _, err := readConfig(); err != nil {
		t.Fatalf("stdio client cannot read record: %v", err)
	}
}

func TestWindowsUnsafeExistingDirectoryIsNotRepaired(t *testing.T) {
	useTestDataDir(t)
	private := filepath.Join(dataDir(), core.AppDataNamespace, "external-mcp")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setTestDACL(t, private, "D:P(A;;FA;;;"+user.User.Sid.String()+")(A;;FR;;;WD)")
	before, err := windows.GetNamedSecurityInfo(private, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err == nil {
		t.Fatal("unsafe existing directory accepted")
	}
	after, err := windows.GetNamedSecurityInfo(private, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("existing directory ACL was modified")
	}
	if _, err := os.Stat(filepath.Join(private, "current.json")); !os.IsNotExist(err) {
		t.Fatalf("unsafe directory was used: %v", err)
	}
}

func TestWindowsRendezvousACLAndHardlinkRejected(t *testing.T) {
	useTestDataDir(t)
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	path, err := rendezvousPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivatePath(path, false); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias.json")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(); err == nil {
		t.Fatal("hardlinked token accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setTestDACL(t, path, "D:P(A;;FA;;;"+user.User.Sid.String()+")(A;;FR;;;WD)")
	if _, err := readConfig(); err == nil {
		t.Fatal("publicly readable token accepted")
	}
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err == nil {
		t.Fatal("unsafe token overwritten")
	}
}

func TestWindowsRendezvousAncestorJunctionRejected(t *testing.T) {
	original := dataDir
	root := t.TempDir()
	target := t.TempDir()
	junction := filepath.Join(root, "junction")
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	dataDir = func() string { return junction }
	t.Cleanup(func() { dataDir = original })
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err == nil {
		t.Fatal("ancestor junction accepted")
	}
	if _, err := os.Stat(filepath.Join(target, core.AppDataNamespace)); !os.IsNotExist(err) {
		t.Fatalf("junction target was modified: %v", err)
	}
}

func TestWindowsSafeStaleRecordIsRemoved(t *testing.T) {
	useTestDataDir(t)
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	path, err := rendezvousPath()
	if err != nil {
		t.Fatal(err)
	}
	RemoveStaleRecord()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("safe stale record retained: %v", err)
	}
}

func TestWindowsRendezvousReparseRecordIsPreserved(t *testing.T) {
	useTestDataDir(t)
	path, err := rendezvousPath()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(target, []byte("do not touch"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(); err == nil {
		t.Fatal("reparse token accepted")
	}
	if err := SetControlRecord("http://127.0.0.1:80", strings.Repeat("a", 64)); err == nil {
		t.Fatal("reparse token overwritten")
	}
	RemoveStaleRecord()
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("reparse record removed: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "do not touch" {
		t.Fatalf("reparse target modified: %v", err)
	}
}

func TestWindowsPrivateParentsCannotBeRenamedWhileLocked(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "parent")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	release, err := lockPrivateParents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.Rename(path, path+"-moved"); err == nil {
		t.Fatal("locked ancestor was renamed")
	}
}
