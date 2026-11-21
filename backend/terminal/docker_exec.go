package terminal

import (
	"fmt"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

func init() {
	core.Register("start_ssh_docker_exec", cmdStartSSHDockerExec)
	core.Register("start_ssh_docker_log_stream", cmdStartSSHDockerLogStream)
}

// dockerExecShells 是允许用于 docker exec 的 shell 白名单。
// 这里不能退化成引号转义：quotePOSIXShellWord 只防 shell 元字符，
// 挡不住 /bin/../../some/binary 这类合法但越界的路径，而 shell 是可执行文件名。
var dockerExecShells = map[string]bool{
	"bash": true,
	"sh":   true,
	"ash":  true,
	"zsh":  true,
	"fish": true,
}

const (
	dockerLogStreamDefaultTail = 200
	dockerLogStreamMaxTail     = 2000
)

// buildDockerExecCommand 构造进入容器的交互式命令。
func buildDockerExecCommand(containerID, shell string) (string, error) {
	containerID = strings.TrimSpace(containerID)
	if !adminContainerPattern.MatchString(containerID) {
		return "", fmt.Errorf("Docker 容器 ID 无效")
	}
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = "sh"
	}
	if !dockerExecShells[shell] {
		return "", fmt.Errorf("Docker shell 不受支持")
	}
	return "docker exec -it " + quotePOSIXShellWord(containerID) + " " + shell, nil
}

// buildDockerLogStreamCommand 构造持续跟随容器日志的命令。
func buildDockerLogStreamCommand(containerID string, tail int) (string, error) {
	containerID = strings.TrimSpace(containerID)
	if !adminContainerPattern.MatchString(containerID) {
		return "", fmt.Errorf("Docker 容器 ID 无效")
	}
	if tail <= 0 {
		tail = dockerLogStreamDefaultTail
	}
	if tail > dockerLogStreamMaxTail {
		tail = dockerLogStreamMaxTail
	}
	return fmt.Sprintf("docker logs -f --tail %d %s", tail, quotePOSIXShellWord(containerID)), nil
}

func cmdStartSSHDockerExec(_, argsJSON string) (string, error) {
	var payload struct {
		ParentSessionID string `json:"parentSessionId"`
		SessionID       string `json:"sessionId"`
		ContainerID     string `json:"containerId"`
		Shell           string `json:"shell"`
		Cols            uint32 `json:"cols"`
		Rows            uint32 `json:"rows"`
	}
	if err := core.DecodeArgs(argsJSON, &payload); err != nil {
		return "", err
	}
	command, err := buildDockerExecCommand(payload.ContainerID, payload.Shell)
	if err != nil {
		return "", err
	}
	cols, rows := clampPtyColsRows(&payload.Cols, &payload.Rows)
	if err := startDerivedSSHSession(payload.ParentSessionID, payload.SessionID, command, true, cols, rows); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func cmdStartSSHDockerLogStream(_, argsJSON string) (string, error) {
	var payload struct {
		ParentSessionID string `json:"parentSessionId"`
		SessionID       string `json:"sessionId"`
		ContainerID     string `json:"containerId"`
		Tail            int    `json:"tail"`
	}
	if err := core.DecodeArgs(argsJSON, &payload); err != nil {
		return "", err
	}
	command, err := buildDockerLogStreamCommand(payload.ContainerID, payload.Tail)
	if err != nil {
		return "", err
	}
	// 日志流不申请 PTY：docker logs 不读 stdin，而 PTY 会引入 CR 控制字符。
	if err := startDerivedSSHSession(payload.ParentSessionID, payload.SessionID, command, false, 0, 0); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// startDerivedSSHSession 在父会话的 SSH 连接上开一条独立会话执行命令。
// 派生会话共享父连接（sharedClient）且命令退出即结束（ephemeral），
// 因此不注册 keepalive、不配置重拨、不参与 GracePeriod 重连。
func startDerivedSSHSession(parentSessionID, sessionID, command string, wantPty bool, cols, rows uint16) error {
	parentSessionID = strings.TrimSpace(parentSessionID)
	sessionID = strings.TrimSpace(sessionID)
	if parentSessionID == "" || sessionID == "" {
		return fmt.Errorf("session_id 不能为空")
	}
	if sessionID == parentSessionID {
		return fmt.Errorf("派生会话不能复用父会话 ID")
	}
	for {
		wait, owner, existing := sessions.beginStart(sessionID, true)
		if existing != nil {
			return fmt.Errorf("派生会话 ID 已存在")
		}
		if owner {
			break
		}
		<-wait
	}
	client, err := sshAdminClient(parentSessionID)
	if err != nil {
		sessions.cancelStart(sessionID)
		return err
	}

	proc := &shellProcess{
		sshClient:            &sshClientHandle{client: client},
		sharedClient:         true,
		ephemeral:            true,
		sshLinkState:         sshLinkActive,
		sshRecoveryMeta:      &sshRecoveryMeta{cols: cols, rows: rows},
		remoteForwardTargets: &sync.Map{},
	}
	if old := sessions.publishStart(sessionID, proc); old != nil {
		shutdownShellProcess(old)
	}
	if err := startSSHSessionRelay(sessionID, proc, command, wantPty, cols, rows); err != nil {
		sessions.finishStart(sessionID)
		if current, ok := sessions.removeIfSame(sessionID, proc); ok {
			shutdownShellProcess(current)
		}
		return err
	}
	sessions.finishStart(sessionID)
	return nil
}
