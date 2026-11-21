package terminalactivity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

func RunHook(input []byte) error {
	if len(input) == 0 {
		return nil
	}
	var payload hookInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return err
	}
	token := strings.TrimSpace(os.Getenv("MINCODE_CLI_TAB_TOKEN"))
	inbox := strings.TrimSpace(os.Getenv("MINCODE_TERMINAL_ACTIVITY_INBOX"))
	if token == "" || inbox == "" || payload.HookEventName == "" {
		return nil
	}
	payload.Prompt = safeSummary(payload.Prompt)
	envelope := hookEnvelope{Token: token, Received: time.Now().UTC(), Input: payload}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(inbox, ".event-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(inbox, filepath.Base(name)+".ready"))
}

var inboxPollInterval = 250 * time.Millisecond

func consumeInbox(inbox string, stop <-chan struct{}, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(inboxPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sweepExpiredRegistrations(time.Now())
			drainInbox(inbox)
			if stopPollerIfIdle(done) {
				return
			}
		}
	}
}

func drainInbox(inbox string) {
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ready") {
			continue
		}
		path := filepath.Join(inbox, entry.Name())
		processEventFile(path)
		_ = os.Remove(path)
	}
}

// stopPollerIfIdle 在没有任何登记时让当前轮询退出；判断与新登记拉起轮询同在 state.mu 下，
// 不会漏掉并发登记。done 用于确认 state 里记录的仍是本轮询，避免误清后继轮询的标记。
func stopPollerIfIdle(done chan struct{}) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.registrations) != 0 {
		return false
	}
	if state.done == done {
		state.pollerRunning = false
	}
	return true
}

func processEventFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	var envelope hookEnvelope
	if err := json.NewDecoder(bufio.NewReader(file)).Decode(&envelope); err != nil {
		return
	}
	state.mu.Lock()
	item, ok := state.registrations[envelope.Token]
	if !ok || time.Since(item.CreatedAt) > registrationTTL {
		if ok {
			delete(state.registrations, envelope.Token)
		}
		state.mu.Unlock()
		return
	}
	state.mu.Unlock()

	phase, summary, autoLabel := mapHookEvent(envelope.Input)
	if phase == "" {
		return
	}
	core.Emit(activityEvent, activitySnapshot{
		PaneID:    item.PaneID,
		Phase:     phase,
		Event:     envelope.Input.HookEventName,
		Source:    "claude-hook",
		Summary:   summary,
		AutoLabel: autoLabel,
		UpdatedAt: envelope.Received.UnixMilli(),
	})
	if phase == "completed" {
		unregisterPane(item.PaneID)
	}
}

func mapHookEvent(input hookInput) (phase, summary, autoLabel string) {
	switch input.HookEventName {
	case "SessionStart":
		// 会话刚起来（含 --resume）只是回到提示符等输入，没有任何 turn 在跑。
		// 标成 active 会让标签一直转圈，直到用户真的提交提示词才有下一个事件。
		phase, summary = "waiting_input", "Claude session ready"
		autoLabel = safeSummary(input.Prompt)
	case "UserPromptSubmit":
		phase = "active"
		autoLabel = safeSummary(input.Prompt)
	case "PreToolUse":
		phase = "active"
		summary = safeToolSummary("Running", input.ToolName)
	case "PostToolUse":
		phase = "active"
		summary = safeToolSummary("Finished", input.ToolName)
	case "PostToolUseFailure", "StopFailure":
		phase = "failed"
		summary = "Claude Code reported a failure"
	case "PermissionRequest":
		phase = "waiting_approval"
		summary = safeToolSummary("Waiting for approval", input.ToolName)
	case "Notification":
		switch input.NotificationType {
		case "permission_prompt":
			phase, summary = "waiting_approval", "Waiting for approval"
		case "idle_prompt", "agent_needs_input":
			phase, summary = "waiting_input", "Waiting for input"
		case "agent_completed":
			phase, summary = "waiting_input", "Claude turn completed"
		}
	case "Stop":
		phase, summary = "waiting_input", "Claude turn completed"
	case "SessionEnd":
		phase, summary = "completed", "Claude session ended"
	}
	return
}

var sensitiveValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[\s_-]*key\s*[=:]\s*)([^\s,;"'}]+)`),
	regexp.MustCompile(`(?i)(authorization\s*[=:]\s*bearer\s+)([^\s,;"'}]+)`),
	regexp.MustCompile(`(?i)(bearer\s+)([a-z0-9._~+/=-]{12,})`),
	regexp.MustCompile(`(?i)((?:access|refresh|session)[\s_-]*token\s*[=:]\s*)([^\s,;"'}]+)`),
	regexp.MustCompile(`(?i)(password\s*[=:]\s*)([^\s,;"'}]+)`),
}

func safeSummary(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	for _, pattern := range sensitiveValuePatterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	if value == "" {
		return ""
	}
	for index, r := range value {
		if strings.ContainsRune(".!?。！？", r) {
			value = value[:index+utf8.RuneLen(r)]
			break
		}
	}
	runes := []rune(value)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return value
}

func safeToolSummary(prefix, tool string) string {
	tool = strings.Join(strings.Fields(tool), " ")
	if tool == "" {
		return prefix
	}
	if len(tool) > 40 {
		tool = tool[:39] + "…"
	}
	return prefix + " " + tool
}
