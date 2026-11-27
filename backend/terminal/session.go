package terminal

import (
	"context"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshLinkState 与 Rust 侧 SshLinkState 对齐：Grace Period 内保留底层 TCP+SSH 连接。
type sshLinkState int

const (
	sshLinkActive sshLinkState = iota
	sshLinkGracePeriod
)

// sshRecoveryMeta 保存重连恢复 shell 时所需的终端尺寸。
type sshRecoveryMeta struct {
	cols uint16
	rows uint16
}

// portForwardHandle 为运行中的单条端口转发任务句柄；调用 cancel 即取消任务。
type portForwardHandle struct {
	ruleID string
	cancel func()
}

// remoteForwardTarget 为 -R 规则的转发目标（bindPort -> target）。
type remoteForwardTarget struct {
	targetHost string
	targetPort int
}

// shellProcess 对齐 Rust 侧 ShellProcess：本地 PTY 或 SSH shell 的运行时状态。
type shellProcess struct {
	control        *controlledPane
	mu             sync.Mutex
	inputMu        sync.Mutex
	sshReconnectMu sync.Mutex
	portForwardMu  sync.Mutex

	// 本地 PTY
	localPTY localPTYHandle // 平台相关实现（unix/windows）；本地 shell 时非 nil
	// output keeps a debuggee session's output for attach_local_shell; nil
	// for every other session. Set before publishing, never changed.
	output *outputLog

	// 通用
	stdinTx   chan string   // 向 shell 写入数据的 channel；nil 表示未就绪/GracePeriod
	stdinDone chan struct{} // 本地 PTY 输入 writer 的退出信号；不关闭 stdinTx 以避免并发发送 panic

	// 派生会话（如 docker exec / docker logs -f）：复用父会话的 SSH 连接，命令结束即终止。
	// 零值即父会话语义（独占连接、断开后进入 GracePeriod 重连），新增会话需显式声明。
	sharedClient bool // true 表示 sshClient 归父会话所有，关闭本会话时不得 Close
	ephemeral    bool // true 表示命令正常退出即结束会话，不进入 GracePeriod 重连

	// SSH
	sshHistoryProfileID     string
	sshHistoryHost          string
	sshHistoryUser          string
	sshHistoryPort          uint16
	historyCapture          *sshInputCapture // 终端输入行还原状态机；仅 SSH 主会话使用
	sshTaskCancel           func()           // 取消当前 relay goroutine
	sshClient               *sshClientHandle // 共享的 SSH 连接句柄
	jumpClient              *sshClientHandle // 跳板机连接（需保活直至会话结束）
	portForwards            []portForwardHandle
	portForwardFailures     map[string]portForwardFailure
	remoteForwardTargets    *sync.Map // uint32(bindPort) -> remoteForwardTarget（-R 用）
	sshLinkState            sshLinkState
	sshRecoveryMeta         *sshRecoveryMeta
	sshResizeTx             chan [2]uint16 // [cols, rows]
	graceToken              uint64
	sshRedial               func() (*ssh.Client, *ssh.Client, error)
	sshKeepAliveCancel      context.CancelFunc
	sshKeepAliveInterval    uint32
	sshKeepAliveMaxMisses   uint32
	portForwardRules        []PortForwardRulePayload
	sshReconnectAttempts    uint32
	sshReconnectMaxAttempts uint32
	sshReconnectBaseDelay   time.Duration
	sshReconnectMaxDelay    time.Duration
	sshInitialDirectory     string
	sshPostConnectCommands  []string
	sshPostConnectPending   bool
}

// sshClientHandle 包装 *ssh.Client，供多个 goroutine（relay、端口转发）共享。
// x/crypto/ssh.Client 本身对并发调用是安全的（内部有锁），这里仅需持有引用。
type sshClientHandle struct {
	client *ssh.Client
}

// sessionStore 是会话表，对齐 Rust 侧 ShellSessions = Arc<Mutex<HashMap<String, ShellProcess>>>。
// starting 按 session ID 协调启动流程；外部 I/O 始终在锁外执行。
type sessionStore struct {
	mu       sync.Mutex
	procs    map[string]*shellProcess
	starting map[string]chan struct{}
	// ended keeps the output of debuggee sessions whose process exited or
	// was ended by EndDebuggeeShell, for a late attach_local_shell, until
	// CloseLocalShell, a new start or eviction: at most endedOutputsMax are
	// kept, the oldest (endedOrder) goes first.
	ended      map[string]*outputLog
	endedOrder []string
}

// endedOutputsMax bounds the kept output of ended debuggee sessions
// (debuggeeOutputLogMaxBytes each).
const endedOutputsMax = 16

var sessions = &sessionStore{
	procs:    map[string]*shellProcess{},
	starting: map[string]chan struct{}{},
	ended:    map[string]*outputLog{},
}

// keepEndedLocked keeps log as the output of the ended session id, evicting
// the oldest kept outputs beyond endedOutputsMax.
func (s *sessionStore) keepEndedLocked(id string, log *outputLog) {
	s.forgetEndedLocked(id)
	s.ended[id] = log
	s.endedOrder = append(s.endedOrder, id)
	for len(s.endedOrder) > endedOutputsMax {
		delete(s.ended, s.endedOrder[0])
		s.endedOrder = s.endedOrder[1:]
	}
}

func (s *sessionStore) forgetEndedLocked(id string) {
	if _, ok := s.ended[id]; !ok {
		return
	}
	delete(s.ended, id)
	for i, kept := range s.endedOrder {
		if kept == id {
			s.endedOrder = append(s.endedOrder[:i:i], s.endedOrder[i+1:]...)
			break
		}
	}
}

func (s *sessionStore) get(id string) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	return p, ok
}

