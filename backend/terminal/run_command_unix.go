//go:build !windows

package terminal

// shellRunProgram/shellRunArgs：Unix 平台通过系统 shell 解释整条命令，
// 与用户终端的交互语义保持一致。
func shellRunProgram() string {
	return "/bin/sh"
}

func shellRunArgs(command string) []string {
	return []string{"-c", command}
}
