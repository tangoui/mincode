package debug

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const (
	defaultConnectTimeout = 10 * time.Second
	defaultStderrMaxBytes = 64 << 10
	// stderrExcerptBytes caps the stderr tail quoted in errors.
	stderrExcerptBytes = 2 << 10
	dialRetryInterval  = 100 * time.Millisecond
	// adapterExitWait bounds how long a killed adapter may take to be reaped.
	adapterExitWait = 5 * time.Second
	// adapterExitGrace is how long an exited adapter's connection may stay
	// open before the session is closed anyway.
	adapterExitGrace = 2 * time.Second
	// eofExitWait is how long a connection read error waits for the adapter
	// to exit so the close reason can carry its exit status and stderr.
	eofExitWait = time.Second
)

// AdapterSpec describes how to launch one debug adapter process.
type AdapterSpec struct {
	// Kind is "stdio" (DAP over stdin/stdout) or "tcp" (the adapter listens
	// on a port MinCode chooses; every literal "${port}" in Args is replaced).
	Kind       string
	Executable string
	Args       []string
	// Env overrides entries of the expanded login environment.
	Env map[string]string
	// Cwd is the adapter's working directory; empty means ProjectRoot.
	Cwd string
	// BeforeExec runs just before the process starts; an error aborts the
	// start (e.g. the user refused permission).
	BeforeExec func() error
	// Valid is polled every 2 s while the session lives; false stops it
	// gracefully (disconnect, then kill) with reason "plugin changed".
	Valid func() bool
	// StderrMaxBytes caps the kept stderr tail (0 = 64 KiB); MaxMessageBytes
	// caps one DAP frame (0 = 4 MiB).
	StderrMaxBytes, MaxMessageBytes int
	// ConnectTimeout bounds the tcp connect retries (0 = 10 s).
	ConnectTimeout time.Duration

	OwnerPluginID, DebuggerType, ProjectRoot string
	// MaxSessionsPerOwner limits live sessions per OwnerPluginID (0 = no limit).
	MaxSessionsPerOwner int
}

// StartAdapter launches the adapter described by spec, connects to it and
// registers the resulting Session (see Lookup, List). The session is not
// initialized yet: call Begin. ctx bounds only the start, not the session.
// On failure no process is left running and nothing is registered.
func StartAdapter(ctx context.Context, spec AdapterSpec) (*Session, error) {
	if spec.Kind != "stdio" && spec.Kind != "tcp" {
		return nil, fmt.Errorf("start debug adapter: unsupported kind %q", spec.Kind)
	}
	if spec.Executable == "" {
		return nil, errors.New("start debug adapter: executable is empty")
	}
	if err := reserveSlot(spec.OwnerPluginID, spec.MaxSessionsPerOwner); err != nil {
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}
	defer releaseSlot(spec.OwnerPluginID)

	if spec.BeforeExec != nil {
		if err := spec.BeforeExec(); err != nil {
			return nil, fmt.Errorf("start debug adapter: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}

	var (
		proc *adapterProcess
		conn *adapterConn
	)
	if spec.Kind == "stdio" {
		proc, conn, err = startStdio(spec)
	} else {
		proc, conn, err = startTCP(ctx, spec)
	}
	if err != nil {
		return nil, fmt.Errorf("start debug adapter %s: %w", spec.Executable, err)
	}
	s := register(id, spec, proc, conn)
	// Shutdown sets the flag before it collects sessions, so a session
	// registered after that collection is seen here and closed.
	if isShuttingDown() {
		s.Close(shutdownReason)
		return nil, fmt.Errorf("start debug adapter: %w", errShuttingDown)
	}
	return s, nil
}

// ConnectSpec describes a debug adapter that is already listening on a
// loopback port and that MinCode did not start (java-debug inside the
// built-in jdtls hands out such a port).
type ConnectSpec struct {
	// Port is the adapter's port on 127.0.0.1 (1–65535).
	Port                                     int
	OwnerPluginID, DebuggerType, ProjectRoot string
	// Valid is polled like AdapterSpec.Valid; false stops the session
	// gracefully (disconnect, then close the connection).
	Valid func() bool
	// MaxMessageBytes caps one DAP frame (0 = 4 MiB).
	MaxMessageBytes int
	// ConnectTimeout bounds the connect retries (0 = 10 s).
	ConnectTimeout time.Duration
	// MaxSessionsPerOwner limits live sessions per OwnerPluginID (0 = no limit).
	MaxSessionsPerOwner int
}

// ConnectAdapter connects to the adapter listening on 127.0.0.1:spec.Port
// and registers the resulting Session (see StartAdapter). No process is
// started, so none is killed: ending the session (after `disconnect` on a
// graceful stop) only closes the connection, and the adapter's owner (jdtls)
// manages the debuggee. ctx bounds only the connect.
func ConnectAdapter(ctx context.Context, spec ConnectSpec) (*Session, error) {
	if spec.Port < 1 || spec.Port > 65535 {
		return nil, fmt.Errorf("connect debug adapter: port %d is out of range", spec.Port)
	}
	if err := reserveSlot(spec.OwnerPluginID, spec.MaxSessionsPerOwner); err != nil {
		return nil, fmt.Errorf("connect debug adapter: %w", err)
	}
	defer releaseSlot(spec.OwnerPluginID)
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("connect debug adapter: %w", err)
	}
	c, err := dialLoopback(ctx, spec.Port, spec.ConnectTimeout, nil)
	if err != nil {
		return nil, fmt.Errorf("connect debug adapter: %w", err)
	}
	s := register(id, AdapterSpec{
		Kind: "connect", Valid: spec.Valid, MaxMessageBytes: spec.MaxMessageBytes,
		OwnerPluginID: spec.OwnerPluginID, DebuggerType: spec.DebuggerType, ProjectRoot: spec.ProjectRoot,
	}, nil, newAdapterConn(c, c, nil, c))
	if isShuttingDown() {
		s.Close(shutdownReason)
		return nil, fmt.Errorf("connect debug adapter: %w", errShuttingDown)
	}
	return s, nil
}

func newSessionID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return "dbg-" + hex.EncodeToString(b[:]), nil
}

// adapterProcess is a started adapter and the goroutine that reaps it.
type adapterProcess struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	exited chan struct{} // closed after cmd.Wait returns
	err    error         // cmd.Wait result; read only after exited
}

