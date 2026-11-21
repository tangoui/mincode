package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Mode selects how a restored section combines with the data on this device.
type Mode string

const (
	// ModeOverwrite replaces local data with the backup; local-only entries
	// are removed.
	ModeOverwrite Mode = "overwrite"
	// ModeMerge adds and restores entries from the backup and keeps
	// local-only entries.
	ModeMerge Mode = "merge"
)

// Change kinds reported to the restore preview.
const (
	ChangeAdd    = "add"
	ChangeUpdate = "update"
	ChangeRemove = "remove"
	ChangeKeep   = "keep"
)

// Change describes one entry a restore adds, updates, removes or keeps.
// Label is an element name, an id, a key or a path; it never carries a value.
type Change struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// secretFieldNames mirrors SECRET_FIELD_NAMES in
// vue-app/src/domain/settings/config-sync.ts; TestSecretNamesMatchFrontend
// keeps the two lists identical.
var secretFieldNames = map[string]struct{}{
	"password":           {},
	"passphrase":         {},
	"token":              {},
	"authtoken":          {},
	"accesstoken":        {},
	"refreshtoken":       {},
	"apikey":             {},
	"apikeyvalue":        {},
	"clientsecret":       {},
	"oauth2clientsecret": {},
	"oauth2password":     {},
	"oauth2accesstoken":  {},
	"privatekey":         {},
	"totpuri":            {},
	"licensekey":         {},
	"authorization":      {},
	"cookie":             {},
}

// normalizedFieldName matches the frontend: drop everything except ASCII
// letters and digits, then lowercase.
func normalizedFieldName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		}
	}
	return string(out)
}

// blankedMapFields are string maps whose every value the frontend blanks in a
// backup without secrets, whatever the child names are: MCP server env and
// headers (sanitizeSettingsForBrowser in
// vue-app/src/services/settings-persistence.ts).
var blankedMapFields = map[string]struct{}{
	"env":     {},
	"headers": {},
}

// HTTP storage keys, following isHttpStorageKey in
// vue-app/src/services/config-sync-persistence.ts.
const (
	httpStateKey     = "app_http_state_v1"
	httpResourcesKey = "app_http_resources_v1"
	httpStatePrefix  = httpStateKey + "__workspace__"
)

// isHTTPStorageKey reports whether a storage key holds HTTP client state,
// whose request URLs, bodies and row values the frontend blanks in a backup
// without secrets (sanitizeHttpStateForBrowser). Restores rehydrate those
// blanks like mergeRedactedHttpValue in
// vue-app/src/services/config-sync-persistence.ts.
func isHTTPStorageKey(key string) bool {
	return key == httpResourcesKey || key == httpStateKey || strings.HasPrefix(key, httpStatePrefix)
}

// isSecretFieldName reports whether a field name carries credentials.
func isSecretFieldName(name string) bool {
	_, ok := secretFieldNames[normalizedFieldName(name)]
	return ok
}

// MergeValue combines the backup value of one frontend storage key with the
// local value. A nil or JSON null local means the key is absent on this
// device; a nil incoming means the backup has no value, and local is returned
// unchanged.
//
// Arrays whose elements are all objects with a unique non-empty string "id"
// are matched by id: matched elements are restored as in the backup, new ones
// are added, and local-only ones are kept (merge) or removed (overwrite). The
// result lists backup elements first, then local-only ones. Other arrays are
// replaced by the backup. Objects merge key by key, recursively; overwrite
// keeps only the backup's keys.
//
// When includesSecrets is false, a secret-named field (at any depth) that is
// empty or missing in the backup keeps its non-empty local value in both
// modes.
func MergeValue(key string, local, incoming json.RawMessage, mode Mode, includesSecrets bool) (json.RawMessage, []Change, error) {
	if mode != ModeOverwrite && mode != ModeMerge {
		return nil, nil, fmt.Errorf("merge %q: unknown mode %q", key, mode)
	}
	if len(incoming) == 0 {
		return local, nil, nil
	}
	if !json.Valid(incoming) {
		return nil, nil, fmt.Errorf("merge %q: backup value is not valid JSON", key)
	}
	if len(local) > 0 && !json.Valid(local) {
		return nil, nil, fmt.Errorf("merge %q: local value is not valid JSON", key)
	}
	if isAbsent(local) {
		local = nil
	}
	if !includesSecrets && local != nil && isHTTPStorageKey(key) {
		// Rehydrating the backup before merging gives the same result as
		// rehydrating the merged value: backup nodes keep their positions and
		// everything else already comes from this device. Doing it first
		// keeps the changes accurate.
		rehydrated, err := rehydrateBlanks(incoming, local)
		if err != nil {
			return nil, nil, fmt.Errorf("merge %q: %w", key, err)
		}
		incoming = rehydrated
	}
	m := &merger{key: key, includesSecrets: includesSecrets}
	out, err := m.value("", "", local, incoming, mode, false, false)
	if err != nil {
		return nil, nil, fmt.Errorf("merge %q: %w", key, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		return nil, nil, fmt.Errorf("merge %q: compact result: %w", key, err)
	}
	return json.RawMessage(compact.Bytes()), m.changes, nil
}

type merger struct {
	key             string
	includesSecrets bool
	// quiet suppresses nested changes while a matched list element is
	// rebuilt; the element reports one change of its own.
	quiet   bool
	changes []Change
}

func (m *merger) record(kind, label string) {
	if !m.quiet {
		m.changes = append(m.changes, Change{Kind: kind, Label: label})
	}
}

// label names a node at path; the root is named by the storage key.
func (m *merger) label(path string) string {
	if path == "" {
		return m.key
	}
	return path
}

func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// keepsLocalSecret reports whether a secret node must keep its local value
// because the backup carries no secrets and has nothing for it.
func (m *merger) keepsLocalSecret(secret bool, local, incoming json.RawMessage) bool {
	return secret && !m.includesSecrets && isEmptySecret(incoming) && !isEmptySecret(local)
}

// keepsLocalBlank reports whether a child of a blanked map must keep its
// local string because the backup blanked it.
func (m *merger) keepsLocalBlank(blanked bool, local, incoming json.RawMessage) bool {
	return blanked && !m.includesSecrets && isEmptyString(incoming) && jsonKind(local) == '"' && !isEmptyString(local)
}

func isEmptyString(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte(`""`))
}

