package sftp

import (
	"os"
	"testing"

	"github.com/jlaffaye/ftp"
)

func TestFormatPermissionsMarksEntryType(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
		want string
	}{
		{"regular file", 0o644, "-rw-r--r--"},
		{"directory", os.ModeDir | 0o755, "drwxr-xr-x"},
		{"symbolic link", os.ModeSymlink | 0o777, "lrwxrwxrwx"},
		{"symbolic link without permission bits", os.ModeSymlink, "l---------"},
		{"executable", 0o750, "-rwxr-x---"},
	}
	for _, test := range tests {
		if got := formatPermissions(test.mode); got != test.want {
			t.Errorf("%s: formatPermissions(%v) = %q, want %q", test.name, test.mode, got, test.want)
		}
	}
}

// FTP listings carry the entry type from the LIST line: a link keeps ModeSymlink, so the
// listing marks it with "l" as well.
func TestFormatPermissionsFTPEntryTypes(t *testing.T) {
	tests := []struct {
		entryType ftp.EntryType
		want      string
	}{
		{ftp.EntryTypeFile, "----------"},
		{ftp.EntryTypeFolder, "d---------"},
		{ftp.EntryTypeLink, "l---------"},
	}
	for _, test := range tests {
		info := ftpFileInfo{entry: &ftp.Entry{Name: "x", Type: test.entryType}}
		if got := formatPermissions(info.Mode()); got != test.want {
			t.Errorf("FTP entry type %v: formatPermissions = %q, want %q", test.entryType, got, test.want)
		}
	}
}
