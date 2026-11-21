package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
)

const customAiPromptsKey = "ai-prompts.custom.v1"

// Reusable templates are global plugin-owned resources, not workspace drafts.
// Ordinary KV/import can replace this document, so every read validates it.
type customAiPrompt struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Body        string `json:"body"`
	Enabled     bool   `json:"enabled"`
}
type customAiPromptDocument struct {
	Version  int              `json:"version"`
	Revision uint64           `json:"revision"`
	Prompts  []customAiPrompt `json:"prompts"`
}
type customAiPromptSnapshot struct {
	customAiPromptDocument
	Builtins []AiPromptContribution `json:"builtins"`
	Entries  []AiPromptContribution `json:"-"`
	Capacity int                    `json:"capacity"`
}

var customAiPromptID = regexp.MustCompile(`^custom-[a-f0-9]{32}$`)
var customAiPromptLocks sync.Map

func customAiPromptLock(id string) *sync.Mutex {
	lock, _ := customAiPromptLocks.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func validateCustomAiPrompt(p customAiPrompt) (customAiPrompt, error) {
	var err error
	if !customAiPromptID.MatchString(p.ID) {
		return p, errors.New("invalid custom prompt id")
	}
	if p.Title, err = validateAiPromptLine(p.Title, aiPromptMaxTitleRunes, true); err != nil {
		return p, fmt.Errorf("title: %w", err)
	}
	if p.Description, err = validateAiPromptLine(p.Description, aiPromptMaxDescription, false); err != nil {
		return p, fmt.Errorf("description: %w", err)
	}
	if p.Category, err = validateAiPromptLine(p.Category, 30, false); err != nil {
		return p, fmt.Errorf("category: %w", err)
	}
	p.Body, _, err = validateAiPromptBody("prompt", p.Body)
	return p, err
}

func readCustomAiPrompts(manifest Manifest) (customAiPromptSnapshot, error) {
	out := customAiPromptSnapshot{customAiPromptDocument: customAiPromptDocument{Version: 1, Prompts: []customAiPrompt{}}, Builtins: manifest.aiPrompts(), Entries: []AiPromptContribution{}}
	out.Capacity = aiPromptSpec.MaxEntries - len(out.Builtins)
	raw, found, err := pluginKVGetRaw(manifest.ID, customAiPromptsKey)
	if err != nil {
		return out, err
	}
	if found {
		out.customAiPromptDocument = customAiPromptDocument{}
		if err := decodeStrictJSON(raw, &out.customAiPromptDocument, true); err != nil {
			return out, errors.New("invalid saved custom prompts document")
		}
	}
	return validateCustomAiPromptSnapshot(out)
}

func validateCustomAiPromptSnapshot(out customAiPromptSnapshot) (customAiPromptSnapshot, error) {
	var err error
	out.Entries = []AiPromptContribution{}
	if out.Version != 1 || out.Revision > 9007199254740990 || out.Prompts == nil || len(out.Prompts) > out.Capacity {
		return out, errors.New("invalid custom prompts version, revision or capacity")
	}
	total := 0
	seen := map[string]bool{}
	for _, p := range out.Builtins {
		total += len(p.Body)
		seen[p.ID] = true
	}
	for i, p := range out.Prompts {
		p, err = validateCustomAiPrompt(p)
		if err != nil {
			return out, fmt.Errorf("saved custom prompt %d: %w", i, err)
		}
		if seen[p.ID] {
			return out, errors.New("duplicate custom prompt id")
		}
		seen[p.ID] = true
		total += len(p.Body)
		if total > aiPromptMaxPluginBytes {
			return out, errors.New("custom prompts exceed plugin body budget")
		}
		out.Prompts[i] = p
		if p.Enabled {
			_, variables, _ := validateAiPromptBody("prompt", p.Body)
			out.Entries = append(out.Entries, AiPromptContribution{ID: p.ID, Kind: "prompt", Title: p.Title, Description: p.Description, Body: p.Body, Variables: variables})
		}
	}
	// Bound the actual management wire shape before any commit. Entries belong
	// only to contribution overlays: duplicating them here can exceed the frame.
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) >= pluginRPCMaxMessage-4096 {
		return out, errors.New("custom prompt response exceeds transport budget")
	}
	return out, nil
}

