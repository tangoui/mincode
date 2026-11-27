package terminal

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/agentcontrol"
	"mincode/app/backend/core"
	"mincode/app/backend/internal/textpolicy"
	"mincode/app/backend/terminalactivity"
)

type PaneInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	CWD   string `json:"cwd"`
	Alive bool   `json:"alive"`
	State string `json:"state"`
	Agent string `json:"agent,omitempty"`
}

type controlledPane struct {
	mu                              sync.Mutex
	info                            PaneInfo
	ring                            outputRing
	changed                         chan struct{}
	lastInput, lastOutput, lastHook time.Time
	hooked                          bool
	turn                            uint64
	stopped                         uint64
	stoppedState                    string
	pty                             localPTYHandle
}

var paneControls = struct {
	sync.RWMutex
	panes    map[string]*controlledPane
	metadata map[string]PaneInfo
}{panes: make(map[string]*controlledPane), metadata: make(map[string]PaneInfo)}

// SetControlCLIPath is called on desktop startup before launching any shells.
func SetControlCLIPath(path string) { controlCLIDir = path }

var controlCLIDir string

func paneEnvironment(env []string, id, helperDir string, local bool) ([]string, string) {
	var clean []string
	var path string
	for _, pair := range env {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		switch key {
		case "MINCODE_PANE_ID", "MINCODE_CONTROL_URL", "MINCODE_CONTROL_TOKEN":
			continue
		case "PATH":
			if local && helperDir != "" {
				path = value
				continue
			}
		}
		clean = append(clean, pair)
	}
	if !local {
		return clean, ""
	}
	if helperDir != "" {
		if path != "" {
			path = string(os.PathListSeparator) + path
		}
		clean = append(clean, "PATH="+helperDir+path)
	}
	url, token := agentcontrol.IssuePaneToken(id)
	if token != "" {
		clean = append(clean, "MINCODE_PANE_ID="+id, "MINCODE_CONTROL_URL="+url, "MINCODE_CONTROL_TOKEN="+token)
	}
	return clean, token
}

func addControlledPane(id, title, cwd string, claude bool, pty localPTYHandle) *controlledPane {
	p := &controlledPane{
		info:    PaneInfo{ID: id, Title: title, CWD: cwd, Alive: true, State: "idle"},
		ring:    newOutputRing(controlOutputBytes, controlOutputLines),
		changed: make(chan struct{}), hooked: claude, pty: pty,
	}
	if claude {
		p.info.Agent = "claude"
	}
	paneControls.Lock()
	defer paneControls.Unlock()
	if info, ok := paneControls.metadata[id]; ok {
		delete(paneControls.metadata, id)
		p.applyMetadata(info)
	}
	if p.info.Title == "" {
		p.info.Title = id
	}
	if old := paneControls.panes[id]; old != nil {
		old.end()
	}
	paneControls.panes[id] = p
	return p
}

func lookupControlledPane(id string) *controlledPane {
	paneControls.RLock()
	defer paneControls.RUnlock()
	return paneControls.panes[id]
}

func removeControlledPane(id string) {
	paneControls.Lock()
	defer paneControls.Unlock()
	if p := paneControls.panes[id]; p != nil {
		p.end()
		delete(paneControls.panes, id)
	}
	delete(paneControls.metadata, id)
	agentcontrol.RevokePane(id)
}

func (p *controlledPane) applyMetadata(info PaneInfo) {
	if info.Title != "" {
		p.info.Title = info.Title
	}
	if info.CWD != "" {
		p.info.CWD = info.CWD
	}
	if info.Agent == "claude" || info.Agent == "codex" {
		p.info.Agent = info.Agent
	}
}

func (p *controlledPane) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *controlledPane) stop(state string) {
	if p.info.State == state {
		return
	}
	p.info.State = state
	p.stopped, p.stoppedState = p.turn, state
	p.notify()
}

func (p *controlledPane) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.info.Alive = false
	if handle, ok := p.pty.(interface{ paneToken() string }); ok {
		agentcontrol.RevokePaneToken(p.info.ID, handle.paneToken())
	}
	p.pty = nil
	p.stop("exited")
}

func (p *controlledPane) observeInput(input string) {
	if p == nil || !strings.ContainsAny(input, "\r\n") || isTerminalReport(input) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.info.Alive {
		return
	}
	if fields := strings.Fields(input); len(fields) != 0 && (fields[0] == "claude" || fields[0] == "codex") {
		p.info.Agent = fields[0]
	}
	p.turn++
	p.lastInput = time.Now()
	p.lastOutput = time.Time{}
	p.info.State = "working"
	p.notify()
}

func (p *controlledPane) observeOutput(data string, now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ring.write(data)
	if p.info.State == "working" && !p.hooked {
		first := p.lastOutput.IsZero()
		p.lastOutput = now
		if first {
			p.notify()
		}
	}
}

func (p *controlledPane) quietDelay(now time.Time) time.Duration {
	if p.info.State != "working" || p.hooked || p.lastOutput.IsZero() {
		return -1
	}
	delay := attentionQuietAfter - now.Sub(p.lastOutput)
	if delay <= 0 {
		p.stop("idle")
		return -1
	}
	return delay
}

func observeControlAttention(id string, now time.Time) {
	if p := lookupControlledPane(id); p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.info.State == "working" && !p.hooked && !now.Before(p.lastInput) {
			p.stop("idle")
		}
	}
}