// value merges one node whose backup value is present. local is nil when the
// node is absent on this device. field is the object key holding the node
// ("" for the root and list elements); blanked marks a child of a
// blankedMapFields map.
func (m *merger) value(path, field string, local, incoming json.RawMessage, mode Mode, secret, blanked bool) (json.RawMessage, error) {
	if m.keepsLocalSecret(secret, local, incoming) || m.keepsLocalBlank(blanked, local, incoming) {
		return local, nil
	}
	switch jsonKind(incoming) {
	case '{':
		if jsonKind(local) == '{' {
			_, blankedMap := blankedMapFields[field]
			return m.object(path, local, incoming, mode, secret, blankedMap)
		}
	case '[':
		out, ok, err := m.idList(path, local, incoming, mode, secret)
		if err != nil || ok {
			return out, err
		}
	}
	m.replaced(m.label(path), local, incoming)
	return incoming, nil
}

// replaced records the change of a node taken whole from the backup.
func (m *merger) replaced(label string, local, incoming json.RawMessage) {
	if jsonEqual(local, incoming) {
		return
	}
	if isAbsent(local) {
		m.record(ChangeAdd, label)
		return
	}
	m.record(ChangeUpdate, label)
}

// object merges two objects key by key. blankedMap marks the value of a
// blankedMapFields field, whose children are blanked in a no-secrets backup.
func (m *merger) object(path string, local, incoming json.RawMessage, mode Mode, secret, blankedMap bool) (json.RawMessage, error) {
	localEntries, err := parseObject(local)
	if err != nil {
		return nil, err
	}
	incomingEntries, err := parseObject(incoming)
	if err != nil {
		return nil, err
	}
	localByKey := make(map[string]json.RawMessage, len(localEntries))
	for _, e := range localEntries {
		localByKey[e.key] = e.value
	}
	inBackup := make(map[string]struct{}, len(incomingEntries))
	out := make([]objectEntry, 0, len(incomingEntries)+len(localEntries))
	for _, e := range incomingEntries {
		inBackup[e.key] = struct{}{}
		local := localByKey[e.key]
		if isAbsent(local) {
			local = nil
		}
		v, err := m.value(childPath(path, e.key), e.key, local, e.value, mode, secret || isSecretFieldName(e.key), blankedMap)
		if err != nil {
			return nil, err
		}
		out = append(out, objectEntry{key: e.key, value: v})
	}
	for _, e := range localEntries {
		if _, ok := inBackup[e.key]; ok {
			continue
		}
		child := childPath(path, e.key)
		switch {
		case mode == ModeMerge:
			out = append(out, e)
			m.record(ChangeKeep, child)
		case m.keepsLocalSecret(secret || isSecretFieldName(e.key), e.value, nil):
			out = append(out, e)
		default:
			m.record(ChangeRemove, child)
		}
	}
	return encodeObject(out)
}

