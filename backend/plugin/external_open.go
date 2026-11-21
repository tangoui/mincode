package plugin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/associations"
	"mincode/app/backend/core"
	"mincode/app/plugins/api/externalopen"
)

// This registry contains no plugin URLs or file contents in persistent storage.
// Remembered choices are host-owned and bound to the validated declaration.
type externalCandidate struct {
	PluginID    string `json:"pluginId"`
	PluginName  string `json:"pluginName"`
	HandlerID   string `json:"handlerId"`
	View        string `json:"view"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}
type associationPreference struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}
type externalItem struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Name       string             `json:"name"`
	Value      string             `json:"-"`
	At         time.Time          `json:"-"`
	Info       os.FileInfo        `json:"-"`
	Grant      *externalCandidate `json:"-"`
	Delivered  bool               `json:"-"`
	Reader     *processRuntime
	Generation uint64
}

var externalQueue = struct {
	sync.Mutex
	items    map[string]*externalItem
	ready    bool
	rejected bool
}{items: map[string]*externalItem{}}
var associationQuery = associations.Query
var associationSet = associations.Set

const externalTTL = 2 * time.Minute

func opaqueID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func preferenceKey(c externalCandidate) string {
	return "associations/enabled/" + c.PluginID + "/" + c.HandlerID
}
func defaultKey(t string) string { return "associations/default/" + t }
func candidateEqual(a, b externalCandidate) bool {
	return a.PluginID == b.PluginID && a.HandlerID == b.HandlerID && a.Type == b.Type && a.Fingerprint == b.Fingerprint && a.View == b.View
}
func preference(c externalCandidate) bool {
	raw, ok, err := pluginKVGetRaw(hostPluginID, preferenceKey(c))
	var p associationPreference
	return err == nil && ok && json.Unmarshal(raw, &p) == nil && p.Enabled && p.Fingerprint == c.Fingerprint
}
func remembered(t string, candidates []externalCandidate) string {
	raw, ok, err := pluginKVGetRaw(hostPluginID, defaultKey(t))
	var old externalCandidate
	if err != nil || !ok || json.Unmarshal(raw, &old) != nil {
		return ""
	}
	for _, c := range candidates {
		if candidateEqual(c, old) {
			return c.PluginID + "/" + c.HandlerID
		}
	}
	return ""
}
func (m *pluginManager) externalCandidates(typeID string, enabledOnly bool) []externalCandidate {
	m.mu.RLock()
	records := m.orderedRecordsLocked()
	m.mu.RUnlock()
	result := []externalCandidate{}
	for _, r := range records {
		r.mu.Lock()
		if r.manifest == nil || r.builtin != nil || !r.contributionsListedLocked() || !m.workspaceActiveLocked(r) {
			r.mu.Unlock()
			continue
		}
		p := r.manifest.contribution(externalOpenPoint)
		if p != nil {
			for _, raw := range p.Entries {
				h, ok := raw.(externalHandler)
				if !ok || (typeID != "" && h.Type != typeID) {
					continue
				}
				// Bind remember/opt-in to target view, version and permissions too.
				_, views, _ := manifestCommandMaps(r.manifest)
				view, exists := views[h.View]
				if !exists {
					continue
				}
				signature, _ := json.Marshal([]any{p.Fingerprint, r.manifest.Version, r.manifest.Capabilities, view})
				digest := sha256.Sum256(signature)
				c := externalCandidate{r.id, r.manifest.Name, h.ID, h.View, h.Type, hex.EncodeToString(digest[:])}
				if !enabledOnly || preference(c) {
					result = append(result, c)
				}
			}
		}
		r.mu.Unlock()
	}
	return result
}

// runtimeCandidate revalidates runtime identity before servicing reverse RPC.
func runtimeCandidate(rt *processRuntime, handlerID string) (externalCandidate, error) {
	r, e := host().record(rt.pluginID)
	if e != nil {
		return externalCandidate{}, errors.New("plugin unavailable")
	}
	r.mu.Lock()
	same := r.runtime == rt
	r.mu.Unlock()
	if !same {
		return externalCandidate{}, errors.New("stale plugin runtime")
	}
	for _, c := range host().externalCandidates("", false) {
		if c.PluginID == rt.pluginID && c.HandlerID == handlerID {
			return c, nil
		}
	}
	return externalCandidate{}, errors.New("handler unavailable")
}
func hostAssociationsList(rt *processRuntime, params json.RawMessage) (any, *rpcError) {
	if e := decodeHostUIParams(params, &struct{}{}); e != nil {
		return nil, e
	}
	rows := []any{}
	for _, c := range host().externalCandidates("", false) {
		if c.PluginID != rt.pluginID {
			continue
		}
		if _, e := runtimeCandidate(rt, c.HandlerID); e != nil {
			continue
		}
		state, e := associationQuery(c.Type)
		if e != nil {
			state = associations.Status{}
		}
		rows = append(rows, map[string]any{"handlerId": c.HandlerID, "type": c.Type, "enabled": preference(c), "defaultPlugin": remembered(c.Type, host().externalCandidates(c.Type, true)) == c.PluginID+"/"+c.HandlerID, "system": state})
	}
	return map[string]any{"handlers": rows}, nil
}
func hostAssociationsRequest(rt *processRuntime, params json.RawMessage) (any, *rpcError) {
	var p struct {
		HandlerID string `json:"handlerId"`
	}
	if e := decodeHostUIParams(params, &p); e != nil {
		return nil, e
	}
	c, e := runtimeCandidate(rt, p.HandlerID)
	if e != nil {
		return nil, hostUIParamsError("handler unavailable")
	}
	if e := rateLimited(rt); e != nil {
		return nil, e
	}
	hostUIEmit("plugin:association-settings", map[string]any{"pluginId": c.PluginID, "handlerId": c.HandlerID})
	return map[string]any{"requested": true}, nil
}

// EnqueueExternalURI is called by native launch handling. No plugin is started.
func EnqueueExternalURI(raw string) bool {
	t, e := externalopen.URI(raw)
	if e != nil {
		return false
	}
	return enqueueExternal(t.ID, raw, t.Name, nil)
}

// OfferExternalFiles preserves ordinary editor opening. Only an opted-in,
// effective plugin can intercept a catalog file type.
func OfferExternalFiles(files []string) []string {
	rest := []string{}
	for _, f := range files {
		t, ok := externalopen.FileType(f)
		if !ok || len(host().externalCandidates(t.ID, true)) == 0 {
			rest = append(rest, f)
			continue
		}
		path, info, e := associations.ValidateFile(f)
		if e != nil {
			externalQueue.Lock()
			rejectExternalLocked()
			externalQueue.Unlock()
			continue
		}
		enqueueExternal(t.ID, path, filepath.Base(path), info)
	}
	return rest
}

// Coalesce cold-start failures without retaining rejected paths or links.
func rejectExternalLocked() {
	if externalQueue.ready {
		core.Emit("plugin:external-open", map[string]bool{"rejected": true})
	} else {
		externalQueue.rejected = true
	}
}
func enqueueExternal(t, value, name string, info os.FileInfo) bool {
	externalQueue.Lock()
	defer externalQueue.Unlock()
	now := time.Now()
	for id, item := range externalQueue.items {
		if now.Sub(item.At) > externalTTL {
			delete(externalQueue.items, id)
		} else if item.Type == t && item.Value == value {
			return true
		}
	}
	if len(externalQueue.items) >= 8 {
		rejectExternalLocked()
		return true
	}
	id := opaqueID()
	if id == "" {
		rejectExternalLocked()
		return true
	}
	externalQueue.items[id] = &externalItem{ID: id, Type: t, Name: name, Value: value, At: now, Info: info}
	time.AfterFunc(externalTTL, func() {
		externalQueue.Lock()
		defer externalQueue.Unlock()
		if p := externalQueue.items[id]; p != nil && time.Since(p.At) >= externalTTL {
			delete(externalQueue.items, id)
		}
	})
	if externalQueue.ready {
		core.Emit("plugin:external-open", map[string]string{"requestId": id})
	}
	return true
}
func externalRequest(id string) (externalItem, error) {
	externalQueue.Lock()
	defer externalQueue.Unlock()
	p := externalQueue.items[id]
	if p == nil || time.Since(p.At) > externalTTL {
		delete(externalQueue.items, id)
		return externalItem{}, errors.New("request expired")
	}
	return *p, nil
}
func externalResolve(id string) (map[string]any, error) {
	p, e := externalRequest(id)
	if e != nil {
		return nil, e
	}
	if p.Delivered {
		return nil, errors.New("request already delivered")
	}
	candidates := host().externalCandidates(p.Type, true)
	return map[string]any{"requestId": p.ID, "type": p.Type, "name": p.Name, "candidates": candidates, "remembered": remembered(p.Type, candidates)}, nil
}

// Files use an opaque request, not plugin-supplied paths; identity is checked
// again when reading. URIs reach a view only, never an action or command line.
func externalDeliver(id, pluginID, handlerID, fingerprint string, remember bool) (any, error) {
	p, e := externalRequest(id)
	if e != nil {
		return nil, e
	}
	if p.Delivered {
		return nil, errors.New("request already delivered")
	}
	var selected *externalCandidate
	for _, c := range host().externalCandidates(p.Type, true) {
		if c.PluginID == pluginID && c.HandlerID == handlerID && c.Fingerprint == fingerprint {
			copy := c
			selected = &copy
			break
		}
	}
	if selected == nil {
		return nil, errors.New("handler unavailable")
	}
	params := map[string]any{"requestId": id, "type": p.Type, "name": p.Name}
	if p.Info != nil {
		path, info, e := associations.ValidateFile(p.Value)
		if e != nil || path != p.Value || !os.SameFile(info, p.Info) || info.Size() != p.Info.Size() || !info.ModTime().Equal(p.Info.ModTime()) {
			return nil, errors.New("file changed")
		}
	} else {
		params["url"] = p.Value
		encoded, err := json.Marshal(params)
		if err != nil || len(encoded) > 6<<10 {
			return nil, errors.New("external link exceeds view request limit")
		}
	}
	if remember {
		raw, _ := json.Marshal(selected)
		if e := pluginKVSetRaw(hostPluginID, defaultKey(p.Type), raw); e != nil {
			return nil, e
		}
	}
	externalQueue.Lock()
	live := externalQueue.items[id]
	if live == nil || live.Delivered || time.Since(live.At) > externalTTL {
		externalQueue.Unlock()
		return nil, errors.New("request expired")
	}
	live.Grant = selected
	live.Delivered = true
	externalQueue.Unlock()
	return map[string]any{"pluginId": selected.PluginID, "view": selected.View, "handlerId": selected.HandlerID, "params": params}, nil
}
func hostAssociationsRead(rt *processRuntime, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		RequestID string `json:"requestId"`
		Offset    int64  `json:"offset"`
		Length    int    `json:"length"`
	}
	if e := decodeHostUIParams(raw, &p); e != nil {
		return nil, e
	}
	if p.Offset < 0 || p.Length < 1 || p.Length > 64<<10 {
		return nil, hostUIParamsError("invalid chunk")
	}
	item, e := externalRequest(p.RequestID)
	if e != nil || item.Grant == nil || item.Info == nil || item.Grant.PluginID != rt.pluginID {
		return nil, hostUIParamsError("file grant unavailable")
	}
	current, e := runtimeCandidate(rt, item.Grant.HandlerID)
	if e != nil || !candidateEqual(current, *item.Grant) || !preference(current) {
		return nil, hostUIParamsError("file grant revoked")
	}
	externalQueue.Lock()
	live := externalQueue.items[p.RequestID]
	if live == nil {
		externalQueue.Unlock()
		return nil, hostUIParamsError("file grant unavailable")
	}
	if live.Reader == nil {
		live.Reader = rt
		live.Generation = rt.Generation()
	}
	sameRuntime := live.Reader == rt && live.Generation == rt.Generation()
	externalQueue.Unlock()
	if !sameRuntime {
		return nil, hostUIParamsError("file grant revoked")
	}
	path, info, e := associations.ValidateFile(item.Value)
	if e != nil || path != item.Value || !os.SameFile(info, item.Info) || info.Size() != item.Info.Size() || !info.ModTime().Equal(item.Info.ModTime()) {
		return nil, hostUIParamsError("file changed")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, hostUIParamsError("cannot read authorized file")
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return nil, hostUIParamsError("file changed")
	}
	if p.Offset > info.Size() {
		return nil, hostUIParamsError("invalid offset")
	}
	buf := make([]byte, p.Length)
	n, e := f.ReadAt(buf, p.Offset)
	if e != nil && e != io.EOF {
		return nil, hostUIParamsError("cannot read authorized file")
	}
	after, err := f.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, hostUIParamsError("file changed")
	}
	return map[string]any{"data": base64.StdEncoding.EncodeToString(buf[:n]), "size": info.Size(), "eof": p.Offset+int64(n) >= info.Size()}, nil
}
func associationSettings(pluginID, handlerID string) (any, error) {
	for _, c := range host().externalCandidates("", false) {
		if c.PluginID == pluginID && c.HandlerID == handlerID {
			t, _ := externalopen.Lookup(c.Type)
			state, e := associationQuery(c.Type)
			if e != nil {
				return nil, e
			}
			return map[string]any{"candidate": c, "name": t.Name, "enabled": preference(c), "system": state, "defaultPlugin": remembered(c.Type, host().externalCandidates(c.Type, true)) == c.PluginID+"/"+c.HandlerID}, nil
		}
	}
	return nil, errors.New("handler unavailable")
}
func associationUpdate(c externalCandidate, operation string) (any, error) {
	valid := false
	for _, next := range host().externalCandidates(c.Type, false) {
		if candidateEqual(c, next) {
			valid = true
			break
		}
	}
	if !valid {
		return nil, errors.New("declaration changed")
	}
	switch operation {
	case "enable", "disable":
		raw, _ := json.Marshal(associationPreference{operation == "enable", c.Fingerprint})
		if e := pluginKVSetRaw(hostPluginID, preferenceKey(c), raw); e != nil {
			return nil, e
		}
		// Disabling invalidates the remembered selector without altering an OS default.
		if operation == "disable" {
			raw, ok, _ := pluginKVGetRaw(hostPluginID, defaultKey(c.Type))
			var remembered externalCandidate
			if ok && json.Unmarshal(raw, &remembered) == nil && remembered.PluginID == c.PluginID && remembered.HandlerID == c.HandlerID {
				if err := pluginKVDeleteRaw(hostPluginID, defaultKey(c.Type)); err != nil {
					return nil, err
				}
			}
		}
	case "forget":
		return nil, pluginKVDeleteRaw(hostPluginID, defaultKey(c.Type))
	case "default", "restore":
		state, e := associationQuery(c.Type)
		if e != nil {
			return nil, e
		}
		key := "associations/previous/" + c.Type
		previous := ""
		raw, ok, e := pluginKVGetRaw(hostPluginID, key)
		if e != nil {
			return nil, e
		}
		if ok {
			_ = json.Unmarshal(raw, &previous)
		}
		if operation == "default" && !state.Default {
			previous = state.Application
			raw, _ = json.Marshal(previous)
			if e = pluginKVSetRaw(hostPluginID, key, raw); e != nil {
				return nil, e
			}
		}
		return associationSet(c.Type, previous, operation == "restore")
	default:
		return nil, errors.New("unknown association operation")
	}
	return associationSettings(c.PluginID, c.HandlerID)
}
func init() {
	core.Register("plugin_host_association_handlers", func(_, raw string) (string, error) {
		var p struct {
			PluginID string `json:"pluginId"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		result := []externalCandidate{}
		for _, c := range host().externalCandidates("", false) {
			if c.PluginID == p.PluginID {
				result = append(result, c)
			}
		}
		return core.EncodeResult(result)
	})
	core.Register("plugin_host_external_files", func(_, raw string) (string, error) {
		var p struct {
			Files []string `json:"files"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		if len(p.Files) > 32 {
			return "", errors.New("too many files")
		}
		return core.EncodeResult(OfferExternalFiles(p.Files))
	})
	core.Register("plugin_host_external_pending", func(_, raw string) (string, error) {
		if err := decodeStrictJSON([]byte(raw), &struct{}{}, true); err != nil {
			return "", err
		}
		externalQueue.Lock()
		externalQueue.ready = true
		if externalQueue.rejected {
			externalQueue.rejected = false
			core.Emit("plugin:external-open", map[string]bool{"rejected": true})
		}
		ids := []string{}
		for id, p := range externalQueue.items {
			if time.Since(p.At) <= externalTTL && !p.Delivered {
				ids = append(ids, id)
			}
		}
		externalQueue.Unlock()
		return core.EncodeResult(ids)
	})
	core.Register("plugin_host_external_resolve", func(_, raw string) (string, error) {
		var p struct {
			RequestID string `json:"requestId"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		v, e := externalResolve(p.RequestID)
		if e != nil {
			return "", e
		}
		return core.EncodeResult(v)
	})
	core.Register("plugin_host_external_deliver", func(_, raw string) (string, error) {
		var p struct {
			RequestID   string `json:"requestId"`
			PluginID    string `json:"pluginId"`
			HandlerID   string `json:"handlerId"`
			Fingerprint string `json:"fingerprint"`
			Remember    bool   `json:"remember"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		v, e := externalDeliver(p.RequestID, p.PluginID, p.HandlerID, p.Fingerprint, p.Remember)
		if e != nil {
			return "", e
		}
		return core.EncodeResult(v)
	})
	core.Register("plugin_host_association_settings", func(_, raw string) (string, error) {
		var p struct {
			PluginID  string `json:"pluginId"`
			HandlerID string `json:"handlerId"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		v, e := associationSettings(p.PluginID, p.HandlerID)
		if e != nil {
			return "", e
		}
		return core.EncodeResult(v)
	})
	core.Register("plugin_host_association_update", func(_, raw string) (string, error) {
		var p struct {
			Candidate externalCandidate `json:"candidate"`
			Operation string            `json:"operation"`
		}
		if e := decodeStrictJSON([]byte(raw), &p, true); e != nil {
			return "", e
		}
		v, e := associationUpdate(p.Candidate, p.Operation)
		if e != nil {
			return "", e
		}
		return core.EncodeResult(v)
	})
	registerRecordLifecycleHook(func(m *pluginManager, r *pluginRecord) {
		r.mu.Lock()
		id, enabled := r.id, r.enabled
		r.mu.Unlock()
		externalQueue.Lock()
		for key, p := range externalQueue.items {
			if p.Grant != nil && p.Grant.PluginID == id {
				delete(externalQueue.items, key)
			}
		}
		externalQueue.Unlock()
		if !enabled {
			for _, t := range externalopen.Catalog() {
				raw, ok, _ := pluginKVGetRaw(hostPluginID, defaultKey(t.ID))
				if ok && strings.Contains(string(raw), `"pluginId":"`+id+`"`) {
					_ = pluginKVDeleteRaw(hostPluginID, defaultKey(t.ID))
				}
			}
		}
	})
	registerRecordRemovalHook(func(_ *pluginManager, id string, _ removalReason) {
		keys, _ := pluginKVListRaw(hostPluginID)
		for _, k := range keys {
			if strings.HasPrefix(k, "associations/enabled/"+id+"/") {
				_ = pluginKVDeleteRaw(hostPluginID, k)
			}
		}
		for _, t := range externalopen.Catalog() {
			raw, ok, _ := pluginKVGetRaw(hostPluginID, defaultKey(t.ID))
			var c externalCandidate
			if ok && json.Unmarshal(raw, &c) == nil && c.PluginID == id {
				_ = pluginKVDeleteRaw(hostPluginID, defaultKey(t.ID))
			}
		}
	})
	registerLogRedactor(func(id, message string) string {
		externalQueue.Lock()
		defer externalQueue.Unlock()
		for _, p := range externalQueue.items {
			if p.Grant != nil && p.Grant.PluginID == id {
				message = strings.ReplaceAll(message, p.Value, "[external input]")
			}
		}
		return message
	})
}
