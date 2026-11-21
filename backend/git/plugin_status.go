package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const pluginStatusTimeout = 5 * time.Second
const pluginStatusMaxBytes = 4 * 1024 * 1024

// Reserve cleanup time inside, not after, the sample's five-second deadline.
const pluginStatusCleanupReserve = 500 * time.Millisecond
const pluginStatusPipeWait = 100 * time.Millisecond

type pluginStatusProcess interface {
	terminate(deadline time.Time) error
	close()
}

// PluginStatusSnapshot deliberately contains no paths or command output. Digest
// describes allowed path/status tuples, not file contents, and stays host-only.
type PluginStatusSnapshot struct {
	Digest                                  string
	Staged, Unstaged, Untracked, Conflicted int
}

var errPluginStatusOverflow = errors.New("git status output limit exceeded")

// Unlike a truncating writer, crossing the limit signals the process owner to
// kill the entire child tree immediately. Memory never exceeds the hard bound.
type pluginStatusWriter struct {
	buffer   bytes.Buffer
	limit    int
	overflow chan struct{}
	once     sync.Once
}

func (w *pluginStatusWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.buffer.Len()
	if n <= remaining {
		return w.buffer.Write(p)
	}
	_, _ = w.buffer.Write(p[:remaining])
	w.once.Do(func() { close(w.overflow) })
	return 0, errPluginStatusOverflow
}

func runPluginStatusCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	return runPluginStatusCommandLimit(ctx, cmd, pluginStatusMaxBytes)
}

func runPluginStatusCommandLimit(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	total, cancelTotal := context.WithTimeout(ctx, pluginStatusTimeout)
	defer cancelTotal()
	deadline, _ := total.Deadline()
	work, cancelWork := context.WithDeadline(total, deadline.Add(-pluginStatusCleanupReserve))
	defer cancelWork()
	if err := work.Err(); err != nil {
		return nil, err
	}
	stdout := &pluginStatusWriter{limit: limit, overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	cmd.WaitDelay = pluginStatusPipeWait
	process, startErr := startPluginStatusProcess(work, cmd)
	if process == nil {
		return nil, errors.New("git status could not start")
	}
	defer process.close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	waited := false
	if startErr == nil {
		select {
		case runErr = <-done:
			waited = true
		case <-work.Done():
		case <-stdout.overflow:
		}
	}
	// Every outcome, including successful root exit and a done/overflow race,
	// owns tree termination. Root Wait/WaitDelay is not descendant cleanup.
	cleanupDeadline := time.Now().Add(pluginStatusCleanupReserve)
	if deadline.Before(cleanupDeadline) {
		cleanupDeadline = deadline
	}
	cleanupErr := process.terminate(cleanupDeadline)
	if !waited {
		remaining := time.Until(cleanupDeadline)
		if remaining < 0 {
			remaining = 0
		}
		timer := time.NewTimer(remaining)
		select {
		case runErr = <-done:
		case <-timer.C:
			cleanupErr = errors.New("git status process wait deadline exceeded")
		}
		timer.Stop()
	}
	if cleanupErr != nil {
		failure := errors.Join(errors.New("git status process tree cleanup failed"), cleanupErr)
		select {
		case <-stdout.overflow:
			return nil, errors.Join(errPluginStatusOverflow, failure)
		default:
		}
		return nil, failure
	}
	if startErr != nil {
		return nil, errors.New("git status process containment unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := work.Err(); err != nil {
		return nil, err
	}
	select {
	case <-stdout.overflow:
		return nil, errPluginStatusOverflow
	default:
	}
	if runErr != nil {
		return nil, errors.New("git status unavailable")
	}
	return stdout.buffer.Bytes(), nil
}

// The configuration probe, layout probe and status share one output allowance.
// No inherited Git variable can inject configuration, traces or executables.
type pluginStatusGitRunner struct{ remaining int }

func pluginStatusEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+6)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			out = append(out, entry)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1")
}

func (runner *pluginStatusGitRunner) run(ctx context.Context, root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"--no-pager", "--no-replace-objects"}, stagedSafeConfig...)
	commandArgs = append(commandArgs, "-c", "core.attributesFile="+os.DevNull)
	cmd, err := newGitCommand(append(commandArgs, args...)...)
	if err != nil {
		return nil, errors.New("git status unavailable")
	}
	cmd.Dir, cmd.Env = root, pluginStatusEnvironment(cmd.Env)
	raw, err := runPluginStatusCommandLimit(ctx, cmd, runner.remaining)
	runner.remaining -= len(raw)
	return raw, err
}

