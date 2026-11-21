// Package agentprogress owns the in-memory identity and progress snapshots for
// AI runs. Frontend and native agent implementations report lifecycle changes
// through this package so every event carries a stable sessionId + runId pair.
package agentprogress

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const (
	ProgressEvent        = "agent-run:progress"
	CancelRequestedEvent = "agent-run:cancel-requested"

	completedRetention = 5 * time.Minute
	failedRetention    = 30 * time.Minute
	cancelSettleDelay  = 10 * time.Second
	maxRetainedRuns    = 200
)

const (
	PhaseQueued          = "queued"
	PhaseThinking        = "thinking"
	PhaseStreaming       = "streaming"
	PhaseToolRunning     = "tool_running"
	PhaseWaitingApproval = "waiting_approval"
	PhaseWaitingInput    = "waiting_input"
	PhaseCancelling      = "cancelling"
	PhaseCompleted       = "completed"
	PhaseFailed          = "failed"
	PhaseCancelled       = "cancelled"
)

var validPhases = map[string]struct{}{
	PhaseQueued: {}, PhaseThinking: {}, PhaseStreaming: {}, PhaseToolRunning: {},
	PhaseWaitingApproval: {}, PhaseWaitingInput: {}, PhaseCancelling: {},
	PhaseCompleted: {}, PhaseFailed: {}, PhaseCancelled: {},
}

var sensitiveValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)`),
	regexp.MustCompile(`(?i)(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)`),
	regexp.MustCompile(`(?i)(bearer\s+)([a-z0-9._~+/=-]{12,})`),
	regexp.MustCompile(`(?i)((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)`),
}

type Navigation struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversationId,omitempty"`
	Provider       string `json:"provider,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
}

type SessionProgress struct {
	Phase           string `json:"phase"`
	Summary         string `json:"summary,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	CompletedAt     *int64 `json:"completedAt,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ApprovalID      string `json:"approvalId,omitempty"`
	CancelRequested bool   `json:"cancelRequested,omitempty"`
	// StepsCompleted/StepsTotal describe countable progress such as a subagent plan.
	StepsCompleted int `json:"stepsCompleted,omitempty"`
	StepsTotal     int `json:"stepsTotal,omitempty"`
}

type AgentRun struct {
	RunID            string          `json:"runId"`
	ParentRunID      string          `json:"parentRunId,omitempty"`
	TaskID           string          `json:"taskId,omitempty"`
	Branch           string          `json:"branch,omitempty"`
	WorktreeName     string          `json:"worktreeName,omitempty"`
	SessionID        string          `json:"sessionId"`
	WorkspaceID      string          `json:"workspaceId"`
	WorkspaceScopeID string          `json:"workspaceScopeId"`
	Source           string          `json:"source"`
	Title            string          `json:"title"`
	Revision         uint64          `json:"revision"`
	Navigation       Navigation      `json:"navigation"`
	Progress         SessionProgress `json:"progress"`
}

type startArgs struct {
	ParentRunID      string     `json:"parentRunId,omitempty"`
	TaskID           string     `json:"taskId,omitempty"`
	Branch           string     `json:"branch,omitempty"`
	WorktreeName     string     `json:"worktreeName,omitempty"`
	SessionID        string     `json:"sessionId"`
	WorkspaceID      string     `json:"workspaceId"`
	WorkspaceScopeID string     `json:"workspaceScopeId"`
	Source           string     `json:"source"`
	Title            string     `json:"title"`
	Phase            string     `json:"phase,omitempty"`
	Summary          string     `json:"summary,omitempty"`
	Navigation       Navigation `json:"navigation"`
}

type updateArgs struct {
	RunID      string  `json:"runId"`
	Phase      string  `json:"phase"`
	Summary    *string `json:"summary,omitempty"`
	ErrorCode  *string `json:"errorCode,omitempty"`
	ApprovalID *string `json:"approvalId,omitempty"`
	// Steps are replaced on every update; omitting them clears countable progress.
	StepsCompleted int `json:"stepsCompleted,omitempty"`
	StepsTotal     int `json:"stepsTotal,omitempty"`
}

