package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_format sends one document (or range) to a process plugin's
// formatter action and returns the validated replacement text. Document text
// reaches the plugin only through this command, which the frontend calls on
// an explicit Format action or on save after the user opted in for that
// language. Bounds: payload ≤ min(action maxPayloadBytes, payloadMaxBytes),
// timeout timeoutMs (saveTimeoutMs for mode "save"), concurrentPerPlugin
// slots, result text ≤ resultMaxBytes and ≤ 4 × input + 64 KiB. Cancellation
// goes through plugin_host_contribution_cancel; the plugin is not notified
// (runtime protocol v1 has no cancel message) and a late result is dropped.
var (
	formatterPayloadMaxBytes = int(formatterSpec.Limit("payloadMaxBytes"))
	formatterResultMaxBytes  = int(formatterSpec.Limit("resultMaxBytes"))
	formatterTimeout         = time.Duration(formatterSpec.Limit("timeoutMs")) * time.Millisecond
	formatterSaveTimeout     = time.Duration(formatterSpec.Limit("saveTimeoutMs")) * time.Millisecond
	formatterConcurrency     = int(formatterSpec.Limit("concurrentPerPlugin"))
)

const (
	formatterFileNameMaxBytes     = 255
	formatterRelativePathMaxBytes = 1024
	formatterTabSizeMax           = 32
	// formatterResultSlackBytes is the growth allowance on top of 4 × input.
	formatterResultSlackBytes = 64 << 10
)

type formatRange struct {
	StartLineNumber int `json:"startLineNumber"`
	StartColumn     int `json:"startColumn"`
	EndLineNumber   int `json:"endLineNumber"`
	EndColumn       int `json:"endColumn"`
}

type formatOptions struct {
	TabSize      int    `json:"tabSize"`
	InsertSpaces bool   `json:"insertSpaces"`
	EOL          string `json:"eol"`
}

type formatRequest struct {
	PluginID     string        `json:"pluginId"`
	FormatterID  string        `json:"formatterId"`
	RequestID    string        `json:"requestId"`
	LanguageID   string        `json:"languageId"`
	FileName     string        `json:"fileName"`
	RelativePath string        `json:"relativePath,omitempty"`
	Text         string        `json:"text"`
	Range        *formatRange  `json:"range,omitempty"`
	Options      formatOptions `json:"options"`
	Mode         string        `json:"mode,omitempty"`
}

// formatPayload is what the plugin action receives.
type formatPayload struct {
	Formatter    string        `json:"formatter"`
	LanguageID   string        `json:"languageId"`
	FileName     string        `json:"fileName"`
	RelativePath string        `json:"relativePath,omitempty"`
	Text         string        `json:"text"`
	Range        *formatRange  `json:"range,omitempty"`
	Options      formatOptions `json:"options"`
	Mode         string        `json:"mode"`
}

type formatResponse struct {
	Text      *string `json:"text,omitempty"`
	Unchanged bool    `json:"unchanged"`
}

type formatActionResult struct {
	Text      *string `json:"text"`
	Unchanged bool    `json:"unchanged"`
}

// format validates the request, builds the bounded payload and invokes the
// formatter's action.
func (manager *pluginManager) format(ctx context.Context, request formatRequest) (formatResponse, error) {
	if err := normalizeFormatRequest(&request); err != nil {
		return formatResponse{}, err
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return formatResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	// Resolve the formatter once to build the payload; Validate re-checks it
	// under record.mu before activation and dispatch (generation pinning).
	record.mu.Lock()
	formatter, lookupErr := formatterForRequestLocked(record, request)
	var action ManifestUIAction
	var workspaceRead bool
	if lookupErr == nil {
		action, _ = contributedActionLocked(record, formatter.Action)
		workspaceRead = manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return formatResponse{}, lookupErr
	}
	encoded, err := buildFormatPayload(formatter, request, workspaceRead)
	if err != nil {
		return formatResponse{}, err
	}
	limit := formatterPayloadMaxBytes
	if action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return formatResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, formatter %q allows %d", len(encoded), formatter.ID, limit)
	}
	timeout := formatterTimeout
	if request.Mode == "save" {
		timeout = formatterSaveTimeout
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: formatterPointKey, EntryID: formatter.ID, ActionID: formatter.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: timeout,
		// JSON escaping may inflate the text; the decoded text is bounded below.
		MaxResultBytes: 2 * formatterResultMaxBytes, Concurrency: formatterConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := formatterForRequestLocked(record, request)
			if err != nil {
				return err
			}
			if current.Action != formatter.Action {
				return newContributionError(contributionErrUnavailable, "formatter %q changed its action", formatter.ID)
			}
			return nil
		},
	})
	if err != nil {
		return formatResponse{}, err
	}
	return parseFormatterResult(raw, request.Text)
}

