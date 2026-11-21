// Package terminal 实现终端域命令：本地 Shell PTY、SSH Shell（含密码/密钥/Agent/
// keyboard-interactive 四种认证、跳板机 ProxyJump、known_hosts TOFU 校验、Grace
// Period 断线重连）、端口转发（-L/-R/-D）、Wire 数据面（本地 WebSocket 二进制帧）、
// 批量执行。命令集、payload 字段（camelCase）、事件名与 src-tauri/src/terminal/
// 保持一致，前端无需任何改动即可通过 platform 抽象层调用。
package terminal

// PortForwardRulePayload 为前端传来的单条端口转发规则。
type PortForwardRulePayload struct {
	ID         string  `json:"id"`
	RuleType   string  `json:"type"`
	BindPort   uint16  `json:"bindPort"`
	TargetHost string  `json:"targetHost"`
	TargetPort uint16  `json:"targetPort"`
	Label      *string `json:"label,omitempty"`
}

// PortForwardStatus 为返回给前端的端口转发运行状态。
type PortForwardStatus struct {
	RuleID       string `json:"ruleId"`
	IsRunning    bool   `json:"isRunning"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// SshMfaPromptItem 为单条 keyboard-interactive 提示。
type SshMfaPromptItem struct {
	Prompt string `json:"prompt"`
	Echo   bool   `json:"echo"`
}

// SshMfaPromptPayload 为 "ssh-mfa-prompt" 事件负载。
type SshMfaPromptPayload struct {
	SessionID    string             `json:"sessionId"`
	Name         string             `json:"name"`
	Instructions string             `json:"instructions"`
	Prompts      []SshMfaPromptItem `json:"prompts"`
}

// StartLocalShellPayload 为 start_local_shell 命令入参。
type StartLocalShellPayload struct {
	SessionID      string            `json:"sessionId"`
	ShellType      *string           `json:"shellType"`
	CustomPath     *string           `json:"customPath"`
	Cwd            *string           `json:"cwd"`
	InitialArgs    []string          `json:"initialArgs"`
	AttachIfExists bool              `json:"attachIfExists"`
	Cols           *uint32           `json:"cols"`
	Rows           *uint32           `json:"rows"`
	ExtraEnv       map[string]string `json:"extraEnv"`
}

// ResizeLocalShellPayload 为 resize_local_shell 命令入参。
type ResizeLocalShellPayload struct {
	SessionID string `json:"sessionId"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
}

// GetLocalShellCwdPayload 为 get_local_shell_cwd 命令入参。
type GetLocalShellCwdPayload struct {
	SessionID string `json:"sessionId"`
}

// ShellSessionIDPayload 为 shell_session_alive/shell_session_in_grace/
// shell_ssh_try_recover 命令共用的 {sessionId} 入参。
type ShellSessionIDPayload struct {
	SessionID string `json:"sessionId"`
}

// WriteToShellPayload 为 write_to_shell 命令入参。
type WriteToShellPayload struct {
	SessionID string `json:"sessionId"`
	Input     string `json:"input"`
}

// CloseShellPayload 为 close_local_shell 命令入参。
type CloseShellPayload struct {
	SessionID string `json:"sessionId"`
}

// JumpHostPayload 为跳板机（ProxyJump）配置。
type JumpHostPayload struct {
	AlgorithmPolicy string  `json:"algorithmPolicy"`
	Host            string  `json:"host"`
	Port            uint16  `json:"port"`
	Username        string  `json:"username"`
	AuthType        string  `json:"authType"`
	Password        *string `json:"password"`
	KeyFile         *string `json:"keyFile"`
	Passphrase      *string `json:"passphrase"`
}

