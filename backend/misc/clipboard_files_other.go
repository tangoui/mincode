//go:build !darwin

package misc

import (
	"bufio"
	"bytes"
	"errors"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

func clipboardWriteFiles(paths []string) error {
	switch runtime.GOOS {
	case "windows":
		cmd, err := windowsClipboardWriteFilesCommand(paths)
		if err != nil {
			return err
		}
		if err := runClipboardCommand(cmd, nil); err != nil {
			return errors.New("clipboard write files 失败: " + err.Error())
		}
		return nil
	default:
		cmd, err := linuxClipboardCommand("-i")
		if err != nil {
			return err
		}
		var lines []string
		for _, p := range paths {
			lines = append(lines, (&url.URL{Scheme: "file", Path: p}).String())
		}
		cmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
		if err := runClipboardCommand(cmd, nil); err != nil {
			return errors.New("clipboard write files 失败: " + err.Error())
		}
		return nil
	}
}

func clipboardReadFiles() ([]string, error) {
	switch runtime.GOOS {
	case "windows":
		var out bytes.Buffer
		err := runClipboardCommand(windowsClipboardReadFilesCommand(), &out)
		if err != nil {
			return nil, errors.New("clipboard read files 失败: " + err.Error())
		}
		var paths []string
		scanner := bufio.NewScanner(strings.NewReader(out.String()))
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				paths = append(paths, line)
			}
		}
		return paths, nil
	default:
		cmd, err := linuxClipboardCommand("-o", "-t", "text/uri-list")
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		if err := runClipboardCommand(cmd, &out); err != nil {
			return nil, errors.New("clipboard read files 失败: " + err.Error())
		}
		var paths []string
		scanner := bufio.NewScanner(strings.NewReader(out.String()))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parsed, parseErr := url.Parse(line)
			if parseErr != nil || parsed.Scheme != "file" {
				continue
			}
			if p, pathErr := pasteboardPathFromFileURL(parsed); pathErr == nil {
				paths = append(paths, p)
			}
		}
		return paths, nil
	}
}

// pasteboardPathFromFileURL 将 file:// URL 解码为本地路径（处理百分号转义）。
func pasteboardPathFromFileURL(u *url.URL) (string, error) {
	if runtime.GOOS == "windows" {
		host := u.Host
		path := strings.TrimPrefix(u.Path, "/")
		if host != "" {
			return "\\\\" + host + "\\" + strings.ReplaceAll(path, "/", "\\"), nil
		}
		return strings.ReplaceAll(path, "/", "\\"), nil
	}
	return u.Path, nil
}

func linuxClipboardCommand(extra ...string) (*exec.Cmd, error) {
	args := []string{"-selection", "clipboard"}
	args = append(args, extra...)
	if _, err := exec.LookPath("xclip"); err == nil {
		return exec.Command("xclip", args...), nil
	}
	return nil, errors.New("clipboard files: 需要 xclip")
}