// Git status may run clean/process filters while refreshing index stat data.
// Enumerate effective keys without values or a shell, then override every
// driver's commands and required flag. Keys (including unusual subsections)
// are single argv values, never executable text. Excessive configuration is
// unknown instead of building an unbounded/Windows-oversized command line.
func pluginStatusFilterOverrides(keys []byte) ([]string, error) {
	drivers := map[string]struct{}{}
	for _, key := range strings.Split(string(keys), "\x00") {
		if !strings.HasPrefix(key, "filter.") {
			continue
		}
		rest := strings.TrimPrefix(key, "filter.")
		last := strings.LastIndexByte(rest, '.')
		if last >= 0 {
			drivers[rest[:last]] = struct{}{}
		}
	}
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	bytes := 0
	for _, name := range names {
		for _, setting := range []string{"clean=", "process=", "smudge=", "required=false"} {
			value := "filter." + name + "." + setting
			bytes += len(value) + 4
			if bytes > 16*1024 {
				return nil, errors.New("git status filter configuration limit exceeded")
			}
			args = append(args, "-c", value)
		}
	}
	return args, nil
}

// SamplePluginStatus shares one five-second deadline across repository checking
// and status. Non-repository, timeout and overflow are unknown (error), never an
// empty/clean snapshot. No inherited GIT_* redirect, hook or remote fetch runs.
func SamplePluginStatus(ctx context.Context, root string, allow func(string) bool) (PluginStatusSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginStatusTimeout)
	defer cancel()
	if root == "" || !filepath.IsAbs(root) || allow == nil {
		return PluginStatusSnapshot{}, errors.New("git status scope unavailable")
	}
	runner := &pluginStatusGitRunner{remaining: pluginStatusMaxBytes}
	layout, err := runner.run(ctx, root, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return PluginStatusSnapshot{}, err
	}
	dirs := strings.Split(strings.TrimSuffix(string(layout), "\n"), "\n")
	if len(dirs) != 3 || resolveRepoDir(dirs[0]) != root {
		return PluginStatusSnapshot{}, errors.New("git status requires the project repository root")
	}
	// A linked worktree with metadata outside this project is not a reason to
	// widen workspace authority. Stay unknown instead of inspecting another root.
	for _, dir := range dirs[1:] {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		relative, relErr := filepath.Rel(root, resolveRepoDir(dir))
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return PluginStatusSnapshot{}, errors.New("git status repository outside project")
		}
	}
	keys, err := runner.run(ctx, root, "config", "--includes", "--null", "--name-only", "--list")
	if err != nil {
		return PluginStatusSnapshot{}, err
	}
	args, err := pluginStatusFilterOverrides(keys)
	if err != nil {
		return PluginStatusSnapshot{}, err
	}
	args = append(args, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=all")
	raw, err := runner.run(ctx, root, args...)
	if err != nil {
		return PluginStatusSnapshot{}, err
	}
	snapshot := pluginStatusDigest(raw, func(relative string) bool {
		return ctx.Err() == nil && allow(relative)
	})
	if err := ctx.Err(); err != nil {
		return PluginStatusSnapshot{}, err
	}
	return snapshot, nil
}

func pluginStatusDigest(raw []byte, allow func(string) bool) PluginStatusSnapshot {
	status := parsePorcelainV2(string(raw))
	var snapshot PluginStatusSnapshot
	tuples := make([]string, 0)
	add := func(group string, entries []gitFileEntry, count *int) {
		for _, entry := range entries {
			if !allow(entry.Path) || (entry.OldPath != nil && !allow(*entry.OldPath)) {
				continue
			}
			old := ""
			if entry.OldPath != nil {
				old = *entry.OldPath
			}
			tuples = append(tuples, group+"\x00"+entry.Status+"\x00"+entry.xy+"\x00"+entry.Path+"\x00"+old+"\x00")
			if entry.Status == "conflicted" {
				snapshot.Conflicted++
			} else {
				*count++
			}
		}
	}
	add("staged", status.Staged, &snapshot.Staged)
	add("unstaged", status.Unstaged, &snapshot.Unstaged)
	add("untracked", status.Untracked, &snapshot.Untracked)
	sort.Strings(tuples)
	hash := sha256.New()
	for _, tuple := range tuples {
		_, _ = io.WriteString(hash, tuple)
	}
	snapshot.Digest = hex.EncodeToString(hash.Sum(nil))
	return snapshot
}