func mutateCustomAiPrompts(manifest Manifest, action string, raw json.RawMessage) (customAiPromptSnapshot, error) {
	lock := customAiPromptLock(manifest.ID)
	lock.Lock()
	defer lock.Unlock()
	current, err := readCustomAiPrompts(manifest)
	if err != nil {
		return current, err
	}
	var params struct {
		Revision *uint64         `json:"revision"`
		Prompt   *customAiPrompt `json:"prompt,omitempty"`
		ID       string          `json:"id,omitempty"`
	}
	if err := decodeStrictJSON(raw, &params, true); err != nil {
		return current, errors.New("invalid custom prompt request")
	}
	if params.Revision == nil || *params.Revision != current.Revision {
		return current, errors.New("revision conflict: reload before saving")
	}
	switch action {
	case "save":
		if params.Prompt == nil || params.ID != "" {
			return current, errors.New("prompt required")
		}
		p := *params.Prompt
		index := -1
		if p.ID == "" {
			if len(current.Prompts) >= current.Capacity {
				return current, errors.New("custom prompt capacity reached")
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return current, err
			}
			p.ID = "custom-" + hex.EncodeToString(nonce[:])
		} else {
			for i, item := range current.Prompts {
				if item.ID == p.ID {
					index = i
					break
				}
			}
			if index < 0 {
				return current, errors.New("custom prompt not found")
			}
		}
		p, err = validateCustomAiPrompt(p)
		if err != nil {
			return current, err
		}
		total := len(p.Body)
		for _, item := range current.Builtins {
			total += len(item.Body)
		}
		for i, item := range current.Prompts {
			if i != index {
				total += len(item.Body)
			}
		}
		if total > aiPromptMaxPluginBytes {
			return current, errors.New("custom prompts exceed plugin body budget")
		}
		if index < 0 {
			current.Prompts = append(current.Prompts, p)
		} else {
			current.Prompts[index] = p
		}
	case "delete":
		if params.Prompt != nil {
			return current, errors.New("unexpected prompt")
		}
		index := -1
		for i, item := range current.Prompts {
			if item.ID == params.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return current, errors.New("custom prompt not found")
		}
		current.Prompts = append(current.Prompts[:index], current.Prompts[index+1:]...)
	default:
		return current, errors.New("unknown custom prompt operation")
	}
	current.Revision++
	if current.Revision > 9007199254740990 {
		return current, errors.New("custom prompt revision exhausted")
	}
	current, err = validateCustomAiPromptSnapshot(current)
	if err != nil {
		return current, err
	}
	encoded, err := json.Marshal(current.customAiPromptDocument)
	if err != nil {
		return current, err
	}
	if err = pluginKVSetRepository(manifest.ID, customAiPromptsKey, encoded); err != nil {
		return current, err
	}
	return current, nil
}

// Generic replacement/import is authoritative, but never reuses a revision
// from an open editor. Keep malformed imports observable for read-time errors.
func replaceCustomAiPromptsRaw(id string, raw json.RawMessage, deleting bool) error {
	lock := customAiPromptLock(id)
	lock.Lock()
	defer lock.Unlock()
	encoded, err := customAiPromptReplacement(id, raw, deleting)
	if err != nil {
		return err
	}
	return pluginKVSetRepository(id, customAiPromptsKey, encoded)
}

// Caller holds the document lock, including namespace import transactions.
func customAiPromptReplacement(id string, raw json.RawMessage, deleting bool) (json.RawMessage, error) {
	old, _, err := pluginKVGetRaw(id, customAiPromptsKey)
	if err != nil {
		return nil, err
	}
	var previous struct {
		Revision uint64 `json:"revision"`
	}
	_ = json.Unmarshal(old, &previous)
	revision := previous.Revision
	var incoming map[string]json.RawMessage
	if deleting {
		raw = json.RawMessage(`{"version":1,"prompts":[]}`)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("invalid custom prompt storage value")
	}
	if json.Unmarshal(raw, &incoming) != nil || incoming == nil {
		incoming = map[string]json.RawMessage{"version": json.RawMessage(`0`), "invalidDocument": raw}
	}
	var supplied uint64
	_ = json.Unmarshal(incoming["revision"], &supplied)
	if supplied > revision {
		revision = supplied
	}
	if revision >= 9007199254740990 {
		return nil, errors.New("custom prompt revision exhausted")
	}
	incoming["revision"], _ = json.Marshal(revision + 1)
	encoded, err := json.Marshal(incoming)
	if len(encoded) > pluginKVMaxValueBytes {
		return nil, errors.New("custom prompts exceed storage budget")
	}
	return encoded, err
}
