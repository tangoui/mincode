package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Plugin-originated AI turns: a plugin with the "ai" capability (for example a
// team-chat bot) asks the desktop AI assistant to answer a message. The turn
// runs in the assistant's own agent loop — same model, tools, approvals and
// usage accounting — in a conversation reserved for that plugin; the host only
// relays the request to the frontend and the streamed reply back to the plugin.
const (
	pluginAITurnEvent       = "plugin:ai-turn"
	pluginAITurnCancelEvent = "plugin:ai-turn-cancel"
	// Carries a chat approval decision to the AI drawer's approval queue.
	pluginAITurnApprovalEvent = "plugin:ai-turn-approval"

	aiTurnMaxPromptBytes   = 16 << 10
	aiTurnMaxInstructions  = 4 << 10
	aiTurnMaxAllowed       = 50
	aiTurnMaxTextBytes     = 64 << 10
	aiTurnMaxActivityBytes = 512
	aiTurnMaxLabelRunes    = 80
	aiTurnMaxActive        = 4
	aiTurnMaxPerHour       = 60
	aiTurnClaimTimeout     = 20 * time.Second
	aiTurnMaxDuration      = 15 * time.Minute
	aiTurnRetention        = 10 * time.Minute
)

var aiTurnKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var aiTurnConversationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var aiTurnPluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type aiTurnStatus string

const (
	aiTurnPending          aiTurnStatus = "pending"
	aiTurnRunning          aiTurnStatus = "running"
	aiTurnAwaitingApproval aiTurnStatus = "awaiting_approval"
	aiTurnCompleted        aiTurnStatus = "completed"
	aiTurnFailed           aiTurnStatus = "failed"
	aiTurnCancelled        aiTurnStatus = "cancelled"
)

func (s aiTurnStatus) terminal() bool {
	return s == aiTurnCompleted || s == aiTurnFailed || s == aiTurnCancelled
}

type aiTurn struct {
	ID              string
	PluginID        string
	PluginName      string
	ConversationKey string
	ConversationID  string
	Title           string
	Sender          string
	Prompt          string
	Instructions    string
	AllowedPlugins  []string
	MaxRisk         string
	ChatApproval    bool
	WorkspaceAccess bool
	History         string
	Approval        *aiTurnApproval
	Status          aiTurnStatus
	Text            string
	Activity        string
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	claimed         bool
}

// aiTurnRequest is what the frontend receives when it claims a turn.
type aiTurnRequest struct {
	ID              string `json:"id"`
	PluginID        string `json:"pluginId"`
	PluginName      string `json:"pluginName"`
	ConversationKey string `json:"conversationKey"`
	ConversationID  string `json:"conversationId,omitempty"`
	Title           string `json:"title"`
	Sender          string `json:"sender,omitempty"`
	Prompt          string `json:"prompt"`
	// Instructions come from the plugin's configuration (the local user), not
	// from the relayed message; the assistant adds them to the system prompt.
	Instructions string `json:"instructions,omitempty"`
	// AllowedPlugins limits plugin.list/describe/invoke to these ids; null means
	// no restriction and an empty list allows no plugin.
	AllowedPlugins []string `json:"allowedPlugins"`
	// MaxRisk caps the tool risk of the turn ("" = no cap); riskier calls are
	// refused without asking.
	MaxRisk string `json:"maxRisk,omitempty"`
	// ChatApproval exposes approvals to the plugin (granted on the desktop).
	ChatApproval bool `json:"chatApproval,omitempty"`
	// WorkspaceAccess runs the turn like a local conversation (granted on the desktop).
	WorkspaceAccess bool `json:"workspaceAccess,omitempty"`
	// History "none" sends the model only the system prompts and this turn's
	// prompt (plus the turn's own tool calls), no earlier conversation
	// messages; "" keeps the default of replaying the conversation.
	History   string `json:"history,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// aiTurnApproval is a pending tool approval of a turn with chat approval.
type aiTurnApproval struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Risk    string `json:"risk"`
	Tool    string `json:"tool"`
}

// aiTurnState is what the plugin sees when it polls.
type aiTurnState struct {
	ID             string       `json:"id"`
	Status         aiTurnStatus `json:"status"`
	Done           bool         `json:"done"`
	Text           string       `json:"text"`
	Activity       string       `json:"activity,omitempty"`
	Error          string       `json:"error,omitempty"`
	ConversationID string       `json:"conversationId,omitempty"`
	// Approval is set while a chat-approval turn waits for a decision.
	Approval *aiTurnApproval `json:"approval,omitempty"`
}

type aiTurnRegistry struct {
	mu     sync.Mutex
	turns  map[string]*aiTurn
	starts map[string][]time.Time
	now    func() time.Time
	emit   func(name string, data any)
	after  func(time.Duration, func())
	grants func(pluginID string) aiGrants
}

var pluginAITurns = newAITurnRegistry()

func newAITurnRegistry() *aiTurnRegistry {
	return &aiTurnRegistry{
		turns:  map[string]*aiTurn{},
		starts: map[string][]time.Time{},
		now:    time.Now,
		emit:   core.Emit,
		after:  func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		grants: readAIGrants,
	}
}

func init() {
	core.Register("plugin_ai_grants_get", pluginAIGrantsGetHandler)
	core.Register("plugin_ai_grants_set", pluginAIGrantsSetHandler)
	core.Register("plugin_ai_turn_pending", pluginAITurnPendingHandler)
	core.Register("plugin_ai_turn_claim", pluginAITurnClaimHandler)
	core.Register("plugin_ai_turn_update", pluginAITurnUpdateHandler)
}

var errAITurnNotFound = errors.New("AI turn not found")

func newAITurnID() string {
	buffer := make([]byte, 12)
	_, _ = rand.Read(buffer)
	return "aiturn-" + hex.EncodeToString(buffer)
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

// truncateBytes keeps the first limit bytes on a rune boundary.
func truncateBytes(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

type aiTurnStartParams struct {
	ConversationKey string   `json:"conversationKey"`
	ConversationID  string   `json:"conversationId"`
	Title           string   `json:"title"`
	Sender          string   `json:"sender"`
	Prompt          string   `json:"prompt"`
	Instructions    string   `json:"instructions"`
	AllowedPlugins  []string `json:"allowedPlugins"`
	MaxRisk         string   `json:"maxRisk"`
	ChatApproval    bool     `json:"chatApproval"`
	WorkspaceAccess bool     `json:"workspaceAccess"`
	History         string   `json:"history"`
}

// aiTurnHistoryNone runs a turn without the conversation's earlier messages.
const aiTurnHistoryNone = "none"

func (r *aiTurnRegistry) start(pluginID, pluginName string, params aiTurnStartParams) (aiTurnState, error) {
	key := strings.TrimSpace(params.ConversationKey)
	if !aiTurnKeyPattern.MatchString(key) {
		return aiTurnState{}, errors.New("conversationKey must be 1-128 characters of A-Z a-z 0-9 . _ : -")
	}
	conversationID := strings.TrimSpace(params.ConversationID)
	if conversationID != "" && !aiTurnConversationPattern.MatchString(conversationID) {
		return aiTurnState{}, errors.New("invalid conversationId")
	}
	prompt := strings.TrimSpace(strings.ToValidUTF8(params.Prompt, ""))
	if prompt == "" || len(prompt) > aiTurnMaxPromptBytes {
		return aiTurnState{}, errors.New("prompt must be 1-16384 bytes")
	}
	instructions := strings.TrimSpace(strings.ToValidUTF8(params.Instructions, ""))
	if len(instructions) > aiTurnMaxInstructions {
		return aiTurnState{}, errors.New("instructions must be at most 4096 bytes")
	}
	var allowed []string
	if params.AllowedPlugins != nil {
		if len(params.AllowedPlugins) > aiTurnMaxAllowed {
			return aiTurnState{}, errors.New("allowedPlugins lists at most 50 plugin ids")
		}
		seen := map[string]bool{}
		allowed = []string{}
		for _, id := range params.AllowedPlugins {
			id = strings.TrimSpace(id)
			if !aiTurnPluginIDPattern.MatchString(id) {
				return aiTurnState{}, errors.New("invalid plugin id in allowedPlugins")
			}
			if !seen[id] {
				seen[id] = true
				allowed = append(allowed, id)
			}
		}
	}
	switch params.MaxRisk {
	case "", "read", "network", "write", "dangerous":
	default:
		return aiTurnState{}, errors.New("maxRisk must be read, network, write or dangerous")
	}
	switch params.History {
	case "", aiTurnHistoryNone:
	default:
		return aiTurnState{}, errors.New(`history must be "none" or omitted`)
	}
	if params.ChatApproval || params.WorkspaceAccess {
		granted := r.grants(pluginID)
		if params.ChatApproval && !granted.ChatApproval {
			return aiTurnState{}, errors.New("chat approval is not granted to this plugin in MinCode")
		}
		if params.WorkspaceAccess && !granted.WorkspaceAccess {
			return aiTurnState{}, errors.New("workspace access is not granted to this plugin in MinCode")
		}
	}
	title := truncateRunes(params.Title, aiTurnMaxLabelRunes)
	if title == "" {
		title = key
	}
	now := r.now()
	r.mu.Lock()
	r.pruneLocked(now)
	active := 0
	for _, turn := range r.turns {
		if turn.PluginID == pluginID && !turn.Status.terminal() {
			active++
		}
	}
	if active >= aiTurnMaxActive {
		r.mu.Unlock()
		return aiTurnState{}, errors.New("too many AI turns are running for this plugin")
	}
	recent := r.starts[pluginID][:0]
	for _, at := range r.starts[pluginID] {
		if now.Sub(at) < time.Hour {
			recent = append(recent, at)
		}
	}
	if len(recent) >= aiTurnMaxPerHour {
		r.starts[pluginID] = recent
		r.mu.Unlock()
		return aiTurnState{}, errors.New("hourly AI turn limit reached for this plugin")
	}
	r.starts[pluginID] = append(recent, now)
	turn := &aiTurn{
		ID: newAITurnID(), PluginID: pluginID, PluginName: truncateRunes(pluginName, aiTurnMaxLabelRunes),
		ConversationKey: key, ConversationID: conversationID, Title: title,
		Sender: truncateRunes(params.Sender, aiTurnMaxLabelRunes), Prompt: prompt,
		Instructions: instructions, AllowedPlugins: allowed,
		MaxRisk: params.MaxRisk, ChatApproval: params.ChatApproval, WorkspaceAccess: params.WorkspaceAccess,
		History: params.History, Status: aiTurnPending, CreatedAt: now, UpdatedAt: now,
	}
	r.turns[turn.ID] = turn
	request := turn.request()
	state := turn.state()
	r.mu.Unlock()

	r.emit(pluginAITurnEvent, map[string]any{"id": request.ID, "pluginId": request.PluginID})
	r.after(aiTurnClaimTimeout, func() {
		r.finishIf(turn.ID, func(t *aiTurn) bool { return !t.claimed }, aiTurnFailed, "MinCode AI 助手未就绪，请确认 MinCode 窗口已打开")
	})
	r.after(aiTurnMaxDuration, func() {
		if r.finishIf(turn.ID, func(*aiTurn) bool { return true }, aiTurnFailed, "AI 回答超时") {
			r.emit(pluginAITurnCancelEvent, map[string]any{"id": turn.ID})
		}
	})
	return state, nil
}

func (t *aiTurn) request() aiTurnRequest {
	return aiTurnRequest{
		ID: t.ID, PluginID: t.PluginID, PluginName: t.PluginName, ConversationKey: t.ConversationKey,
		ConversationID: t.ConversationID, Title: t.Title, Sender: t.Sender, Prompt: t.Prompt,
		Instructions: t.Instructions, AllowedPlugins: cloneAllowedPlugins(t.AllowedPlugins),
		MaxRisk: t.MaxRisk, ChatApproval: t.ChatApproval, WorkspaceAccess: t.WorkspaceAccess,
		History: t.History, CreatedAt: t.CreatedAt.UnixMilli(),
	}
}

func cloneAllowedPlugins(ids []string) []string {
	if ids == nil {
		return nil
	}
	return append([]string{}, ids...)
}

func (t *aiTurn) state() aiTurnState {
	state := aiTurnState{
		ID: t.ID, Status: t.Status, Done: t.Status.terminal(), Text: t.Text,
		Activity: t.Activity, Error: t.Error, ConversationID: t.ConversationID,
	}
	if t.ChatApproval && t.Status == aiTurnAwaitingApproval && t.Approval != nil {
		approval := *t.Approval
		state.Approval = &approval
	}
	return state
}

// finishIf moves a live turn to a terminal status when condition holds.
func (r *aiTurnRegistry) finishIf(id string, condition func(*aiTurn) bool, status aiTurnStatus, message string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[id]
	if turn == nil || turn.Status.terminal() || !condition(turn) {
		return false
	}
	turn.Status, turn.Error, turn.Activity, turn.Approval, turn.UpdatedAt = status, message, "", nil, r.now()
	return true
}

func (r *aiTurnRegistry) poll(pluginID, id string) (aiTurnState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[id]
	if turn == nil || turn.PluginID != pluginID {
		return aiTurnState{}, errAITurnNotFound
	}
	return turn.state(), nil
}

func (r *aiTurnRegistry) cancel(pluginID, id string) (aiTurnState, error) {
	r.mu.Lock()
	turn := r.turns[id]
	if turn == nil || turn.PluginID != pluginID {
		r.mu.Unlock()
		return aiTurnState{}, errAITurnNotFound
	}
	changed := !turn.Status.terminal()
	if changed {
		turn.Status, turn.Activity, turn.Approval, turn.UpdatedAt = aiTurnCancelled, "", nil, r.now()
	}
	state := turn.state()
	r.mu.Unlock()
	if changed {
		r.emit(pluginAITurnCancelEvent, map[string]any{"id": id})
	}
	return state, nil
}

// abandonPlugin cancels every live turn of a plugin whose process stopped.
func (r *aiTurnRegistry) abandonPlugin(pluginID string) {
	r.mu.Lock()
	var cancelled []string
	for id, turn := range r.turns {
		if turn.PluginID == pluginID && !turn.Status.terminal() {
			turn.Status, turn.Activity, turn.Error, turn.Approval, turn.UpdatedAt = aiTurnCancelled, "", "plugin stopped", nil, r.now()
			cancelled = append(cancelled, id)
		}
	}
	r.mu.Unlock()
	for _, id := range cancelled {
		r.emit(pluginAITurnCancelEvent, map[string]any{"id": id})
	}
}

func (r *aiTurnRegistry) pending() []aiTurnRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []aiTurnRequest{}
	for _, turn := range r.turns {
		if turn.Status == aiTurnPending && !turn.claimed {
			request := turn.request()
			request.Prompt, request.Instructions, request.AllowedPlugins = "", "", nil
			request.MaxRisk, request.ChatApproval, request.WorkspaceAccess, request.History = "", false, false, ""
			out = append(out, request)
		}
	}
	return out
}

// claim hands the prompt to exactly one frontend consumer.
func (r *aiTurnRegistry) claim(id string) (aiTurnRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[id]
	if turn == nil || turn.claimed || turn.Status != aiTurnPending {
		return aiTurnRequest{}, errAITurnNotFound
	}
	turn.claimed = true
	turn.Status, turn.UpdatedAt = aiTurnRunning, r.now()
	return turn.request(), nil
}

type aiTurnUpdate struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Text           *string         `json:"text"`
	Activity       *string         `json:"activity"`
	Error          string          `json:"error"`
	ConversationID string          `json:"conversationId"`
	Approval       *aiTurnApproval `json:"approval"`
}

func (r *aiTurnRegistry) update(input aiTurnUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn := r.turns[input.ID]
	if turn == nil || !turn.claimed {
		return errAITurnNotFound
	}
	if turn.Status.terminal() {
		// A late update after cancellation or timeout is harmless.
		return nil
	}
	status := aiTurnStatus(input.Status)
	switch status {
	case "":
		status = turn.Status
	case aiTurnRunning, aiTurnAwaitingApproval, aiTurnCompleted, aiTurnFailed, aiTurnCancelled:
	default:
		return errors.New("invalid AI turn status")
	}
	if id := strings.TrimSpace(input.ConversationID); id != "" {
		if !aiTurnConversationPattern.MatchString(id) {
			return errors.New("invalid conversationId")
		}
		turn.ConversationID = id
	}
	if input.Text != nil {
		turn.Text = truncateBytes(*input.Text, aiTurnMaxTextBytes)
	}
	if input.Activity != nil {
		turn.Activity = truncateBytes(strings.TrimSpace(*input.Activity), aiTurnMaxActivityBytes)
	}
	turn.Status, turn.UpdatedAt = status, r.now()
	turn.Approval = nil
	if status == aiTurnAwaitingApproval && input.Approval != nil && turn.ChatApproval {
		approval := aiTurnApproval{
			ID:      strings.TrimSpace(input.Approval.ID),
			Summary: truncateBytes(strings.TrimSpace(input.Approval.Summary), aiTurnMaxActivityBytes),
			Risk:    input.Approval.Risk,
			Tool:    truncateBytes(input.Approval.Tool, 128),
		}
		if approval.ID == "" || len(approval.ID) > 128 {
			return errors.New("invalid approval id")
		}
		turn.Approval = &approval
	}
	if status.terminal() {
		turn.Activity = ""
		turn.Error = truncateBytes(strings.TrimSpace(input.Error), aiTurnMaxActivityBytes)
	}
	return nil
}

// approve forwards a chat decision for the turn's pending approval. The grant
// is checked again so revoking it on the desktop takes effect immediately.
func (r *aiTurnRegistry) approve(pluginID, id, approvalID string, approved bool) (aiTurnState, error) {
	if !r.grants(pluginID).ChatApproval {
		return aiTurnState{}, errors.New("chat approval is not granted to this plugin in MinCode")
	}
	r.mu.Lock()
	turn := r.turns[id]
	if turn == nil || turn.PluginID != pluginID {
		r.mu.Unlock()
		return aiTurnState{}, errAITurnNotFound
	}
	if !turn.ChatApproval || turn.Status != aiTurnAwaitingApproval || turn.Approval == nil || turn.Approval.ID != approvalID {
		state := turn.state()
		r.mu.Unlock()
		return state, errors.New("no matching approval is pending for this turn")
	}
	turn.Approval, turn.Status, turn.UpdatedAt = nil, aiTurnRunning, r.now()
	state := turn.state()
	r.mu.Unlock()
	r.emit(pluginAITurnApprovalEvent, map[string]any{"id": id, "approvalId": approvalID, "approved": approved})
	return state, nil
}

func (r *aiTurnRegistry) pruneLocked(now time.Time) {
	for id, turn := range r.turns {
		if turn.Status.terminal() && now.Sub(turn.UpdatedAt) > aiTurnRetention {
			delete(r.turns, id)
		}
	}
}

func (runtimeHost *processRuntime) handleAITurnRequest(message rpcEnvelope, respond func(any, *rpcError)) {
	var result any
	var err error
	switch message.Method {
	case "host.ai.turn.start":
		var params aiTurnStartParams
		// Decoding is lenient (unknown fields are ignored), so hosts released
		// before "history" run such a turn with the full conversation history.
		if json.Unmarshal(message.Params, &params) != nil {
			respond(nil, &rpcError{Code: -32602, Message: "invalid AI turn params"})
			return
		}
		result, err = pluginAITurns.start(runtimeHost.pluginID, runtimeHost.manifest.Name, params)
	case "host.ai.plugins":
		result, err = aiCallablePlugins(runtimeHost.pluginID)
	case "host.ai.grants":
		result = readAIGrants(runtimeHost.pluginID)
	case "host.ai.turn.approve":
		var params struct {
			ID         string `json:"id"`
			ApprovalID string `json:"approvalId"`
			Approved   bool   `json:"approved"`
		}
		if json.Unmarshal(message.Params, &params) != nil || strings.TrimSpace(params.ID) == "" || strings.TrimSpace(params.ApprovalID) == "" {
			respond(nil, &rpcError{Code: -32602, Message: "invalid approval params"})
			return
		}
		result, err = pluginAITurns.approve(runtimeHost.pluginID, params.ID, params.ApprovalID, params.Approved)
	case "host.ai.turn.poll", "host.ai.turn.cancel":
		var params struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(message.Params, &params) != nil || strings.TrimSpace(params.ID) == "" {
			respond(nil, &rpcError{Code: -32602, Message: "invalid AI turn id"})
			return
		}
		if message.Method == "host.ai.turn.poll" {
			result, err = pluginAITurns.poll(runtimeHost.pluginID, params.ID)
		} else {
			result, err = pluginAITurns.cancel(runtimeHost.pluginID, params.ID)
		}
	}
	if err != nil {
		code := -32602
		if errors.Is(err, errAITurnNotFound) {
			code = -32004
		}
		respond(nil, &rpcError{Code: code, Message: err.Error()})
		return
	}
	respond(result, nil)
}

// aiCallablePluginInfo describes a plugin whose actions the assistant can call,
// so a plugin can offer "which plugins may answer" choices (host.ai.plugins).
type aiCallablePluginInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Actions     []string `json:"actions"`
}

func aiCallablePlugins(caller string) ([]aiCallablePluginInfo, error) {
	manager := host()
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	out := []aiCallablePluginInfo{}
	for _, record := range records {
		record.mu.Lock()
		manifest, enabled, id := record.manifest, record.enabled, record.id
		record.mu.Unlock()
		if manifest == nil || id == caller {
			continue
		}
		var actions []string
		for _, action := range manifest.Contributes.Actions {
			if action.AICallable {
				actions = append(actions, action.ID)
			}
		}
		if len(actions) == 0 {
			continue
		}
		out = append(out, aiCallablePluginInfo{ID: id, Name: manifest.Name, Description: truncateRunes(manifest.Description, 160), Enabled: enabled, Actions: actions})
	}
	return out, nil
}

func pluginAITurnPendingHandler(_, _ string) (string, error) {
	return core.EncodeResult(pluginAITurns.pending())
}

func pluginAITurnClaimHandler(_, args string) (string, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	request, err := pluginAITurns.claim(strings.TrimSpace(input.ID))
	if err != nil {
		return "", err
	}
	return core.EncodeResult(request)
}

func pluginAITurnUpdateHandler(_, args string) (string, error) {
	var input aiTurnUpdate
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	if err := pluginAITurns.update(input); err != nil {
		return "", err
	}
	return core.EncodeResult(true)
}

// aiGrants are desktop-only permissions for an "ai" plugin, stored in the
// host's own KV namespace; plugins can read them but never change them.
type aiGrants struct {
	ChatApproval    bool `json:"chatApproval"`
	WorkspaceAccess bool `json:"workspaceAccess"`
}

func aiGrantsKVKey(id string) string { return id + ".ai" }

func readAIGrants(id string) aiGrants {
	raw, found, err := pluginKVGetRaw(hostPluginID, aiGrantsKVKey(id))
	var grants aiGrants
	if err != nil || !found || json.Unmarshal(raw, &grants) != nil {
		return aiGrants{}
	}
	return grants
}

func pluginAIGrantsGetHandler(_, args string) (string, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	return core.EncodeResult(readAIGrants(strings.TrimSpace(input.ID)))
}

// pluginAIGrantsSetHandler is a frontend command: only the desktop UI can grant.
func pluginAIGrantsSetHandler(_, args string) (string, error) {
	var input struct {
		ID string `json:"id"`
		aiGrants
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	id := strings.TrimSpace(input.ID)
	record, err := host().record(id)
	if err != nil {
		return "", err
	}
	record.mu.Lock()
	manifest := record.manifest
	record.mu.Unlock()
	hasAI := false
	if manifest != nil {
		for _, capability := range manifest.Capabilities {
			hasAI = hasAI || capability == api.CapabilityAI
		}
	}
	if !hasAI {
		return "", errors.New("this plugin does not declare the ai capability")
	}
	value, _ := json.Marshal(input.aiGrants)
	if err := pluginKVSetRaw(hostPluginID, aiGrantsKVKey(id), value); err != nil {
		return "", err
	}
	return core.EncodeResult(input.aiGrants)
}