// newCommand prepares the adapter command; stdout/stdin are set by the caller.
func newCommand(spec AdapterSpec, args []string) (*exec.Cmd, *tailBuffer) {
	cmd := exec.Command(spec.Executable, args...)
	cmd.Dir = spec.Cwd
	if cmd.Dir == "" {
		cmd.Dir = spec.ProjectRoot
	}
	cmd.Env = core.MergeEnv(core.ExpandedEnv(), spec.Env)
	core.PrepareProcessTree(cmd)
	max := spec.StderrMaxBytes
	if max <= 0 {
		max = defaultStderrMaxBytes
	}
	stderr := &tailBuffer{max: max}
	cmd.Stderr = stderr
	// Grandchildren holding stderr must not block Wait forever.
	cmd.WaitDelay = 2 * time.Second
	return cmd, stderr
}

// startProcess starts cmd and the reaper goroutine.
func startProcess(cmd *exec.Cmd, stderr *tailBuffer) (*adapterProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if afterAdapterStart != nil {
		afterAdapterStart(cmd)
	}
	p := &adapterProcess{cmd: cmd, stderr: stderr, exited: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// kill terminates the process tree and waits (bounded) until the adapter is
// reaped. The group is signalled even when the adapter itself has already
// exited, so descendants it left behind die too. On Unix that works because
// the process group outlives its reaped leader while members remain. On
// Windows core.KillProcessTree does nothing once the adapter has exited
// (taskkill /T cannot find orphaned descendants), so such orphans survive:
// a known limitation.
func (p *adapterProcess) kill() {
	if p == nil {
		return // connected session: MinCode started no process
	}
	_ = core.KillProcessTree(p.cmd)
	select {
	case <-p.exited:
	case <-time.After(adapterExitWait):
	}
}

// exitReason describes an exited process: "debug adapter exited<when>
// (<status>): <stderr tail>". Call only after exited is closed.
func (p *adapterProcess) exitReason(when string) string {
	if p == nil {
		return "debug adapter closed the connection" + when
	}
	status := ""
	if p.err != nil {
		status = " (" + p.err.Error() + ")"
	}
	return "debug adapter exited" + when + status + p.stderrSuffix()
}

// stderrSuffix returns ": <last ≤ 2 KiB of stderr>" or "".
func (p *adapterProcess) stderrSuffix() string {
	if p == nil {
		return ""
	}
	if tail := p.stderr.tail(stderrExcerptBytes); tail != "" {
		return ": " + tail
	}
	return ""
}

func startStdio(spec AdapterSpec) (*adapterProcess, *adapterConn, error) {
	cmd, stderr := newCommand(spec, spec.Args)
	// os.Pipe instead of cmd.StdoutPipe: Wait (run by the reaper) must not
	// close our ends while the session is still reading buffered output.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stdin, cmd.Stdout = stdinR, stdoutW
	proc, err := startProcess(cmd, stderr)
	// The child holds its own copies now (or never started).
	_ = stdinR.Close()
	_ = stdoutW.Close()
	if err != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		return nil, nil, err
	}
	return proc, newAdapterConn(stdoutR, stdinW, proc, stdinW, stdoutR), nil
}

func startTCP(ctx context.Context, spec AdapterSpec) (*adapterProcess, *adapterConn, error) {
	port, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	args := make([]string, len(spec.Args))
	for i, a := range spec.Args {
		args[i] = strings.ReplaceAll(a, "${port}", strconv.Itoa(port))
	}
	cmd, stderr := newCommand(spec, args)
	cmd.Stdout = stderr // adapter logs, quoted on failure
	proc, err := startProcess(cmd, stderr)
	if err != nil {
		return nil, nil, err
	}

	c, err := dialLoopback(ctx, port, spec.ConnectTimeout, proc)
	if err != nil {
		proc.kill() // descendants may still hold the port or run on
		return nil, nil, err
	}
	return proc, newAdapterConn(c, c, proc, c), nil
}

// dialLoopback connects to 127.0.0.1:port, retrying until timeout (0 = 10 s),
// ctx is done or, when proc is not nil, the adapter process exits.
func dialLoopback(ctx context.Context, port int, timeout time.Duration, proc *adapterProcess) (net.Conn, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	var exited <-chan struct{}
	if proc != nil {
		exited = proc.exited
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	retry := time.NewTicker(dialRetryInterval)
	defer retry.Stop()
	var dialer net.Dialer
	for {
		c, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			return c, nil
		}
		select {
		case <-retry.C:
		case <-exited:
			return nil, errors.New(proc.exitReason(" before accepting a connection"))
		case <-deadline.C:
			return nil, fmt.Errorf("debug adapter did not accept a connection on %s within %v%s", addr, timeout, proc.stderrSuffix())
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// freePort asks the OS for an unused loopback port and releases it for the
// adapter to listen on.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("pick port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, fmt.Errorf("pick port: %w", err)
	}
	return port, nil
}

// adapterConn is the session's connection to an adapter process. When a read
// fails because the adapter went away, it waits briefly for the process to
// exit and reports its exit status and stderr tail instead of a bare EOF.
type adapterConn struct {
	r       io.Reader
	w       io.Writer
	proc    *adapterProcess
	closers []io.Closer

	closeOnce sync.Once
	closed    chan struct{}
}

func newAdapterConn(r io.Reader, w io.Writer, proc *adapterProcess, closers ...io.Closer) *adapterConn {
	return &adapterConn{r: r, w: w, proc: proc, closers: closers, closed: make(chan struct{})}
}

func (c *adapterConn) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if err == nil || c.proc == nil {
		return n, err
	}
	select {
	case <-c.closed:
		return n, err // closed by us: the original error is fine
	case <-c.proc.exited:
	case <-time.After(eofExitWait):
		return n, err
	}
	return n, errors.New(c.proc.exitReason(""))
}

func (c *adapterConn) Write(b []byte) (int, error) { return c.w.Write(b) }

func (c *adapterConn) Close() error {
	var first error
	c.closeOnce.Do(func() {
		close(c.closed)
		for _, cl := range c.closers {
			if err := cl.Close(); err != nil && first == nil {
				first = err
			}
		}
	})
	return first
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, b...)
	if len(t.data) > t.max {
		t.data = append([]byte(nil), t.data[len(t.data)-t.max:]...)
	}
	return len(b), nil
}

// tail returns at most n trailing bytes, trimmed, starting on a rune boundary.
func (t *tailBuffer) tail(n int) string {
	t.mu.Lock()
	data := t.data
	if len(data) > n {
		data = data[len(data)-n:]
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
	}
	s := string(data)
	t.mu.Unlock()
	return strings.TrimSpace(s)
}