func (s *sessionStore) put(id string, p *shellProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs[id] = p
}

func (s *sessionStore) remove(id string) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	delete(s.procs, id)
	return p, ok
}

func (s *sessionStore) drain() []*shellProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	procs := make([]*shellProcess, 0, len(s.procs))
	for id, p := range s.procs {
		procs = append(procs, p)
		delete(s.procs, id)
	}
	for id := range s.starting {
		s.finishStartLocked(id)
	}
	clear(s.ended)
	s.endedOrder = nil
	return procs
}

// beginStart 取得指定 session 的启动权；attach 请求等待既有启动完成后复查会话。
func (s *sessionStore) beginStart(id string, attach bool) (wait <-chan struct{}, owner bool, existing *shellProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if done, ok := s.starting[id]; ok {
		return done, false, nil
	}
	if attach {
		if p, ok := s.procs[id]; ok {
			return nil, false, p
		}
	}
	done := make(chan struct{})
	s.starting[id] = done
	return nil, true, nil
}

// publishStart 原子替换会话，但保持启动门关闭；返回的旧会话必须在锁外关闭。
func (s *sessionStore) publishStart(id string, p *shellProcess) (old *shellProcess) {
	s.mu.Lock()
	old = s.procs[id]
	s.procs[id] = p
	s.forgetEndedLocked(id)
	s.mu.Unlock()
	return old
}

// finishStart 在会话完全可用后唤醒同 ID 等待者。
func (s *sessionStore) finishStart(id string) {
	s.mu.Lock()
	s.finishStartLocked(id)
	s.mu.Unlock()
}

// cancelStart 只释放启动权，不影响可能仍可用的旧会话。
func (s *sessionStore) cancelStart(id string) {
	s.mu.Lock()
	s.finishStartLocked(id)
	s.mu.Unlock()
}

func (s *sessionStore) finishStartLocked(id string) {
	if done, ok := s.starting[id]; ok {
		delete(s.starting, id)
		close(done)
	}
}

// removeIfSame 仅在会话表仍指向目标实例时移除，避免旧 goroutine 清理新会话。
func (s *sessionStore) removeIfSame(id string, target *shellProcess) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.procs[id]
	if !ok || current != target {
		return nil, false
	}
	delete(s.procs, id)
	return current, true
}

// retireIfSame is removeIfSame for a session whose process ended by itself:
// a debuggee session's output stays attachable (see ended).
func (s *sessionStore) retireIfSame(id string, target *shellProcess) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.procs[id]
	if !ok || current != target {
		return nil, false
	}
	delete(s.procs, id)
	if current.output != nil {
		s.keepEndedLocked(id, current.output)
	}
	return current, true
}

