package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_lint sends one document to a process plugin's linter action and
// returns sanitised diagnostics. Document text reaches the plugin only through
// this command, which the frontend calls for a language whose machine-local
// preference selects the linter, on a trigger the linter declares (open,
// save, or debounced type when the user also allowed lint-while-typing).
// Bounds: payload ≤ min(action maxPayloadBytes, payloadMaxBytes), timeout
// timeoutMs, concurrentPerPlugin slots, result ≤ 2 × resultMaxBytes of JSON.
// The top-level result is strict ({"diagnostics": [...]}); items are decoded
// leniently: unknown fields are ignored and an invalid item is dropped and
// counted, so one bad diagnostic does not hide the others. Diagnostics carry
// no URLs or code descriptions: markers never link anywhere.
var (
	linterPayloadMaxBytes = int(linterSpec.Limit("payloadMaxBytes"))
	linterResultMaxBytes  = int(linterSpec.Limit("resultMaxBytes"))
	linterDiagnosticsMax  = int(linterSpec.Limit("diagnosticsMax"))
	linterMessageMaxRunes = int(linterSpec.Limit("messageMaxRunes"))
	linterMessageMaxLines = int(linterSpec.Limit("messageMaxLines"))
	linterCodeMaxRunes    = int(linterSpec.Limit("codeMaxRunes"))
	linterSourceMaxRunes  = int(linterSpec.Limit("sourceMaxRunes"))
	linterLineMax         = int(linterSpec.Limit("lineMax"))
	linterColumnMax       = int(linterSpec.Limit("columnMax"))
	linterTimeout         = time.Duration(linterSpec.Limit("timeoutMs")) * time.Millisecond
	linterConcurrency     = int(linterSpec.Limit("concurrentPerPlugin"))
	linterSeverities      = linterSpec.List("severities")
	linterTags            = linterSpec.List("tags")
)

type lintRequest struct {
	PluginID     string `json:"pluginId"`
	LinterID     string `json:"linterId"`
	RequestID    string `json:"requestId"`
	LanguageID   string `json:"languageId"`
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath,omitempty"`
	Text         string `json:"text"`
	Trigger      string `json:"trigger"`
}

// lintPayload is what the plugin action receives.
type lintPayload struct {
	Linter       string `json:"linter"`
	LanguageID   string `json:"languageId"`
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath,omitempty"`
	Text         string `json:"text"`
	Trigger      string `json:"trigger"`
}

