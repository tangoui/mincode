//go:build windows

package lsp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecodeInstallOutputPreservesUTF8(t *testing.T) {
	for _, input := range []string{"", "npm install completed\r\n", "安装失败：找不到程序\r\n", "安装完成 𠮷\x00尾部"} {
		if got := decodeInstallOutput([]byte(input)); got != input {
			t.Errorf("decodeInstallOutput(%q) = %q", input, got)
		}
		if got := decodeInstallOutputCodePage([]byte(input), 437); got != input {
			t.Errorf("合法 UTF8 应优先保留，得到 %q，期望 %q", got, input)
		}
	}
}

func TestRunInstallDecodesMissingCommand(t *testing.T) {
	const command = "mincode_nonexistent_command_47319"
	output, err := runInstall(command)
	if err == nil {
		t.Fatal("不存在的命令应返回错误")
	}
	if !utf8.ValidString(output) {
		t.Errorf("命令错误输出不是合法 UTF8：%q", output)
	}
	if strings.ContainsRune(output, '\uFFFD') {
		t.Errorf("命令错误输出包含替换字符：%q", output)
	}
	if !strings.Contains(output, command) {
		t.Errorf("命令错误输出未包含命令名：%q", output)
	}
}

func TestDecodeInstallOutputCodePage(t *testing.T) {
	tests := []struct {
		name     string
		codePage uint32
		input    []byte
		want     string
	}{
		{
			name: "中文 CP936", codePage: 936,
			input: []byte{'n', 'p', 'm', ':', ' ', 0xd6, 0xd0, 0xce, 0xc4, '\r', '\n'},
			want:  "npm: 中文\r\n",
		},
		{
			name: "OEM CP437", codePage: 437,
			input: []byte{'c', 'a', 'f', 0x82, ' ', 0xb3},
			want:  "café │",
		},
		{
			name: "保留 NUL", codePage: 437,
			input: []byte{0x82, 0, 'x'}, want: "é\x00x",
		},
		{
			name: "截断 CP936 字符", codePage: 936,
			input: []byte{'x', 0x81}, want: "x?",
		},
		{
			name: "非法 UTF8 替换", codePage: 65001,
			input: []byte{'x', 0xff, 'y'}, want: "x\uFFFDy",
		},
		{
			name: "无效码页回退替换", codePage: 0xffffffff,
			input: []byte{'x', 0xff, 'y'}, want: "x\uFFFDy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeInstallOutputCodePage(tt.input, tt.codePage)
			if got != tt.want {
				t.Errorf("得到 %q，期望 %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("解码输出不是合法 UTF8：%q", got)
			}
		})
	}
}
