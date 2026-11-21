package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mincode/app/backend/core"
	"mincode/app/plugins/api"
	"sync"
	"time"
	"unicode/utf8"
)

// Tickets are issued only to the trusted desktop bridge, never to plugin frames.
// Exact parameters are retained by the host and cannot be replaced at execution.
type aiPluginTicket struct {
	Record              *pluginRecord
	ApprovalEpoch       uint64
	Contract            string
	ID, Action, Payload string
	Expires             time.Time
}

var aiTickets = struct {
	sync.Mutex
	values map[string]aiPluginTicket
}{values: map[string]aiPluginTicket{}}

func init() {
	core.Register("plugin_host_describe", pluginDescribeHandler)
	core.Register("plugin_host_ai_prepare", pluginAIPrepareHandler)
	core.Register("plugin_host_ai_execute", pluginAIExecuteHandler)
}
func pluginDescribeHandler(_, args string) (string, error) {
	var input hostIDArgs
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	r, err := host().record(input.ID)
	if err != nil {
		return "", err
	}
	meta := recordMeta(r)
	r.mu.Lock()
	defer r.mu.Unlock()
	actions := []ManifestUIAction{}
	if r.manifest != nil {
		for _, declared := range r.manifest.Contributes.Actions {
			resolved, err := resolveAIActionPermissions(declared, r.capabilities)
			if err != nil {
				return "", err
			}
			actions = append(actions, resolved)
		}
	}
	return core.EncodeResult(map[string]any{"plugin": meta, "actions": actions, "note": "Only aiCallable actions with a supported inputSchema can be invoked by AI. Examples are illustrative, not authorization."})
}