// removeKeepingOutput removes a live session and, when it keeps output (a
// debuggee session), keeps that output attachable like an exited one.
func (s *sessionStore) removeKeepingOutput(id string) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	if !ok {
		return nil, false
	}
	delete(s.procs, id)
	if p.output != nil {
		s.keepEndedLocked(id, p.output)
	}
	return p, true
}

// removeWithOutput removes the session and any output kept after its exit.
func (s *sessionStore) removeWithOutput(id string) (*shellProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetEndedLocked(id)
	p, ok := s.procs[id]
	delete(s.procs, id)
	return p, ok
}

// lookupWithOutput returns the live session, or else the output an exited
// debuggee session kept (nil if none).
func (s *sessionStore) lookupWithOutput(id string) (proc *shellProcess, live bool, ended *outputLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.procs[id]; ok {
		return p, true, nil
	}
	return nil, false, s.ended[id]
}

// mfaChannels：keyboard-interactive MFA 等待表，每个 session_id 对应一个
// 一次性 channel，由 respond_ssh_mfa 命令触发。对齐 Rust 侧 SshMfaChannels。
type mfaChannelStore struct {
	mu  sync.Mutex
	chs map[string]chan []string
}

var mfaChannels = &mfaChannelStore{chs: map[string]chan []string{}}

func (m *mfaChannelStore) register(sessionID string) chan []string {
	ch := make(chan []string, 1)
	m.mu.Lock()
	m.chs[sessionID] = ch
	m.mu.Unlock()
	return ch
}

func (m *mfaChannelStore) unregister(sessionID string) {
	m.mu.Lock()
	delete(m.chs, sessionID)
	m.mu.Unlock()
}

func (m *mfaChannelStore) respond(sessionID string, responses []string) bool {
	m.mu.Lock()
	ch, ok := m.chs[sessionID]
	if ok {
		delete(m.chs, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	ch <- responses
	return true
}

// shutdownShellProcess 清理会话的全部资源：端口转发、SSH/PTY 句柄、goroutine。
// 对齐 Rust 侧 shutdown_shell_process。
func shutdownShellProcess(p *shellProcess) {
	p.portForwardMu.Lock()
	defer p.portForwardMu.Unlock()
	p.mu.Lock()
	for _, h := range p.portForwards {
		if h.cancel != nil {
			h.cancel()
		}
	}
	p.portForwards = nil
	p.portForwardFailures = nil
	if p.stdinDone != nil {
		close(p.stdinDone)
		p.stdinDone = nil
	}
	if p.sshKeepAliveCancel != nil {
		p.sshKeepAliveCancel()
		p.sshKeepAliveCancel = nil
	}
	// 派生会话共享父会话的连接，关闭它不得波及父会话的 SSH 终端。
	if !p.sharedClient {
		if p.sshClient != nil && p.sshClient.client != nil {
			_ = p.sshClient.client.Close()
		}
		if p.jumpClient != nil && p.jumpClient.client != nil {
			_ = p.jumpClient.client.Close()
		}
	}
	p.sshClient = nil
	p.jumpClient = nil
	p.remoteForwardTargets = nil
	if p.sshTaskCancel != nil {
		p.sshTaskCancel()
		p.sshTaskCancel = nil
	}
	p.stdinTx = nil
	if p.localPTY != nil {
		_ = p.localPTY.Close()
		p.localPTY = nil
	}
	p.mu.Unlock()
}

// sessionEnds tracks closed local sessions whose processes are still being
// ended (see unixPTY.Close).
var sessionEnds sync.WaitGroup

// Shutdown 关闭全部终端会话；可重复调用。Waits (until ctx ends) for the
// processes of the closed sessions to be ended, so quitting the app does not
// leave them running.
func Shutdown(ctx context.Context) {
	attention.reset()
	for _, p := range sessions.drain() {
		shutdownShellProcess(p)
	}
	done := make(chan struct{})
	go func() {
		sessionEnds.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