func observeClaudeActivity(id, phase, event string, when time.Time) {
	p := lookupControlledPane(id)
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.info.Alive || when.Before(p.lastInput) || when.Before(p.lastHook) {
		return
	}
	p.lastHook = when
	p.hooked = true
	p.info.Agent = "claude"
	switch {
	case event == "SessionEnd":
		p.hooked = false
		p.stop("idle")
	case event == "Stop" || event == "StopFailure":
		p.stop("idle")
	case phase == "waiting_approval" || phase == "waiting_input":
		p.stop("waiting_input")
	case phase == "active":
		if p.turn == 0 || p.info.State != "working" && p.stopped == p.turn {
			p.turn++
		}
		p.info.State = "working"
		p.notify()
	}
}

func ControlSend(paneID, text string, submit bool) error {
	if lookupControlledPane(paneID) == nil {
		return agentcontrol.ErrPaneNotFound
	}
	if submit {
		text += "\r"
	}
	return writeToShell(WriteToShellPayload{SessionID: paneID, Input: text})
}

func ControlRead(paneID string, lines int) (string, error) {
	p := lookupControlledPane(paneID)
	if p == nil {
		return "", agentcontrol.ErrPaneNotFound
	}
	p.mu.Lock()
	raw := p.ring.read(0)
	p.mu.Unlock()
	return tailOutputLines(textpolicy.RedactCredentials(raw), lines), nil
}

func ControlWait(ctx context.Context, paneID string) (string, error) {
	p := lookupControlledPane(paneID)
	if p == nil {
		return "", agentcontrol.ErrPaneNotFound
	}
	p.mu.Lock()
	// An idle shell with no completed turn waits for its first transition.
	turn := max(1, p.turn)
	for {
		delay := p.quietDelay(time.Now())
		state := p.info.State
		if state == "exited" || state == "waiting_input" || state == "idle" && p.stopped != 0 {
			p.mu.Unlock()
			return state, nil
		}
		if p.stopped >= turn {
			state := p.stoppedState
			p.mu.Unlock()
			return state, nil
		}
		changed := p.changed
		p.mu.Unlock()

		var timer *time.Timer
		var quiet <-chan time.Time
		if delay >= 0 {
			timer = time.NewTimer(delay)
			quiet = timer.C
		}
		select {
		case <-changed:
		case <-quiet:
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "timeout", nil
			}
			return "", ctx.Err()
		}
		if timer != nil {
			timer.Stop()
		}
		p.mu.Lock()
	}
}

func (p *controlledPane) snapshot(refreshCWD bool) PaneInfo {
	p.mu.Lock()
	p.quietDelay(time.Now())
	info, pty := p.info, p.pty
	p.mu.Unlock()
	if refreshCWD && pty != nil {
		if cwd, err := cwdFromPid(pty.Pid()); err == nil && cwd != "" {
			info.CWD = cwd
		}
	}
	return info
}

func ControlInfo(paneID string) (PaneInfo, error) {
	p := lookupControlledPane(paneID)
	if p == nil {
		return PaneInfo{}, agentcontrol.ErrPaneNotFound
	}
	return p.snapshot(true), nil
}

func ControlList() []PaneInfo {
	paneControls.RLock()
	panes := make([]*controlledPane, 0, len(paneControls.panes))
	for _, pane := range paneControls.panes {
		panes = append(panes, pane)
	}
	paneControls.RUnlock()

	out := make([]PaneInfo, 0, len(panes))
	for _, pane := range panes {
		out = append(out, pane.snapshot(true))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func init() {
	terminalactivity.SetPhaseObserver(observeClaudeActivity)
	for _, op := range []string{"send", "read", "wait", "info", "list", "set_metadata"} {
		core.RegisterCtx("terminal_control_"+op, controlCommand)
	}
}

func controlCommand(ctx context.Context, command, raw string) (string, error) {
	var args struct {
		Pane        string `json:"pane"`
		Text        string `json:"text"`
		Submit      bool   `json:"submit"`
		Lines       int    `json:"lines"`
		TimeoutSecs int    `json:"timeoutSecs"`
		PaneInfo
	}
	if err := core.DecodeArgsPayload(raw, &args); err != nil {
		return "", err
	}
	switch command {
	case "terminal_control_send":
		return "{}", ControlSend(args.Pane, args.Text, args.Submit)
	case "terminal_control_read":
		if args.Lines < 0 {
			return "", errors.New("lines must not be negative")
		}
		text, err := ControlRead(args.Pane, args.Lines)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]string{"text": text})
	case "terminal_control_wait":
		if args.TimeoutSecs < 0 {
			return "", errors.New("timeoutSecs must not be negative")
		}
		timeout := 10 * time.Minute
		if args.TimeoutSecs > 0 && args.TimeoutSecs < int(timeout/time.Second) {
			timeout = time.Duration(args.TimeoutSecs) * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		state, err := ControlWait(ctx, args.Pane)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]string{"state": state})
	case "terminal_control_info":
		info, err := ControlInfo(args.Pane)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(info)
	case "terminal_control_list":
		return core.EncodeResult(map[string]any{"panes": ControlList()})
	case "terminal_control_set_metadata":
		paneControls.Lock()
		defer paneControls.Unlock()
		if p := paneControls.panes[args.ID]; p != nil {
			p.mu.Lock()
			p.applyMetadata(args.PaneInfo)
			p.mu.Unlock()
		} else if args.ID != "" && len(paneControls.metadata) < 200 {
			paneControls.metadata[args.ID] = args.PaneInfo
		}
	}
	return "{}", nil
}
