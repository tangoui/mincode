// Clipboard 域：读写系统剪贴板文本。
// 使用平台原生命令，避免额外依赖：
//
//	macOS   => pbcopy / pbpaste
//	Windows => powershell Set-Clipboard / Get-Clipboard -Raw（经 stdin/stdout 传 UTF-8）
//	Linux   => xclip 或 xsel
//
// 命令契约：
//
//	clipboard_write_text {text} -> void
//	clipboard_read_text         -> string
package misc

import (
	"mincode/app/backend/core"

	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func init() {
	core.Register("clipboard_write_text", clipboardWriteTextHandler)
	core.Register("clipboard_read_text", clipboardReadTextHandler)
}

type clipboardTextArg struct {
	Text string `json:"text"`
}

func clipboardWriteTextHandler(_, argsJson string) (string, error) {
	var a clipboardTextArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := clipboardWrite(a.Text); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func clipboardReadTextHandler(_, _ string) (string, error) {
	text, err := clipboardRead()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(text)
}

func clipboardWrite(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
		// 桌面启动不一定带 UTF-8 locale；仅覆盖子进程，避免中文按系统旧编码读写。
		cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	case "windows":
		if err := runClipboardCommand(windowsClipboardWriteTextCommand(text), nil); err != nil {
			return errors.New("clipboard write 失败: " + err.Error())
		}
		return nil
	default:
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return errors.New("clipboard write: 需要 xclip 或 xsel")
		}
	}
	cmd.Stdin = strings.NewReader(text)
	if err := runClipboardCommand(cmd, nil); err != nil {
		return errors.New("clipboard write 失败: " + err.Error())
	}
	return nil
}

func clipboardRead() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbpaste")
		// LC_ALL 优先于 LC_CTYPE，确保返回给 JSON/前端的字节始终使用 UTF-8。
		cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	case "windows":
		cmd = windowsClipboardReadTextCommand()
	default:
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--output")
		} else {
			return "", errors.New("clipboard read: 需要 xclip 或 xsel")
		}
	}
	var out bytes.Buffer
	if err := runClipboardCommand(cmd, &out); err != nil {
		return "", errors.New("clipboard read 失败: " + err.Error())
	}
	return out.String(), nil
}

const clipboardCommandTimeout = 10 * time.Second

// runClipboardCommand 为剪贴板工具设置超时，避免 xclip 等在无显示服务时挂起 IPC 调用。
func runClipboardCommand(cmd *exec.Cmd, stdout io.Writer) error {
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(clipboardCommandTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-done
		return errors.New("timed out")
	}
}
