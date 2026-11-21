package browser

import (
	"encoding/json"
	"errors"
)

// Structured browser tool error codes. The Vue side parses these from the
// error text returned by embedded_browser_ai_execute, so the values are fixed.
const (
	errCodeDialogPending           = "dialog_pending"
	errCodeDialogAlreadyHandled    = "dialog_already_handled"
	errCodeTabNotShared            = "browser_tab_not_shared"
	errCodeTabNotVisible           = "tab_not_visible"
	errCodeFileChooserRequiresUser = "file_chooser_requires_user"
	errCodeNoHistory               = "no_history"
	errCodeUnsupportedOnPlatform   = "unsupported_on_platform"
)

// browserToolError is a tool failure the front end can recognise by Code.
type browserToolError struct {
	Code    string
	Message string
	Details map[string]any
}

func newToolError(code, message string, details map[string]any) *browserToolError {
	return &browserToolError{Code: code, Message: message, Details: details}
}

func (e *browserToolError) Error() string { return e.Message }

// encoded renders the error as {"code":...,"message":...,"details":...}.
func (e *browserToolError) encoded() string {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	payload, err := json.Marshal(struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}{e.Code, e.Message, details})
	if err != nil {
		payload, _ = json.Marshal(map[string]any{"code": e.Code, "message": e.Message, "details": map[string]any{}})
	}
	return string(payload)
}

// encodeToolError turns a browserToolError into its JSON error text and leaves
// every other error untouched.
func encodeToolError(err error) error {
	var toolErr *browserToolError
	if errors.As(err, &toolErr) {
		return errors.New(toolErr.encoded())
	}
	return err
}