// Deliberately limited schema dialect: unsupported keywords fail closed.
func validateAIValue(schema map[string]any, value any, path string) error {
	for key := range schema {
		switch key {
		case "type", "properties", "required", "additionalProperties", "description", "enum", "items",
			"minimum", "maximum", "minLength", "maxLength":
		default:
			return fmt.Errorf("%s: unsupported schema keyword %s", path, key)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, key := range required {
			if _, ok := object[fmt.Sprint(key)]; !ok {
				return fmt.Errorf("%s.%v is required", path, key)
			}
		}
		for key, v := range object {
			raw, ok := properties[key]
			if !ok {
				if schema["additionalProperties"] != true {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			child, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid property schema")
			}
			if err := validateAIValue(child, v, path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		length := float64(utf8.RuneCountInString(text))
		if limit, ok, err := schemaNumber(schema, "minLength", path); err != nil {
			return err
		} else if ok && length < limit {
			return fmt.Errorf("%s must contain at least %v characters", path, limit)
		}
		if limit, ok, err := schemaNumber(schema, "maxLength", path); err != nil {
			return err
		} else if ok && length > limit {
			return fmt.Errorf("%s must contain at most %v characters", path, limit)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "number", "integer":
		n, ok := value.(float64)
		if !ok {
			return fmt.Errorf("%s must be numeric", path)
		}
		if schema["type"] == "integer" && n != float64(int64(n)) {
			return fmt.Errorf("%s must be integer", path)
		}
		if limit, ok, err := schemaNumber(schema, "minimum", path); err != nil {
			return err
		} else if ok && n < limit {
			return fmt.Errorf("%s must be >= %v", path, limit)
		}
		if limit, ok, err := schemaNumber(schema, "maximum", path); err != nil {
			return err
		} else if ok && n > limit {
			return fmt.Errorf("%s must be <= %v", path, limit)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		child, ok := schema["items"].(map[string]any)
		if !ok {
			return errors.New("array items schema required")
		}
		for _, v := range items {
			if err := validateAIValue(child, v, path+"[]"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unsupported schema type", path)
	}
	if values, ok := schema["enum"].([]any); ok {
		found := false
		actual, _ := json.Marshal(value)
		for _, v := range values {
			expected, _ := json.Marshal(v)
			if string(actual) == string(expected) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	return nil
}

// schemaNumber reads a numeric bound; a non-numeric bound is a schema error, not "no limit".
func schemaNumber(schema map[string]any, key, path string) (float64, bool, error) {
	raw, exists := schema[key]
	if !exists {
		return 0, false, nil
	}
	n, ok := raw.(float64)
	if !ok {
		return 0, false, fmt.Errorf("%s: schema keyword %s must be a number", path, key)
	}
	return n, true, nil
}

func validateAIActionLocked(manager *pluginManager, r *pluginRecord, action, payload string) (ManifestUIAction, error) {
	if !manager.contributionsAvailableLocked(r) {
		return ManifestUIAction{}, errors.New("plugin is disabled or unavailable in the active workspace; failed plugins require explicit reload")
	}
	if r.manifest != nil {
		for _, a := range r.manifest.Contributes.Actions {
			if a.ID == action && a.AICallable && a.InputSchema != nil {
				var err error
				a, err = resolveAIActionPermissions(a, r.capabilities)
				if err != nil {
					return a, err
				}
				if a.Risk != "read" && a.Risk != "network" && a.Risk != "write" && a.Risk != "dangerous" {
					return a, errors.New("invalid action risk")
				}
				if a.MaxPayloadBytes <= 0 || len(payload) > a.MaxPayloadBytes {
					return a, errors.New("payload exceeds action limit")
				}
				var value any
				if err := json.Unmarshal([]byte(payload), &value); err != nil {
					return a, fmt.Errorf("invalid JSON payload: %w", err)
				}
				return a, validateAIValue(a.InputSchema, value, "payload")
			}
		}
	}
	return ManifestUIAction{}, errors.New("action is not declared AI-callable with a parameter schema; use plugin.describe")
}
func pluginAIPrepareHandler(_, args string) (string, error) {
	var input hostInvokeArgs
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	record, err := host().record(input.ID)
	if err != nil {
		return "", err
	}
	record.mu.Lock()
	a, err := validateAIActionLocked(host(), record, input.Action, input.Payload)
	epoch := record.approvalEpoch
	record.mu.Unlock()
	if err != nil {
		return "", err
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(bytes)
	aiTickets.Lock()
	defer aiTickets.Unlock()
	for key, v := range aiTickets.values {
		if time.Now().After(v.Expires) {
			delete(aiTickets.values, key)
		}
	}
	if len(aiTickets.values) >= 128 {
		return "", errors.New("too many pending plugin approvals")
	}
	aiTickets.values[ticket] = aiPluginTicket{Record: record, ApprovalEpoch: epoch, ID: input.ID, Action: input.Action, Payload: input.Payload, Expires: time.Now().Add(2 * time.Minute), Contract: aiContractFingerprint(a)}
	return core.EncodeResult(map[string]any{"ticket": ticket, "risk": a.Risk, "description": a.Description, "permissions": a.Permissions})
}
func pluginAIExecuteHandler(_, args string) (string, error) {
	var input struct {
		Ticket   string `json:"ticket"`
		Approved bool   `json:"approved"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	aiTickets.Lock()
	ticket, ok := aiTickets.values[input.Ticket]
	delete(aiTickets.values, input.Ticket)
	aiTickets.Unlock()
	if !ok || time.Now().After(ticket.Expires) || !input.Approved {
		return "", errors.New("plugin approval missing, expired, rejected or already consumed")
	}
	var action ManifestUIAction
	result, err := host().invokeValidated(context.Background(), ticket.ID, ticket.Action, ticket.Payload, func(record *pluginRecord) error {
		var err error
		action, err = ticket.validateLocked(record)
		return err
	})
	core.Emit("plugin:action-completed", map[string]any{"id": ticket.ID, "action": ticket.Action, "ok": err == nil, "risk": action.Risk})
	if err != nil {
		return "", fmt.Errorf("plugin %s action %s failed: %w", ticket.ID, ticket.Action, err)
	}
	if !json.Valid([]byte(result)) {
		return "", errors.New("plugin returned invalid JSON")
	}
	return core.EncodeResult(map[string]any{"pluginId": ticket.ID, "action": ticket.Action, "summary": "插件操作已执行；请检查 result 中的业务结果。", "result": json.RawMessage(result)})
}

// Called under the record lock both before lazy activation and before dispatch.
// Runtime generation is deliberately excluded: the first lazy start is authorized.
func (ticket aiPluginTicket) validateLocked(record *pluginRecord) (ManifestUIAction, error) {
	if record != ticket.Record || record.approvalEpoch != ticket.ApprovalEpoch {
		return ManifestUIAction{}, errors.New("plugin approval invalidated by lifecycle or package change; confirm again")
	}
	if time.Now().After(ticket.Expires) {
		return ManifestUIAction{}, errors.New("plugin approval expired during activation")
	}
	action, err := validateAIActionLocked(host(), record, ticket.Action, ticket.Payload)
	if err != nil {
		return action, err
	}
	if aiContractFingerprint(action) != ticket.Contract {
		return action, errors.New("plugin action contract changed; confirm again")
	}
	return action, nil
}

func aiContractFingerprint(action ManifestUIAction) string {
	data, _ := json.Marshal(action)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Policy is capability-based, never plugin-ID or action-name based.
// Omitted permissions inherit the complete plugin capability set for compatibility,
// except capabilities that are not action permissions (database.driver, ai.config,
// backup, remote.context, ui.wasm): those describe what the host hands the plugin
// or what its page may ask the host frame for or run, not what an action may do, so they are
// never inherited and an action that lists one is rejected.
func resolveAIActionPermissions(action ManifestUIAction, capabilities []api.Capability) (ManifestUIAction, error) {
	if action.Permissions == nil {
		action.Permissions = make([]api.Capability, 0, len(capabilities))
		for _, capability := range capabilities {
			if capability != api.CapabilityDatabaseDriver && capability != api.CapabilityAIConfig && capability != api.CapabilityBackup && capability != api.CapabilityRemoteContext && capability != api.CapabilityUIWasm {
				action.Permissions = append(action.Permissions, capability)
			}
		}
	}
	granted := map[api.Capability]bool{}
	for _, capability := range capabilities {
		granted[capability] = true
	}
	rank := map[string]int{"read": 0, "network": 1, "write": 2, "dangerous": 3}
	if action.Risk == "" {
		action.Risk = "read"
	}
	if _, ok := rank[action.Risk]; !ok {
		return action, errors.New("invalid action risk")
	}
	seen := map[api.Capability]bool{}
	for _, permission := range action.Permissions {
		if !granted[permission] {
			return action, fmt.Errorf("action %s requests undeclared plugin permission %s", action.ID, permission)
		}
		if seen[permission] {
			return action, fmt.Errorf("duplicate action permission %s", permission)
		}
		seen[permission] = true
		minimum := "read"
		switch permission {
		case api.CapabilityCommands, api.CapabilityUI, api.CapabilityWorkspaceRead, api.CapabilityDatabaseSchema, api.CapabilitySystemAssociations:
		case api.CapabilityNetwork:
			minimum = "network"
		case api.CapabilityStorage, api.CapabilityWorkspaceWrite, api.CapabilityNotifications, api.CapabilityAI, api.CapabilityPlugins:
			minimum = "write"
		case api.CapabilityShell, api.CapabilitySecrets, api.CapabilityClipboard:
			minimum = "dangerous"
		case api.CapabilityDatabaseDriver, api.CapabilityAIActivity, api.CapabilityAITasks, api.CapabilityAIConfig, api.CapabilityBackup, api.CapabilityRemoteContext, api.CapabilityUIWasm:
			return action, fmt.Errorf("action %s: %s is not an action permission", action.ID, permission)
		default:
			return action, fmt.Errorf("unsupported action permission %s", permission)
		}
		if rank[minimum] > rank[action.Risk] {
			action.Risk = minimum
		}
	}
	return action, nil
}