type runIDArgs struct {
	RunID string `json:"runId"`
}

type cancelRequestedPayload struct {
	RunID     string `json:"runId"`
	SessionID string `json:"sessionId"`
}

type registry struct {
	mu   sync.RWMutex
	runs map[string]AgentRun
}

var runs = registry{runs: make(map[string]AgentRun)}

func init() {
	core.Register("agent_run_start", startHandler)
	core.Register("agent_run_update", updateHandler)
	core.Register("agent_run_list", listHandler)
	core.Register("agent_run_request_cancel", requestCancelHandler)
	core.Register("agent_run_remove", removeHandler)
}

func startHandler(_, argsJSON string) (string, error) {
	var args startArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	run, err := runs.start(args)
	if err != nil {
		return "", err
	}
	emitProgress(run)
	return core.EncodeResult(run)
}

func updateHandler(_, argsJSON string) (string, error) {
	var args updateArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	run, changed, err := runs.update(args)
	if err != nil {
		return "", err
	}
	if changed {
		emitProgress(run)
	}
	return core.EncodeResult(run)
}

func listHandler(_, _ string) (string, error) {
	return core.EncodeResult(runs.list())
}

func requestCancelHandler(_, argsJSON string) (string, error) {
	var args runIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	run, err := RequestCancel(args.RunID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(run)
}

// RequestCancel asks the owner of an unfinished run to stop it, exactly like
// the task list's stop button: the run moves to cancelling, the
// cancel-requested event reaches the conversation or agent session that runs
// it, and the run settles as cancelled if nothing reports back in time.
// Finished runs and repeated requests are left unchanged.
func RequestCancel(runID string) (AgentRun, error) {
	run, changed, err := runs.requestCancel(runID)
	if err != nil {
		return AgentRun{}, err
	}
	if changed {
		emitProgress(run)
		core.Emit(CancelRequestedEvent, cancelRequestedPayload{RunID: run.RunID, SessionID: run.SessionID})
		go settleCancellationAfter(run.RunID, cancelSettleDelay)
	}
	return run, nil
}

func settleCancellationAfter(runID string, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	if run, changed := runs.settleCancellation(runID); changed {
		emitProgress(run)
	}
}

func removeHandler(_, argsJSON string) (string, error) {
	var args runIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := runs.remove(args.RunID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// UpdateActiveSession lets a native agent protocol update the latest active run
// for a stable session without knowing the frontend-created runId.
func UpdateActiveSession(sessionID, source, phase, summary string) bool {
	run, changed := runs.updateActiveSession(sessionID, source, phase, summary)
	if changed {
		emitProgress(run)
	}
	return changed
}

func (r *registry) start(args startArgs) (AgentRun, error) {
	args.SessionID = strings.TrimSpace(args.SessionID)
	args.WorkspaceID = strings.TrimSpace(args.WorkspaceID)
	args.WorkspaceScopeID = strings.TrimSpace(args.WorkspaceScopeID)
	args.Source = strings.TrimSpace(args.Source)
	args.ParentRunID = strings.TrimSpace(args.ParentRunID)
	args.TaskID = strings.TrimSpace(args.TaskID)
	args.Branch = strings.TrimSpace(args.Branch)
	args.WorktreeName = strings.TrimSpace(args.WorktreeName)
	if args.SessionID == "" || args.WorkspaceID == "" || args.WorkspaceScopeID == "" {
		return AgentRun{}, fmt.Errorf("sessionId, workspaceId and workspaceScopeId are required")
	}
	if args.Source != "agentic" && args.Source != "acp" {
		return AgentRun{}, fmt.Errorf("unsupported agent run source: %q", args.Source)
	}
	phase := strings.TrimSpace(args.Phase)
	if phase == "" {
		phase = PhaseQueued
	}
	if !validPhase(phase) || isTerminalPhase(phase) || phase == PhaseCancelling {
		return AgentRun{}, fmt.Errorf("invalid initial agent run phase: %q", phase)
	}
	if err := validateNavigation(args.Navigation); err != nil {
		return AgentRun{}, err
	}
	if args.ParentRunID == "" && (args.TaskID != "" || args.Branch != "" || args.WorktreeName != "") {
		return AgentRun{}, fmt.Errorf("subagent metadata requires parentRunId")
	}
	if args.ParentRunID != "" && args.TaskID == "" {
		return AgentRun{}, fmt.Errorf("subagent run requires taskId")
	}
	runID, err := newRunID()
	if err != nil {
		return AgentRun{}, err
	}
	now := time.Now().UnixMilli()
	run := AgentRun{
		RunID:            runID,
		ParentRunID:      safeText(args.ParentRunID, 120),
		TaskID:           safeText(args.TaskID, 64),
		Branch:           safeText(args.Branch, 200),
		WorktreeName:     safeText(args.WorktreeName, 120),
		SessionID:        args.SessionID,
		WorkspaceID:      args.WorkspaceID,
		WorkspaceScopeID: args.WorkspaceScopeID,
		Source:           args.Source,
		Title:            safeText(args.Title, 120),
		Revision:         1,
		Navigation:       normalizeNavigation(args.Navigation),
		Progress: SessionProgress{
			Phase:     phase,
			Summary:   safeText(args.Summary, 240),
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if run.Title == "" {
		run.Title = "AI task"
	}
	r.mu.Lock()
	r.pruneLocked(now)
	r.runs[run.RunID] = run
	r.mu.Unlock()
	return run, nil
}

func (r *registry) update(args updateArgs) (AgentRun, bool, error) {
	runID := strings.TrimSpace(args.RunID)
	phase := strings.TrimSpace(args.Phase)
	if runID == "" || !validPhase(phase) {
		return AgentRun{}, false, fmt.Errorf("runId and a valid phase are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return AgentRun{}, false, fmt.Errorf("agent run not found: %q", runID)
	}
	if isTerminalPhase(run.Progress.Phase) {
		return run, false, nil
	}
	if run.Progress.Phase == PhaseCancelling && phase != PhaseCancelling && !isTerminalPhase(phase) {
		return run, false, nil
	}
	applyUpdate(&run, phase, args.Summary, args.ErrorCode, args.ApprovalID)
	run.Progress.StepsCompleted, run.Progress.StepsTotal = normalizeSteps(args.StepsCompleted, args.StepsTotal)
	r.runs[runID] = run
	return run, true, nil
}

func (r *registry) updateActiveSession(sessionID, source, phase, summary string) (AgentRun, bool) {
	sessionID = strings.TrimSpace(sessionID)
	source = strings.TrimSpace(source)
	phase = strings.TrimSpace(phase)
	if sessionID == "" || !validPhase(phase) {
		return AgentRun{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var selected AgentRun
	found := false
	for _, run := range r.runs {
		if run.SessionID != sessionID || (source != "" && run.Source != source) || isTerminalPhase(run.Progress.Phase) {
			continue
		}
		if !found || run.Progress.CreatedAt > selected.Progress.CreatedAt {
			selected = run
			found = true
		}
	}
	if !found {
		return AgentRun{}, false
	}
	if selected.Progress.Phase == PhaseCancelling && phase != PhaseCancelling && !isTerminalPhase(phase) {
		return selected, false
	}
	if selected.Progress.Phase == phase && summary == "" {
		return selected, false
	}
	var summaryPointer *string
	if summary != "" {
		summaryPointer = &summary
	}
	applyUpdate(&selected, phase, summaryPointer, nil, nil)
	r.runs[selected.RunID] = selected
	return selected, true
}

func normalizeSteps(completed, total int) (int, int) {
	if total <= 0 || completed < 0 {
		return 0, 0
	}
	if completed > total {
		completed = total
	}
	return completed, total
}

func applyUpdate(run *AgentRun, phase string, summary, errorCode, approvalID *string) {
	now := time.Now().UnixMilli()
	if now <= run.Progress.UpdatedAt {
		now = run.Progress.UpdatedAt + 1
	}
	run.Progress.Phase = phase
	if phase == PhaseCancelling {
		run.Progress.CancelRequested = true
	}
	run.Progress.UpdatedAt = now
	run.Revision++
	if summary != nil {
		run.Progress.Summary = safeText(*summary, 240)
	}
	if errorCode != nil {
		run.Progress.ErrorCode = safeText(*errorCode, 80)
	}
	if approvalID != nil {
		run.Progress.ApprovalID = safeText(*approvalID, 120)
	}
	if phase != PhaseWaitingApproval {
		run.Progress.ApprovalID = ""
	}
	if isTerminalPhase(phase) {
		completedAt := now
		run.Progress.CompletedAt = &completedAt
	}
}

func (r *registry) requestCancel(runID string) (AgentRun, bool, error) {
	runID = strings.TrimSpace(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return AgentRun{}, false, fmt.Errorf("agent run not found: %q", runID)
	}
	if isTerminalPhase(run.Progress.Phase) || run.Progress.CancelRequested {
		return run, false, nil
	}
	run.Progress.CancelRequested = true
	applyUpdate(&run, PhaseCancelling, nil, nil, nil)
	r.runs[runID] = run
	return run, true, nil
}

func (r *registry) settleCancellation(runID string) (AgentRun, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return AgentRun{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.Progress.Phase != PhaseCancelling {
		return run, false
	}
	applyUpdate(&run, PhaseCancelled, nil, nil, nil)
	r.runs[runID] = run
	return run, true
}

func (r *registry) list() []AgentRun {
	r.mu.Lock()
	r.pruneLocked(time.Now().UnixMilli())
	result := make([]AgentRun, 0, len(r.runs))
	for _, run := range r.runs {
		result = append(result, run)
	}
	r.mu.Unlock()
	sort.Slice(result, func(i, j int) bool {
		return result[i].Progress.UpdatedAt > result[j].Progress.UpdatedAt
	})
	return result
}

func (r *registry) pruneLocked(now int64) {
	for runID, run := range r.runs {
		if !isTerminalPhase(run.Progress.Phase) {
			continue
		}
		terminalAt := run.Progress.UpdatedAt
		if run.Progress.CompletedAt != nil {
			terminalAt = *run.Progress.CompletedAt
		}
		retention := completedRetention
		if run.Progress.Phase == PhaseFailed {
			retention = failedRetention
		}
		if now-terminalAt > retention.Milliseconds() {
			delete(r.runs, runID)
		}
	}

	overflow := len(r.runs) - maxRetainedRuns
	if overflow <= 0 {
		return
	}
	terminalRuns := make([]AgentRun, 0, len(r.runs))
	for _, run := range r.runs {
		if isTerminalPhase(run.Progress.Phase) {
			terminalRuns = append(terminalRuns, run)
		}
	}
	sort.Slice(terminalRuns, func(i, j int) bool {
		return terminalRuns[i].Progress.UpdatedAt < terminalRuns[j].Progress.UpdatedAt
	})
	for index := 0; index < overflow && index < len(terminalRuns); index++ {
		delete(r.runs, terminalRuns[index].RunID)
	}
}

func (r *registry) remove(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("runId is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return fmt.Errorf("agent run not found: %q", runID)
	}
	if !isTerminalPhase(run.Progress.Phase) {
		return fmt.Errorf("active agent run cannot be removed: %q", runID)
	}
	delete(r.runs, runID)
	return nil
}

func validateNavigation(n Navigation) error {
	switch strings.TrimSpace(n.Type) {
	case "ai-conversation":
		if strings.TrimSpace(n.ConversationID) == "" {
			return fmt.Errorf("conversationId is required for ai-conversation navigation")
		}
	case "session":
		if strings.TrimSpace(n.Provider) == "" || strings.TrimSpace(n.SessionID) == "" {
			return fmt.Errorf("provider and sessionId are required for session navigation")
		}
	default:
		return fmt.Errorf("unsupported agent run navigation type: %q", n.Type)
	}
	return nil
}

func normalizeNavigation(n Navigation) Navigation {
	return Navigation{
		Type:           strings.TrimSpace(n.Type),
		ConversationID: safeText(n.ConversationID, 160),
		Provider:       safeText(n.Provider, 40),
		SessionID:      safeText(n.SessionID, 240),
	}
}

func validPhase(phase string) bool {
	_, ok := validPhases[phase]
	return ok
}

func isTerminalPhase(phase string) bool {
	return phase == PhaseCompleted || phase == PhaseFailed || phase == PhaseCancelled
}

func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate agent run id: %w", err)
	}
	return "run-" + hex.EncodeToString(buf), nil
}

func safeText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	for _, pattern := range sensitiveValuePatterns {
		value = pattern.ReplaceAllString(value, "${1}[REDACTED]")
	}
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-1]) + "…"
}

var (
	observersMu sync.RWMutex
	observers   []func(AgentRun)
)

// Observe registers fn for every published run snapshot. fn runs on the
// publishing goroutine and must not block; call from init().
func Observe(fn func(AgentRun)) {
	if fn == nil {
		panic("nil agent run observer")
	}
	observersMu.Lock()
	observers = append(observers, fn)
	observersMu.Unlock()
}

func notifyObservers(run AgentRun) {
	observersMu.RLock()
	current := append([]func(AgentRun){}, observers...)
	observersMu.RUnlock()
	for _, fn := range current {
		fn(run)
	}
}

// Activity is the state of every unfinished AI task at one moment. A run and
// its sub-agent runs (ParentRunID chains) count as one task, in the most
// urgent state of any of them: waiting for the user, then working (running a
// tool), then thinking (queued, thinking, streaming or cancelling).
type Activity struct {
	Active   int `json:"active"`
	Thinking int `json:"thinking"`
	Working  int `json:"working"`
	Waiting  int `json:"waiting"`
}

const maxParentDepth = 16

func activityRank(phase string) int {
	switch phase {
	case PhaseWaitingApproval, PhaseWaitingInput:
		return 3
	case PhaseToolRunning:
		return 2
	case PhaseQueued, PhaseThinking, PhaseStreaming, PhaseCancelling:
		return 1
	}
	return 0
}

// CurrentActivity summarizes the unfinished runs in the registry.
func CurrentActivity() Activity {
	all := runs.list()
	byID := make(map[string]AgentRun, len(all))
	for _, run := range all {
		byID[run.RunID] = run
	}
	root := func(run AgentRun) string {
		id := run.RunID
		for depth := 0; depth < maxParentDepth; depth++ {
			parent, ok := byID[byID[id].ParentRunID]
			if !ok {
				break
			}
			id = parent.RunID
		}
		return id
	}
	tasks := map[string]int{}
	for _, run := range all {
		if rank := activityRank(run.Progress.Phase); rank > 0 {
			key := root(run)
			tasks[key] = max(tasks[key], rank)
		}
	}
	var activity Activity
	for _, rank := range tasks {
		activity.Active++
		switch rank {
		case 3:
			activity.Waiting++
		case 2:
			activity.Working++
		default:
			activity.Thinking++
		}
	}
	return activity
}

// Runs returns a copy of the runs the registry holds, most recently updated first.
func Runs() []AgentRun {
	return runs.list()
}

// Lookup returns the run with runID while the registry still holds it.
func Lookup(runID string) (AgentRun, bool) {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	run, ok := runs.runs[runID]
	return run, ok
}