// LintDiagnostic is one sanitised diagnostic (1-based, end inclusive-exclusive
// like Monaco markers).
type LintDiagnostic struct {
	StartLineNumber int      `json:"startLineNumber"`
	StartColumn     int      `json:"startColumn"`
	EndLineNumber   int      `json:"endLineNumber"`
	EndColumn       int      `json:"endColumn"`
	Severity        string   `json:"severity"`
	Message         string   `json:"message"`
	Code            string   `json:"code,omitempty"`
	Source          string   `json:"source,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

type lintResponse struct {
	Diagnostics []LintDiagnostic `json:"diagnostics"`
	Dropped     int              `json:"dropped"`
	Truncated   bool             `json:"truncated"`
}

type lintActionResult struct {
	Diagnostics *[]json.RawMessage `json:"diagnostics"`
}

// lint validates the request, builds the bounded payload and invokes the
// linter's action.
func (manager *pluginManager) lint(ctx context.Context, request lintRequest) (lintResponse, error) {
	if err := normalizeLintRequest(&request); err != nil {
		return lintResponse{}, err
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return lintResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	record.mu.Lock()
	linter, lookupErr := linterForRequestLocked(record, request)
	var action ManifestUIAction
	var workspaceRead bool
	if lookupErr == nil {
		action, _ = contributedActionLocked(record, linter.Action)
		workspaceRead = manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return lintResponse{}, lookupErr
	}
	encoded, err := buildLintPayload(linter, request, workspaceRead)
	if err != nil {
		return lintResponse{}, err
	}
	limit := linterPayloadMaxBytes
	if action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return lintResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, linter %q allows %d", len(encoded), linter.ID, limit)
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: linterPointKey, EntryID: linter.ID, ActionID: linter.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: linterTimeout,
		MaxResultBytes: 2 * linterResultMaxBytes, Concurrency: linterConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := linterForRequestLocked(record, request)
			if err != nil {
				return err
			}
			if current.Action != linter.Action {
				return newContributionError(contributionErrUnavailable, "linter %q changed its action", linter.ID)
			}
			return nil
		},
	})
	if err != nil {
		return lintResponse{}, err
	}
	return parseLinterResult(raw)
}

// buildLintPayload encodes the action payload. The project-relative path is
// included only when the plugin declares workspace.read.
func buildLintPayload(linter LinterContribution, request lintRequest, workspaceRead bool) ([]byte, error) {
	payload := lintPayload{
		Linter: linter.ID, LanguageID: request.LanguageID, FileName: request.FileName,
		Text: request.Text, Trigger: request.Trigger,
	}
	if workspaceRead {
		payload.RelativePath = request.RelativePath
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// normalizeLintRequest reuses the formatter request rules for the file name,
// relative path and text, then checks the linter id and trigger.
func normalizeLintRequest(request *lintRequest) error {
	if !linterIDPattern.MatchString(request.LinterID) {
		return newContributionError(contributionErrInvalidRequest, "linterId %q is invalid", request.LinterID)
	}
	valid := false
	for _, trigger := range linterTriggers {
		valid = valid || trigger == request.Trigger
	}
	if !valid {
		return newContributionError(contributionErrInvalidRequest, "trigger %q must be one of %s", request.Trigger, strings.Join(linterTriggers, ", "))
	}
	shared := formatRequest{
		FormatterID: request.LinterID, LanguageID: request.LanguageID, FileName: request.FileName,
		RelativePath: request.RelativePath, Text: request.Text,
		Options: formatOptions{TabSize: 1, EOL: "\n"},
	}
	if err := normalizeFormatRequest(&shared); err != nil {
		return err
	}
	request.FileName, request.RelativePath = shared.FileName, shared.RelativePath
	return nil
}

// linterForRequestLocked finds the requested linter in the record's current
// generation and checks it covers the language and trigger. Called with
// record.mu held.
func linterForRequestLocked(record *pluginRecord, request lintRequest) (LinterContribution, error) {
	if record.manifest == nil {
		return LinterContribution{}, newContributionError(contributionErrNotFound, "plugin %q has no manifest", request.PluginID)
	}
	if resolved := record.manifest.contribution(linterPointKey); resolved != nil {
		for _, entry := range resolved.Entries {
			linter, ok := entry.(LinterContribution)
			if !ok || linter.ID != request.LinterID {
				continue
			}
			covered := false
			for _, language := range linter.Languages {
				covered = covered || language == request.LanguageID
			}
			if !covered {
				return linter, newContributionError(contributionErrUnavailable, "linter %q does not lint %q", linter.ID, request.LanguageID)
			}
			triggered := false
			for _, trigger := range linter.Triggers {
				triggered = triggered || trigger == request.Trigger
			}
			if !triggered {
				return linter, newContributionError(contributionErrInvalidRequest, "linter %q does not run on %q", linter.ID, request.Trigger)
			}
			return linter, nil
		}
	}
	return LinterContribution{}, newContributionError(contributionErrNotFound, "linter %q is not contributed by %q", request.LinterID, request.PluginID)
}

// parseLinterResult validates {"diagnostics": [...]} strictly at the top
// level and sanitises each diagnostic leniently.
func parseLinterResult(raw json.RawMessage) (lintResponse, error) {
	if !utf8.Valid(raw) {
		return lintResponse{}, newContributionError(contributionErrInvalidResult, "result must be valid UTF-8")
	}
	var result lintActionResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return lintResponse{}, newContributionError(contributionErrInvalidResult, "result must be {\"diagnostics\": [...]}: %v", err)
	}
	if result.Diagnostics == nil {
		return lintResponse{}, newContributionError(contributionErrInvalidResult, "result must be {\"diagnostics\": [...]}")
	}
	items := *result.Diagnostics
	response := lintResponse{Diagnostics: []LintDiagnostic{}}
	if len(items) > linterDiagnosticsMax {
		items = items[:linterDiagnosticsMax]
		response.Truncated = true
	}
	for _, item := range items {
		diagnostic, ok := sanitizeLintDiagnostic(item)
		if !ok {
			response.Dropped++
			continue
		}
		response.Diagnostics = append(response.Diagnostics, diagnostic)
	}
	return response, nil
}

type lintRawDiagnostic struct {
	StartLineNumber json.RawMessage `json:"startLineNumber"`
	StartColumn     json.RawMessage `json:"startColumn"`
	EndLineNumber   json.RawMessage `json:"endLineNumber"`
	EndColumn       json.RawMessage `json:"endColumn"`
	Severity        *string         `json:"severity"`
	Message         *string         `json:"message"`
	Code            *string         `json:"code"`
	Source          *string         `json:"source"`
	Tags            []string        `json:"tags"`
}

// sanitizeLintDiagnostic decodes one item leniently (unknown fields are
// ignored) and reports false when it must be dropped.
func sanitizeLintDiagnostic(raw json.RawMessage) (LintDiagnostic, bool) {
	var item lintRawDiagnostic
	if err := json.Unmarshal(raw, &item); err != nil {
		return LintDiagnostic{}, false
	}
	startLine, ok1 := lintPosition(item.StartLineNumber, linterLineMax)
	startColumn, ok2 := lintPosition(item.StartColumn, linterColumnMax)
	endLine, ok3 := lintPosition(item.EndLineNumber, linterLineMax)
	endColumn, ok4 := lintPosition(item.EndColumn, linterColumnMax)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return LintDiagnostic{}, false
	}
	if endLine < startLine || (endLine == startLine && endColumn < startColumn) {
		startLine, startColumn, endLine, endColumn = endLine, endColumn, startLine, startColumn
	}
	severity := "warning"
	if item.Severity != nil {
		if !lintListed(linterSeverities, *item.Severity) {
			return LintDiagnostic{}, false
		}
		severity = *item.Severity
	}
	if item.Message == nil {
		return LintDiagnostic{}, false
	}
	message := sanitizeLintMessage(*item.Message)
	if message == "" {
		return LintDiagnostic{}, false
	}
	diagnostic := LintDiagnostic{
		StartLineNumber: startLine, StartColumn: startColumn, EndLineNumber: endLine, EndColumn: endColumn,
		Severity: severity, Message: message,
	}
	var err error
	if item.Code != nil {
		if diagnostic.Code, err = validateContributionLine(*item.Code, linterCodeMaxRunes, false); err != nil {
			return LintDiagnostic{}, false
		}
	}
	if item.Source != nil {
		if diagnostic.Source, err = validateContributionLine(*item.Source, linterSourceMaxRunes, false); err != nil {
			return LintDiagnostic{}, false
		}
	}
	seen := map[string]bool{}
	for _, tag := range item.Tags {
		if !lintListed(linterTags, tag) {
			return LintDiagnostic{}, false
		}
		if !seen[tag] {
			seen[tag] = true
			diagnostic.Tags = append(diagnostic.Tags, tag)
		}
	}
	return diagnostic, true
}

// lintPosition accepts a JSON integer in 1..max.
func lintPosition(raw json.RawMessage, max int) (int, bool) {
	var value float64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	if value != math.Trunc(value) || value < 1 || value > float64(max) {
		return 0, false
	}
	return int(value), true
}

func lintListed(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// sanitizeLintMessage replaces unsafe characters (newlines stay) with a
// space, trims and bounds the message to messageMaxRunes and messageMaxLines,
// marking a cut with a trailing ellipsis.
func sanitizeLintMessage(value string) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, " ")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.Map(func(r rune) rune {
		if isUnsafeContributionRune(r, true, false) {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cut := false
	if lines := strings.Split(value, "\n"); len(lines) > linterMessageMaxLines {
		value = strings.TrimSpace(strings.Join(lines[:linterMessageMaxLines], "\n"))
		cut = true
	}
	if runes := []rune(value); len(runes) > linterMessageMaxRunes {
		value = strings.TrimSpace(string(runes[:linterMessageMaxRunes-1]))
		cut = true
	}
	if cut {
		if utf8.RuneCountInString(value) >= linterMessageMaxRunes {
			value = string([]rune(value)[:linterMessageMaxRunes-1])
		}
		value += "…"
	}
	return value
}

func pluginHostLintHandler(_, args string) (string, error) {
	var request lintRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_lint: invalid arguments: %v", err)
	}
	response, err := host().lint(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_lint", pluginHostLintHandler)
}
