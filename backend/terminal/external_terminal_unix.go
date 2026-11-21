//go:build !windows

package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// openExternalTerminal picks the terminal that runs a launch script
	// (platformExternalTerminal; tests replace it).
	openExternalTerminal = platformExternalTerminal
	// externalPIDTimeout bounds the wait for the program to start.
	externalPIDTimeout = 10 * time.Second
)

// externalAckTimeout bounds the wait for a reported program to open its ack FIFO.
var externalAckTimeout = 2 * time.Second

// externalBeforeAck runs after the host recorded the program's identity and
// before it lets the program run (tests check that nothing ran yet).
var externalBeforeAck func(pid int)

// startExternalDebuggee writes the launch script, opens the terminal on it
// and waits for the program's pid (see StartExternalDebuggee).
func startExternalDebuggee(ctx context.Context, spec ProfileShellSpec, title string) (*ExternalDebuggee, error) {
	SweepExternalLaunchDirs()
	dir, err := os.MkdirTemp("", externalDirPrefix) // 0700
	if err != nil {
		return nil, externalError(ExternalFailed, "create the launch directory: %w", err)
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, externalScriptName)
	report := filepath.Join(dir, externalReportDir)
	body, err := shLaunchScript(scriptLaunch{
		Script:  script,
		Report:  report,
		Title:   title,
		Cwd:     spec.Cwd,
		Program: spec.Executable,
		Args:    spec.Args,
		Env:     spec.Env,
		Unset:   spec.unsetEnv,
	})
	if err != nil {
		return nil, err
	}
	// Pick the terminal before the environment touches the disk.
	launch, err := openExternalTerminal(script)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(report, 0o700); err != nil {
		return nil, externalError(ExternalFailed, "create the report directory: %w", err)
	}
	if err := syscall.Mkfifo(filepath.Join(report, externalAckFile), 0o600); err != nil {
		return nil, externalError(ExternalFailed, "create the launch FIFO: %w", err)
	}
	if err := writePrivateFile(script, body); err != nil {
		return nil, externalError(ExternalFailed, "write the launch script: %w", err)
	}

	cmd := exec.Command(launch.Argv[0], launch.Argv[1:]...)
	stderr := &cappedBuffer{max: externalDetailMax}
	cmd.Stderr = stderr
	// Its own process group: signals to MinCode's group do not reach the window.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, externalError(ExternalFailed, "start %s: %w", launch.Name, err)
	}
	exited := make(chan error, 1)
	go func() { // reaps the terminal command whenever it ends (with its window, for some)
		err := cmd.Wait()
		if err != nil {
			err = fmt.Errorf("%s: %w: %s", launch.Name, err, strings.TrimSpace(stderr.String()))
		}
		exited <- err
	}()

	pid, err := waitExternalPID(ctx, report, externalPIDTimeout, exited)
	// The reported process waits on the ack FIFO before it execs the
	// program: its identity is recorded while it cannot have exited on its
	// own, so the pid cannot have been reused in between.
	identity, _ := processIdentity(pid)
	if err != nil {
		if pid > 0 { // reported as the wait ended: the program never runs
			go endUnixProcess(pid, identity, externalEndGrace)
		}
		return nil, err
	}
	if externalBeforeAck != nil {
		externalBeforeAck(pid)
	}
	if err := ackExternalLaunch(filepath.Join(report, externalAckFile), externalAckTimeout); err != nil {
		go endUnixProcess(pid, identity, externalEndGrace)
		return nil, externalError(ExternalFailed, "the launch script did not wait for the program to start: %w", err)
	}
	return &ExternalDebuggee{
		PID:      pid,
		Terminal: launch.Name,
		end:      func() { endUnixProcess(pid, identity, externalEndGrace) },
	}, nil
}

// ackExternalLaunch lets the reported process exec the program: it writes a
// line to the FIFO once the process has opened it for reading.
func ackExternalLaunch(fifo string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_, err = f.Write([]byte("\n"))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			return err
		}
		if !errors.Is(err, syscall.ENXIO) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// externalStaleAge is how old a launch directory must be to be swept: a live
// launch removes its directory within externalPIDTimeout.
const externalStaleAge = 10 * time.Minute

// SweepExternalLaunchDirs removes launch directories a crashed MinCode left
// in the temporary directory (a script written but never run still holds the
// debuggee's environment): only directories named with our prefix, owned by
// this user and older than externalStaleAge. It runs at startup and before
// each external launch.
func SweepExternalLaunchDirs() {
	tmp := os.TempDir()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	uid := os.Getuid()
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), externalDirPrefix) {
			continue
		}
		path := filepath.Join(tmp, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || time.Since(info.ModTime()) < externalStaleAge {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		_ = os.RemoveAll(path)
	}
}

// writePrivateFile creates path (which must not exist) readable and
// executable by the owner only.
func writePrivateFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// endUnixProcess sends SIGTERM to pid and SIGKILL after grace, but only while
// pid is still the process identity named (its start time): a program that
// ended, whose pid now belongs to another process, is left alone.
func endUnixProcess(pid int, identity string, grace time.Duration) {
	same := func() bool {
		id, alive := processIdentity(pid)
		return alive && identity != "" && id == identity
	}
	if !same() {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
		return
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !same() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if same() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// cappedBuffer keeps the first max bytes written to it.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