// buildFormatPayload encodes the action payload. The project-relative path
// is included only when the plugin declares workspace.read; absolute paths
// never reach the plugin.
func buildFormatPayload(formatter FormatterContribution, request formatRequest, workspaceRead bool) ([]byte, error) {
	payload := formatPayload{
		Formatter: formatter.ID, LanguageID: request.LanguageID, FileName: request.FileName,
		Text: request.Text, Range: request.Range, Options: request.Options, Mode: request.Mode,
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

func normalizeFormatRequest(request *formatRequest) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	if !formatterIDPattern.MatchString(request.FormatterID) {
		return invalid("formatterId %q is invalid", request.FormatterID)
	}
	if !formatterLanguagePattern.MatchString(request.LanguageID) {
		return invalid("languageId %q is invalid", request.LanguageID)
	}
	switch request.Mode {
	case "":
		request.Mode = "explicit"
	case "explicit", "save":
	default:
		return invalid("mode %q must be explicit or save", request.Mode)
	}
	name := strings.ReplaceAll(request.FileName, "\\", "/")
	name = path.Base(strings.TrimRight(name, "/"))
	if name == "." || name == "/" || name == "" || len(name) > formatterFileNameMaxBytes || !utf8.ValidString(name) || checkContributionRunes(name, false, false) != nil {
		return invalid("fileName is invalid")
	}
	request.FileName = name
	if request.RelativePath != "" {
		relative := strings.ReplaceAll(request.RelativePath, "\\", "/")
		if len(relative) > formatterRelativePathMaxBytes || !utf8.ValidString(relative) || checkContributionRunes(relative, false, false) != nil ||
			strings.HasPrefix(relative, "/") || strings.Contains(relative, ":") || path.Clean(relative) != relative || relative == "." || strings.HasPrefix(relative, "../") || relative == ".." {
			return invalid("relativePath must be a clean project-relative path")
		}
		request.RelativePath = relative
	}
	if !utf8.ValidString(request.Text) {
		return invalid("text must be valid UTF-8")
	}
	if request.Options.TabSize < 1 || request.Options.TabSize > formatterTabSizeMax {
		return invalid("options.tabSize must be 1-%d", formatterTabSizeMax)
	}
	if request.Options.EOL != "\n" && request.Options.EOL != "\r\n" {
		return invalid("options.eol must be \\n or \\r\\n")
	}
	if r := request.Range; r != nil {
		if r.StartLineNumber < 1 || r.StartColumn < 1 || r.EndLineNumber < r.StartLineNumber || r.EndColumn < 1 ||
			(r.EndLineNumber == r.StartLineNumber && r.EndColumn < r.StartColumn) {
			return invalid("range is invalid")
		}
	}
	return nil
}

// formatterForRequestLocked finds the requested formatter in the record's
// current generation and checks it covers the language (and ranges when a
// range is requested). Called with record.mu held.
func formatterForRequestLocked(record *pluginRecord, request formatRequest) (FormatterContribution, error) {
	if record.manifest == nil {
		return FormatterContribution{}, newContributionError(contributionErrNotFound, "plugin %q has no manifest", request.PluginID)
	}
	resolved := record.manifest.contribution(formatterPointKey)
	if resolved != nil {
		for _, entry := range resolved.Entries {
			formatter, ok := entry.(FormatterContribution)
			if !ok || formatter.ID != request.FormatterID {
				continue
			}
			covered := false
			for _, language := range formatter.Languages {
				covered = covered || language == request.LanguageID
			}
			if !covered {
				return formatter, newContributionError(contributionErrUnavailable, "formatter %q does not format %q", formatter.ID, request.LanguageID)
			}
			if request.Range != nil && !formatter.Range {
				return formatter, newContributionError(contributionErrInvalidRequest, "formatter %q does not format ranges", formatter.ID)
			}
			return formatter, nil
		}
	}
	return FormatterContribution{}, newContributionError(contributionErrNotFound, "formatter %q is not contributed by %q", request.FormatterID, request.PluginID)
}

// parseFormatterResult validates {"text": string} or {"unchanged": true}.
func parseFormatterResult(raw json.RawMessage, input string) (formatResponse, error) {
	if !utf8.Valid(raw) {
		return formatResponse{}, newContributionError(contributionErrInvalidResult, "result must be valid UTF-8")
	}
	var result formatActionResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return formatResponse{}, newContributionError(contributionErrInvalidResult, "result must be {\"text\": string} or {\"unchanged\": true}: %v", err)
	}
	if result.Unchanged {
		if result.Text != nil {
			return formatResponse{}, newContributionError(contributionErrInvalidResult, "result must not combine text and unchanged")
		}
		return formatResponse{Unchanged: true}, nil
	}
	if result.Text == nil {
		return formatResponse{}, newContributionError(contributionErrInvalidResult, "result must be {\"text\": string} or {\"unchanged\": true}")
	}
	text := *result.Text
	growth := 4*len(input) + formatterResultSlackBytes
	if len(text) > formatterResultMaxBytes || len(text) > growth {
		return formatResponse{}, newContributionError(contributionErrInvalidResult, "formatted text is %d bytes, the limit is %d", len(text), min(formatterResultMaxBytes, growth))
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return formatResponse{}, newContributionError(contributionErrInvalidResult, "formatted text must be valid UTF-8 without NUL")
	}
	if text == input {
		return formatResponse{Unchanged: true}, nil
	}
	return formatResponse{Text: &text}, nil
}

func pluginHostFormatHandler(_, args string) (string, error) {
	var request formatRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_format: invalid arguments: %v", err)
	}
	response, err := host().format(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_format", pluginHostFormatHandler)
}