// idList merges arrays matched by element id. ok is false when either side
// is not an id list, and the caller replaces the array whole.
func (m *merger) idList(path string, local, incoming json.RawMessage, mode Mode, secret bool) (json.RawMessage, bool, error) {
	incomingItems, ok := parseIDList(incoming)
	if !ok {
		return nil, false, nil
	}
	var localItems []idItem
	switch jsonKind(local) {
	case 0:
	case '[':
		if localItems, ok = parseIDList(local); !ok {
			return nil, false, nil
		}
	default:
		return nil, false, nil
	}
	localByID := make(map[string]idItem, len(localItems))
	for _, item := range localItems {
		localByID[item.id] = item
	}
	inBackup := make(map[string]struct{}, len(incomingItems))
	out := make([]json.RawMessage, 0, len(incomingItems)+len(localItems))
	for _, item := range incomingItems {
		inBackup[item.id] = struct{}{}
		localItem, found := localByID[item.id]
		if !found {
			out = append(out, item.raw)
			m.record(ChangeAdd, item.label())
			continue
		}
		// The backup wins for a matched element; only local secrets survive.
		quiet := m.quiet
		m.quiet = true
		v, err := m.value(path, "", localItem.raw, item.raw, ModeOverwrite, secret, false)
		m.quiet = quiet
		if err != nil {
			return nil, false, err
		}
		out = append(out, v)
		if !jsonEqual(localItem.raw, v) {
			m.record(ChangeUpdate, item.label())
		}
	}
	for _, item := range localItems {
		if _, ok := inBackup[item.id]; ok {
			continue
		}
		if mode == ModeMerge {
			out = append(out, item.raw)
			m.record(ChangeKeep, item.label())
			continue
		}
		m.record(ChangeRemove, item.label())
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, v := range out {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(v)
	}
	buf.WriteByte(']')
	return json.RawMessage(buf.Bytes()), true, nil
}

type idItem struct {
	id   string
	name string
	raw  json.RawMessage
}

func (item idItem) label() string {
	if item.name != "" {
		return item.name
	}
	return item.id
}

// parseIDList reports whether raw is an array whose elements are all objects
// with a unique non-empty string "id".
func parseIDList(raw json.RawMessage) ([]idItem, bool) {
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, false
	}
	items := make([]idItem, 0, len(elements))
	seen := make(map[string]struct{}, len(elements))
	for _, element := range elements {
		if jsonKind(element) != '{' {
			return nil, false
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(element, &fields); err != nil {
			return nil, false
		}
		var id string
		if err := json.Unmarshal(fields["id"], &id); err != nil || id == "" {
			return nil, false
		}
		if _, dup := seen[id]; dup {
			return nil, false
		}
		seen[id] = struct{}{}
		var name string
		if jsonKind(fields["name"]) == '"' {
			_ = json.Unmarshal(fields["name"], &name)
		}
		items = append(items, idItem{id: id, name: name, raw: element})
	}
	return items, true
}

// rehydrateBlanks replaces every "" string in incoming with the local string
// at the same position, like mergeRedactedHttpValue in
// vue-app/src/services/config-sync-persistence.ts. Array items are matched by
// identity (the first non-empty string of id, key, name), else by index.
func rehydrateBlanks(incoming, local json.RawMessage) (json.RawMessage, error) {
	switch jsonKind(incoming) {
	case '"':
		if isEmptyString(incoming) && jsonKind(local) == '"' {
			return local, nil
		}
	case '[':
		var items, localItems []json.RawMessage
		if err := json.Unmarshal(incoming, &items); err != nil {
			return nil, fmt.Errorf("read array: %w", err)
		}
		if jsonKind(local) == '[' {
			if err := json.Unmarshal(local, &localItems); err != nil {
				return nil, fmt.Errorf("read local array: %w", err)
			}
		}
		localByIdentity := make(map[string]json.RawMessage, len(localItems))
		for _, item := range localItems {
			if identity := itemIdentity(item); identity != "" {
				localByIdentity[identity] = item
			}
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, item := range items {
			var match json.RawMessage
			if identity := itemIdentity(item); identity != "" {
				match = localByIdentity[identity]
			} else if i < len(localItems) {
				match = localItems[i]
			}
			v, err := rehydrateBlanks(item, match)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(v)
		}
		buf.WriteByte(']')
		return json.RawMessage(buf.Bytes()), nil
	case '{':
		entries, err := parseObject(incoming)
		if err != nil {
			return nil, err
		}
		localByKey := map[string]json.RawMessage{}
		if jsonKind(local) == '{' {
			localEntries, err := parseObject(local)
			if err != nil {
				return nil, err
			}
			for _, e := range localEntries {
				localByKey[e.key] = e.value
			}
		}
		for i, e := range entries {
			v, err := rehydrateBlanks(e.value, localByKey[e.key])
			if err != nil {
				return nil, err
			}
			entries[i].value = v
		}
		return encodeObject(entries)
	}
	return incoming, nil
}

// itemIdentity mirrors itemIdentity in config-sync-persistence.ts.
func itemIdentity(raw json.RawMessage) string {
	if jsonKind(raw) != '{' {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"id", "key", "name"} {
		var candidate string
		if jsonKind(fields[key]) == '"' && json.Unmarshal(fields[key], &candidate) == nil && candidate != "" {
			return key + ":" + candidate
		}
	}
	return ""
}

type objectEntry struct {
	key   string
	value json.RawMessage
}

// parseObject decodes a JSON object keeping its key order. A repeated key
// keeps its first position and its last value, like JSON.parse.
func parseObject(raw json.RawMessage) ([]objectEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("expected object")
	}
	var entries []objectEntry
	index := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("read object key: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("read object field: %w", err)
		}
		if i, dup := index[key]; dup {
			entries[i].value = value
			continue
		}
		index[key] = len(entries)
		entries = append(entries, objectEntry{key: key, value: value})
	}
	return entries, nil
}

