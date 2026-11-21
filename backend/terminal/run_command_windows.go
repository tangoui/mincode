//go:build windows

package terminal

// shellRunProgram/shellRunArgs：Windows 平台通过 cmd 解释整条命令。
func shellRunProgram() string {
	return "cmd"
}

func shellRunArgs(command string) []string {
	return []string{"/C", command}
}
