package terminal

import (
	"fmt"
	"strings"
)

const (
	maxSSHInitialDirectoryBytes = 4096
	maxSSHPostConnectCommands   = 32
	maxSSHPostConnectCommand    = 8192
	maxSSHPostConnectTotal      = 64 * 1024
)

func normalizeSSHInitialization(directory string, commands []string) (string, []string, error) {
	directory = strings.TrimSpace(directory)
	if len(directory) > maxSSHInitialDirectoryBytes || strings.ContainsRune(directory, 0) || strings.ContainsAny(directory, "\r\n") {
		return "", nil, fmt.Errorf("SSH 默认目录无效")
	}
	if len(commands) > maxSSHPostConnectCommands {
		return "", nil, fmt.Errorf("连接后自动执行命令最多允许 %d 条", maxSSHPostConnectCommands)
	}
	cleaned := make([]string, 0, len(commands))
	total := 0
	for _, command := range commands {
		command = strings.TrimSpace(strings.ReplaceAll(command, "\r", ""))
		if command == "" {
			continue
		}
		if strings.ContainsRune(command, 0) || strings.Contains(command, "\n") || len(command) > maxSSHPostConnectCommand {
			return "", nil, fmt.Errorf("连接后自动执行命令无效")
		}
		total += len(command)
		if total > maxSSHPostConnectTotal {
			return "", nil, fmt.Errorf("连接后自动执行命令总长度超过限制")
		}
		cleaned = append(cleaned, command)
	}
	return directory, cleaned, nil
}

func enqueueSSHInitialization(proc *shellProcess, input chan<- string) {
	proc.mu.Lock()
	directory := proc.sshInitialDirectory
	commands := []string(nil)
	if proc.sshPostConnectPending {
		commands = append(commands, proc.sshPostConnectCommands...)
		proc.sshPostConnectPending = false
	}
	proc.mu.Unlock()

	if directory != "" {
		input <- sshChangeDirectoryCommand(directory) + "\r"
	}
	for _, command := range commands {
		input <- command + "\r"
	}
}

func sshChangeDirectoryCommand(directory string) string {
	switch {
	case directory == "~":
		return `cd -- "$HOME"`
	case strings.HasPrefix(directory, "~/"):
		return `cd -- "$HOME"/` + quotePOSIXShellWord(strings.TrimPrefix(directory, "~/"))
	default:
		return "cd -- " + quotePOSIXShellWord(directory)
	}
}

func quotePOSIXShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