func encodeObject(entries []objectEntry) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	buf.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := enc.Encode(e.key); err != nil {
			return nil, fmt.Errorf("encode object key: %w", err)
		}
		buf.Truncate(buf.Len() - 1) // Encode appends a newline.
		buf.WriteByte(':')
		buf.Write(e.value)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

// jsonKind returns the first byte of a JSON value ('{', '[', '"', ...), or 0
// when the value is absent (empty or null).
func jsonKind(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0
	}
	return trimmed[0]
}

func isAbsent(raw json.RawMessage) bool { return jsonKind(raw) == 0 }

func decodeJSON(raw json.RawMessage) (any, bool) {
	if isAbsent(raw) {
		return nil, true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// jsonEqual compares two values ignoring formatting and key order; absent
// and null are equal.
func jsonEqual(a, b json.RawMessage) bool {
	av, aok := decodeJSON(a)
	bv, bok := decodeJSON(b)
	if !aok || !bok {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(av, bv)
}

// isEmptySecret reports whether a secret value carries nothing: absent, an
// empty string, or an array or object made only of empty values. This is the
// shape the frontend leaves after redacting a secret.
func isEmptySecret(raw json.RawMessage) bool {
	v, ok := decodeJSON(raw)
	if !ok {
		return false
	}
	return emptySecretValue(v)
}

func emptySecretValue(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		for _, child := range v {
			if !emptySecretValue(child) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, child := range v {
			if !emptySecretValue(child) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func compactEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// MergeKV combines a plugin's KV namespace with the backup. Keys from the
// backup are written as stored there; local-only keys are kept (merge) or
// dropped (overwrite). Changes are labelled by key, in key order.
// Any mode other than ModeOverwrite merges, so nothing is dropped by mistake.
func MergeKV(local, incoming map[string]json.RawMessage, mode Mode) (map[string]json.RawMessage, []Change) {
	out := make(map[string]json.RawMessage, len(local)+len(incoming))
	var changes []Change
	for _, key := range unionKeys(local, incoming) {
		in, inBackup := incoming[key]
		l, onDevice := local[key]
		switch {
		case inBackup && onDevice:
			out[key] = in
			if !compactEqual(l, in) {
				changes = append(changes, Change{Kind: ChangeUpdate, Label: key})
			}
		case inBackup:
			out[key] = in
			changes = append(changes, Change{Kind: ChangeAdd, Label: key})
		case mode == ModeOverwrite:
			changes = append(changes, Change{Kind: ChangeRemove, Label: key})
		default:
			out[key] = l
			changes = append(changes, Change{Kind: ChangeKeep, Label: key})
		}
	}
	return out, changes
}

func unionKeys(a, b map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// MergeFileSet compares a plugin's file paths with the backup's. write lists
// every backup path (the caller writes the backup contents); remove lists
// local-only paths in overwrite mode and is nil in merge mode. All outputs
// are sorted by path. Any mode other than ModeOverwrite merges.
func MergeFileSet(local, incoming []string, mode Mode) (write []string, remove []string, changes []Change) {
	onDevice := make(map[string]struct{}, len(local))
	for _, p := range local {
		onDevice[p] = struct{}{}
	}
	inBackup := make(map[string]struct{}, len(incoming))
	for _, p := range incoming {
		inBackup[p] = struct{}{}
	}
	paths := make([]string, 0, len(onDevice)+len(inBackup))
	for p := range inBackup {
		paths = append(paths, p)
	}
	for p := range onDevice {
		if _, ok := inBackup[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		_, inB := inBackup[p]
		_, onD := onDevice[p]
		switch {
		case inB && onD:
			write = append(write, p)
			changes = append(changes, Change{Kind: ChangeUpdate, Label: p})
		case inB:
			write = append(write, p)
			changes = append(changes, Change{Kind: ChangeAdd, Label: p})
		case mode == ModeOverwrite:
			remove = append(remove, p)
			changes = append(changes, Change{Kind: ChangeRemove, Label: p})
		default:
			changes = append(changes, Change{Kind: ChangeKeep, Label: p})
		}
	}
	return write, remove, changes
}
