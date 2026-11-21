package misc

import (
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
