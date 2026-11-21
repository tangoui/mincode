package misc

import (
	"errors"
	"os/exec"
	"strings"

	"mincode/app/backend/core"
)

// Windows 剪贴板 PowerShell 命令。脚本均为常量：用户数据只经 stdin 以原始 UTF-8 字节传入、
// 经 stdout 以原始 UTF-8 字节传出，既不拼进脚本（避免引号/通配符注入），也不依赖控制台代码页。
// 脚本内不含双引号，避免 Windows 命令行转义差异。
const (
	powershellReadStdinUTF8 = "$ErrorActionPreference='Stop'; " +
		"$ms=New-Object System.IO.MemoryStream; [Console]::OpenStandardInput().CopyTo($ms); " +
		"$s=[Text.Encoding]::UTF8.GetString($ms.ToArray()); "

	// Set-Clipboard 对空字符串会抛错，空文本时改为清空剪贴板。
	windowsClipboardWriteTextScript = powershellReadStdinUTF8 +
		"if ($s.Length -eq 0) { Set-Clipboard -Value $null } else { Set-Clipboard -Value $s }"

	// -Raw 返回原文，不按行拆分、不追加换行，与 pbpaste / xclip 一致。
	windowsClipboardReadTextScript = "$ErrorActionPreference='Stop'; " +
		"$b=[Text.Encoding]::UTF8.GetBytes([string](Get-Clipboard -Raw)); " +
		"$o=[Console]::OpenStandardOutput(); $o.Write($b, 0, $b.Length); $o.Flush()"

	// FileDropList 返回 FileInfo 对象，默认输出是格式化表格且受控制台代码页影响；
	// 只取 FullName，以换行分隔按原始 UTF-8 字节输出。
	windowsClipboardReadFilesScript = "$ErrorActionPreference='Stop'; " +
		"$f=@(Get-Clipboard -Format FileDropList | ForEach-Object { $_.FullName }); " +
		"$b=[Text.Encoding]::UTF8.GetBytes(($f -join [string][char]10)); " +
		"$o=[Console]::OpenStandardOutput(); $o.Write($b, 0, $b.Length); $o.Flush()"

	// -LiteralPath 不展开通配符；路径以换行分隔经 stdin 传入。
	windowsClipboardWriteFilesScript = powershellReadStdinUTF8 +
		"Set-Clipboard -LiteralPath ($s.Split([char]10))"
)

func windowsPowerShellCommand(script string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	core.PrepareBackgroundCommand(cmd)
	return cmd
}

func windowsClipboardWriteTextCommand(text string) *exec.Cmd {
	cmd := windowsPowerShellCommand(windowsClipboardWriteTextScript)
	cmd.Stdin = strings.NewReader(text)
	return cmd
}

func windowsClipboardReadTextCommand() *exec.Cmd {
	return windowsPowerShellCommand(windowsClipboardReadTextScript)
}

func windowsClipboardReadFilesCommand() *exec.Cmd {
	return windowsPowerShellCommand(windowsClipboardReadFilesScript)
}

func windowsClipboardWriteFilesCommand(paths []string) (*exec.Cmd, error) {
	if len(paths) == 0 {
		return nil, errors.New("clipboard write files: 路径为空")
	}
	for _, p := range paths {
		if p == "" || strings.ContainsAny(p, "\r\n\x00") {
			return nil, errors.New("clipboard write files: 路径包含非法字符")
		}
	}
	cmd := windowsPowerShellCommand(windowsClipboardWriteFilesScript)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n"))
	return cmd, nil
}