// SocksProxyPayload 为 SOCKS5 出站代理配置：仅作用于第一跳 TCP 拨号
// （有跳板机时拨跳板机，否则拨目标）。字段为可选，不传即直连。
type SocksProxyPayload struct {
	Host     string  `json:"host"`
	Port     uint16  `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

// StartSshShellPayload 为 start_ssh_shell 命令入参。
type StartSshShellPayload struct {
	SessionID                string                   `json:"sessionId"`
	ProfileID                string                   `json:"profileId"`
	Host                     string                   `json:"host"`
	Port                     uint16                   `json:"port"`
	Username                 string                   `json:"username"`
	AttachIfExists           bool                     `json:"attachIfExists"`
	AuthType                 string                   `json:"authType"`
	Password                 *string                  `json:"password"`
	KeyFile                  *string                  `json:"keyFile"`
	Passphrase               *string                  `json:"passphrase"`
	TotpURI                  *string                  `json:"totpUri"`
	Cols                     *uint32                  `json:"cols"`
	Rows                     *uint32                  `json:"rows"`
	PortForwards             []PortForwardRulePayload `json:"portForwards"`
	JumpHost                 *JumpHostPayload         `json:"jumpHost"`
	SocksProxy               *SocksProxyPayload       `json:"socksProxy,omitempty"`
	AlgorithmPolicy          string                   `json:"algorithmPolicy"`
	KeepAliveIntervalSeconds uint32                   `json:"keepAliveIntervalSeconds"`
	KeepAliveMaxMisses       uint32                   `json:"keepAliveMaxMisses"`
	ReconnectEnabled         *bool                    `json:"reconnectEnabled"`
	ReconnectMaxAttempts     uint32                   `json:"reconnectMaxAttempts"`
	ReconnectBaseDelayMs     uint32                   `json:"reconnectBaseDelayMs"`
	ReconnectMaxDelayMs      uint32                   `json:"reconnectMaxDelayMs"`
	InitialDirectory         string                   `json:"initialDirectory"`
	PostConnectCommands      []string                 `json:"postConnectCommands"`
}

// TestSshConnectionPayload 为 test_ssh_connection 命令入参：仅探测 TCP+SSH 认证，
// 不打开 shell、不占用会话表。
type TestSshConnectionPayload struct {
	Host            string             `json:"host"`
	Port            uint16             `json:"port"`
	Username        string             `json:"username"`
	AuthType        string             `json:"authType"`
	Password        *string            `json:"password"`
	KeyFile         *string            `json:"keyFile"`
	Passphrase      *string            `json:"passphrase"`
	TotpURI         *string            `json:"totpUri"`
	JumpHost        *JumpHostPayload   `json:"jumpHost"`
	SocksProxy      *SocksProxyPayload `json:"socksProxy,omitempty"`
	AlgorithmPolicy string             `json:"algorithmPolicy"`
}

// DefaultSshAuthType 与 Rust 侧 default_ssh_auth_type() 对齐。
const DefaultSshAuthType = "password"

// BatchExecServer 为 ssh_batch_exec 命令入参中的单台服务器配置。
type BatchExecServer struct {
	ID              string             `json:"id"`
	Host            string             `json:"host"`
	Port            uint16             `json:"port"`
	Username        string             `json:"username"`
	AuthType        string             `json:"authType"`
	Password        *string            `json:"password"`
	KeyFile         *string            `json:"keyFile"`
	Passphrase      *string            `json:"passphrase"`
	TotpURI         *string            `json:"totpUri"`
	JumpHost        *JumpHostPayload   `json:"jumpHost"`
	SocksProxy      *SocksProxyPayload `json:"socksProxy,omitempty"`
	AlgorithmPolicy string             `json:"algorithmPolicy"`
}

// BatchExecResult 为 ssh_batch_exec 命令返回的单台服务器执行结果。
type BatchExecResult struct {
	ID         string  `json:"id"`
	Host       string  `json:"host"`
	Stdout     string  `json:"stdout"`
	Stderr     string  `json:"stderr"`
	ExitCode   *int    `json:"exitCode"`
	DurationMs uint64  `json:"durationMs"`
	Error      *string `json:"error"`
}

// RunShellCommandPayload 为 shell_run_command 命令入参：AI 一次性执行本地命令并
// 回收输出，不复用交互式 PTY 会话。
type RunShellCommandPayload struct {
	Command     string  `json:"command"`
	Cwd         *string `json:"cwd"`
	TimeoutMs   *uint32 `json:"timeoutMs"`
	OutputLimit *int    `json:"outputLimit"`
}

// RunShellCommandResult 为 shell_run_command 命令返回结果。
type RunShellCommandResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exitCode"`
	DurationMs      uint64 `json:"durationMs"`
	TimedOut        bool   `json:"timedOut"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
}
