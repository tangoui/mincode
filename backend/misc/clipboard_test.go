package misc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 替身命令验证实际读写链路的语言环境，不接触用户的系统剪贴板。
func TestClipboardDarwinUTF8Locale(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS clipboard commands only")
	}
	for _, locale := range []string{"", "C", "zh_CN.GB18030"} {
		t.Run("locale="+locale, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("LANG", locale)
			t.Setenv("LC_ALL", locale)
			t.Setenv("LC_CTYPE", locale)
			const text = "修复：空结果可添加筛选条件 🧪\n第二行 café\n"
			readScript := "#!/bin/sh\nif [ \"$LC_ALL\" != en_US.UTF-8 ] || [ \"$LANG\" != en_US.UTF-8 ]; then\n  printf '\\377'\nelse\n  printf '%s' '" + text + "'\nfi\n"
			writeScript := "#!/bin/sh\n[ \"$LC_ALL\" = en_US.UTF-8 ] && [ \"$LANG\" = en_US.UTF-8 ] || exit 1\n[ \"$(/bin/cat)\" = '修复：空结果可添加筛选条件 🧪\n第二行 café' ]\n"
			for name, script := range map[string]string{"pbpaste": readScript, "pbcopy": writeScript} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Run("read", func(t *testing.T) {
				got, err := clipboardRead()
				if err != nil {
					t.Fatal(err)
				}
				if got != text {
					t.Fatal("clipboard read did not preserve UTF-8 text")
				}
			})
			t.Run("write", func(t *testing.T) {
				if err := clipboardWrite(text); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
