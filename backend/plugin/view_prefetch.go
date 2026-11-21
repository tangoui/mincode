package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ManifestViewPrefetch is contributes.views[].prefetch (manifest API v2): one
// read-only UI action, with an optional static JSON payload, that the host
// invokes for the view's first screen as soon as a UI session exists, in
// parallel with loading the frame. The call goes through the same session,
// nonce, generation and action checks as a page invoke (plugin_host_ui_invoke),
// and the result is delivered only to that session's page after its
// handshake. Only an action that a page could call without any confirmation
// qualifies: uiCallable, risk "read", no host result handling.
type ManifestViewPrefetch struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// pluginUIPrefetchDTO is the session's validated prefetch: payload is the
// exact JSON string forwarded to the plugin ("null" when none was declared).
type pluginUIPrefetchDTO struct {
	Action  string `json:"action"`
	Payload string `json:"payload"`
}

// normalizeViewPrefetch validates and canonicalizes view.Prefetch against the
// manifest's already validated actions. tools/mincode-cli/view_prefetch.go
// mirrors these rules.
func normalizeViewPrefetch(manifest *Manifest, view *ManifestView) error {
	prefetch := view.Prefetch
	if prefetch == nil {
		return nil
	}
	prefetch.Action = strings.TrimSpace(prefetch.Action)
	if !contributionIDPattern.MatchString(prefetch.Action) {
		return fmt.Errorf("prefetch action %q is invalid", prefetch.Action)
	}
	var action *ManifestUIAction
	for index := range manifest.Contributes.Actions {
		if manifest.Contributes.Actions[index].ID == prefetch.Action {
			action = &manifest.Contributes.Actions[index]
			break
		}
	}
	if action == nil {
		return fmt.Errorf("prefetch action %q is not declared in contributes.actions", prefetch.Action)
	}
	if !action.UICallable {
		return fmt.Errorf("prefetch action %q must be uiCallable", prefetch.Action)
	}
	if action.Risk != "read" {
		return fmt.Errorf("prefetch action %q must have risk read", prefetch.Action)
	}
	if action.ResultHandling != "" {
		return fmt.Errorf("prefetch action %q must not use resultHandling", prefetch.Action)
	}
	payload := bytes.TrimSpace(prefetch.Payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		prefetch.Payload = nil
		return nil
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, payload); err != nil {
		return fmt.Errorf("prefetch payload is not valid JSON: %w", err)
	}
	if compacted.Len() > action.MaxPayloadBytes {
		return fmt.Errorf("prefetch payload exceeds the %d-byte limit of action %q", action.MaxPayloadBytes, prefetch.Action)
	}
	prefetch.Payload = json.RawMessage(compacted.Bytes())
	return nil
}

func viewPrefetchDTO(view *ManifestView) *pluginUIPrefetchDTO {
	if view == nil || view.Prefetch == nil || view.Prefetch.Action == "" {
		return nil
	}
	payload := "null"
	if len(view.Prefetch.Payload) > 0 {
		payload = string(view.Prefetch.Payload)
	}
	return &pluginUIPrefetchDTO{Action: view.Prefetch.Action, Payload: payload}
}
