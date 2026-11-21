package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Contribution points are the data extension points under manifest
// "contributes" other than commands, views and actions. Their bounds live in
// plugins/api/contribution-points.json (shared with the CLI, cloud,
// devkit and frontend); each point implementation registers itself from an
// init() in its own file (keymaps.go, later contrib_<point>.go).
//
// A point is accepted only when its contract status is "enabled" AND a Go
// implementation is registered. Planned points fail closed as unknown, exactly
// like a key an older host has never heard of.

const pluginContributionsChangedEvent = "plugin:contributions-changed"

// ContributionIssue reports one skipped entry. Loading an installed package
// only reports issues; install, upgrade and install preview make them fatal.
type ContributionIssue struct {
	Point   string `json:"point"`
	Entry   int    `json:"entry"`
	Key     string `json:"key,omitempty"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}

// contributionEntry is one decoded entry. Value must marshal to the JSON shape
// the frontend consumer expects.
type contributionEntry struct {
	Index int
	Key   string
	ID    string
	Value any
	// Assets are the content-hash keys this entry read through readAsset;
	// only assets of entries that survive Finalize stay servable.
	Assets []string
}

// contributionAsset records one validated package asset of a point. The
// snapshot carries only its key (hex sha256 of Canonical); the asset command
// re-reads Path and revalidates it with the same Hint and Ext.
type contributionAsset struct {
	Path string // package-relative, slash separated
	Hint string // ValidateAsset hint (the referencing field)
	Ext  string // lower-case extension of Path
	Mime string
	Size int64 // canonical size
}

// resolvedContribution is the validated, immutable state of one point.
// Assets is host-only (never part of the snapshot).
type resolvedContribution struct {
	Entries     []any
	Issues      []ContributionIssue
	Fingerprint string
	Assets      map[string]contributionAsset
}

// pointContext is what a point implementation sees while decoding.
type pointContext struct {
	root string
	// paths validates package-relative files under root and caches verified
	// directories for the manifest being validated; nil means "create one for
	// root on first use" (see pathResolver).
	paths    *pluginPathResolver
	manifest *Manifest
	spec     api.ContributionPointSpec
	// commands are this plugin's already validated contributes.commands by local ID.
	commands map[string]ManifestCommand
	// views are this plugin's already validated contributes.views by ID.
	views map[string]ManifestView
	// actions are this plugin's already normalized contributes.actions by ID
	// (risk and maxPayloadBytes defaults applied).
	actions map[string]ManifestUIAction
	// validateAsset is the point's ValidateAsset hook (nil without assets).
	validateAsset func(hint, ext string, data []byte) ([]byte, string, error)
	// assets are committed assets of accepted entries; pending holds the
	// assets read while decoding the current entry (see readAsset).
	assets       map[string]contributionAsset
	assetBytes   int64
	pending      map[string]contributionAsset
	pendingOrder []string
	// resolved holds the points of this manifest resolved so far; a point
	// that lists DependsOn finds those points here (read only).
	resolved map[string]*resolvedContribution
}

// contributionFileLabels keeps point-specific error wording (for example the
// historical "keymap path ..." messages) while the framework owns the checks.
type contributionFileLabels struct {
	Noun      string // "keymap": used in path/file errors
	Entry     string // "keymap preset": used for inline size errors
	ShapeHint string // "a preset object": used when an entry is not an object
}

type contributionPoint struct {
	Key string
	// Decode validates one entry. For fileRef mode "entry" the framework has
	// already replaced a {"path": ...} reference with the file content. The
	// returned id is used for duplicate detection and issue reporting even when
	// err is non-nil.
	Decode func(ctx *pointContext, index int, key string, raw json.RawMessage) (value any, id string, err error)
	// Finalize runs cross-entry checks (references, extends chains) on entries
	// that decoded successfully. Optional.
	Finalize func(ctx *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue)
	// DuplicateMessage formats the issue for a repeated entry id. Optional:
	// without it ids are not checked for duplicates by the framework.
	DuplicateMessage func(id string) string
	// IssuePrefix starts the strict (install) error, e.g. "invalid keymap contribution".
	IssuePrefix string
	// IssueText renders an issue for plugin logs and development diagnostics.
	IssueText func(issue ContributionIssue) string
	// EntryNoun pluralises count errors ("presets"); defaults to "entries".
	EntryNoun string
	// FileLabels customise fileRef error wording. Optional.
	FileLabels contributionFileLabels
	// DecodeReference parses a fileRef mode "entry" reference object and
	// returns its path. Optional; the default accepts exactly {field: string}.
	DecodeReference func(raw json.RawMessage) (string, error)
	// ExpandsPermission reports that a development generation's entries need a
	// newly approved session. Optional; nil means data-only.
	ExpandsPermission func(current, candidate *resolvedContribution) bool
	// RuntimeOverlay returns runtime state merged into the snapshot (for example
	// dynamic status bar text). Optional. Called with record.mu held; it must
	// not lock record.mu.
	RuntimeOverlay func(record *pluginRecord) any
	// ValidateAsset validates one asset read through pointContext.readAsset
	// (install) and by plugin_host_contribution_assets (every fetch). hint is
	// the caller-chosen field name ("messages", "icon") and doubles as the
	// error noun; ext is the lower-case file extension. It returns the
	// canonical bytes that are hashed and served, and their MIME type.
	// Required when the contract declares assets.
	ValidateAsset func(hint, ext string, data []byte) (canonical []byte, mime string, err error)
	// DependsOn names points that are resolved before this one (a point
	// resolves after every point it depends on, transitively), so Decode can read their accepted entries through
	// pointContext.resolved. Optional.
	DependsOn []string
}

var (
	contributionPointsMu sync.RWMutex
	contributionPoints   = map[string]*contributionPoint{}
)

// registerContributionPoint is called from a point file's init(). It panics
// on duplicates and on keys missing from contribution-points.json.
func registerContributionPoint(point *contributionPoint) {
	if point == nil || point.Key == "" || point.Decode == nil || point.IssuePrefix == "" {
		panic("contribution point needs Key, Decode and IssuePrefix")
	}
	spec, ok := api.ContributionPointSpecFor(point.Key)
	if !ok {
		panic(fmt.Sprintf("contribution point %q is missing from contribution-points.json", point.Key))
	}
	if spec.Assets != nil && point.ValidateAsset == nil {
		panic(fmt.Sprintf("contribution point %q declares assets but has no ValidateAsset", point.Key))
	}
	contributionPointsMu.Lock()
	defer contributionPointsMu.Unlock()
	if _, exists := contributionPoints[point.Key]; exists {
		panic(fmt.Sprintf("contribution point %q registered twice", point.Key))
	}
	contributionPoints[point.Key] = point
}

// enabledContributionPoint returns the implementation and spec of a point the
// host accepts: registered and marked enabled in the shared contract. This is
// the explicit "enabled points" gate.
func enabledContributionPoint(key string) (*contributionPoint, api.ContributionPointSpec, bool) {
	spec, ok := api.ContributionPointSpecFor(key)
	if !ok || !spec.Enabled() {
		return nil, spec, false
	}
	contributionPointsMu.RLock()
	point := contributionPoints[key]
	contributionPointsMu.RUnlock()
	return point, spec, point != nil
}

// enabledContributionPointKeys lists accepted point keys in sorted order.
func enabledContributionPointKeys() []string {
	keys := []string{}
	for _, spec := range api.ContributionPointSpecs() {
		if _, _, ok := enabledContributionPoint(spec.Key); ok {
			keys = append(keys, spec.Key)
		}
	}
	return keys
}

// ---------------------------------------------------------------------------
// Manifest encoding: commands/views/actions keep typed fields; every other key
// is a contribution point and must be accepted by the host.

var fixedContributionKeys = map[string]struct{}{"commands": {}, "views": {}, "actions": {}}

func (contributions *ManifestContributions) UnmarshalJSON(data []byte) error {
	*contributions = ManifestContributions{}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("contributes must be an object: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := bytes.TrimSpace(raw[key])
		if bytes.Equal(value, []byte("null")) {
			continue
		}
		var err error
		switch key {
		case "commands":
			err = decodeStrictJSON(value, &contributions.Commands, true)
		case "views":
			err = decodeStrictJSON(value, &contributions.Views, true)
		case "actions":
			err = decodeStrictJSON(value, &contributions.Actions, true)
		default:
			if _, _, ok := enabledContributionPoint(key); !ok {
				return fmt.Errorf("unknown contribution point %q", key)
			}
			if contributions.Points == nil {
				contributions.Points = map[string]json.RawMessage{}
			}
			contributions.Points[key] = append(json.RawMessage(nil), value...)
		}
		if err != nil {
			return fmt.Errorf("contributes.%s: %w", key, err)
		}
	}
	return nil
}

func (contributions ManifestContributions) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	first := true
	write := func(key string, value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !first {
			buffer.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(key)
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(encoded)
		return nil
	}
	if len(contributions.Commands) > 0 {
		if err := write("commands", contributions.Commands); err != nil {
			return nil, err
		}
	}
	if len(contributions.Views) > 0 {
		if err := write("views", contributions.Views); err != nil {
			return nil, err
		}
	}
	if len(contributions.Actions) > 0 {
		if err := write("actions", contributions.Actions); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(contributions.Points))
	for key := range contributions.Points {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := write(key, contributions.Points[key]); err != nil {
			return nil, err
		}
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// hasContributionEntries reports whether a raw point value holds at least one
// entry (non-empty list, map with a non-empty list, or non-empty object).
func hasContributionEntries(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var list []json.RawMessage
	if json.Unmarshal(trimmed, &list) == nil {
		return len(list) > 0
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) == nil {
		for _, value := range object {
			var nested []json.RawMessage
			if json.Unmarshal(value, &nested) != nil || len(nested) > 0 {
				return true
			}
		}
	}
	return false
}

// declarativeDataContributed reports whether a declarative package contributes
// at least one entry to a point that allows declarative packages.
func declarativeDataContributed(manifest *Manifest) bool {
	for key, raw := range manifest.Contributes.Points {
		if _, spec, ok := enabledContributionPoint(key); ok && spec.AllowsKind(pluginRuntimeDeclarative) && hasContributionEntries(raw) {
			return true
		}
	}
	return false
}

// declarativeContributionNouns names the data a declarative package may
// contribute ("keymap", later "keymap or theme") for the empty-package error.
func declarativeContributionNouns() string {
	var nouns []string
	for _, key := range enabledContributionPointKeys() {
		point, spec, _ := enabledContributionPoint(key)
		if !spec.AllowsKind(pluginRuntimeDeclarative) {
			continue
		}
		noun := point.FileLabels.Noun
		if noun == "" {
			noun = key + " entry"
		}
		nouns = append(nouns, noun)
	}
	if len(nouns) == 0 {
		return "data entry"
	}
	return strings.Join(nouns, " or ")
}

// ---------------------------------------------------------------------------
// Validation.

// resolveManifestContributionPoints validates every contributed point in key
// order. Structural problems (unknown point, wrong runtime kind, API version,
// shape or count) are fatal; entry problems become issues.
func resolveManifestContributionPoints(root string, manifest *Manifest) error {
	return resolveManifestContributionPointsWith(newPluginPathResolver(root), manifest)
}

// resolveManifestContributionPointsWith shares paths (and its verified
// directory cache) with the rest of manifest validation, so packages with many
// assets resolve the plugin root once and Lstat each asset leaf once.
func resolveManifestContributionPointsWith(paths *pluginPathResolver, manifest *Manifest) error {
	root := paths.root
	manifest.contributions = nil
	if len(manifest.Contributes.Points) == 0 {
		return nil
	}
	keys := make([]string, 0, len(manifest.Contributes.Points))
	for key := range manifest.Contributes.Points {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Points with DependsOn resolve after every point without it.
	slices.SortStableFunc(keys, func(a, b string) int {
		return contributionPointDependsRank(a) - contributionPointDependsRank(b)
	})
	commands := make(map[string]ManifestCommand, len(manifest.Contributes.Commands))
	for _, command := range manifest.Contributes.Commands {
		commands[command.ID] = command
	}
	views := make(map[string]ManifestView, len(manifest.Contributes.Views))
	// ctx.views lists tab views only. Floating views open in their own window;
	// the one point that may target them (globalShortcuts, run.type floating,
	// to show or hide that window) reads manifest.Contributes.Views itself.
	for _, view := range manifest.Contributes.Views {
		if view.Floating == nil {
			views[view.ID] = view
		}
	}
	actions := make(map[string]ManifestUIAction, len(manifest.Contributes.Actions))
	for _, action := range manifest.Contributes.Actions {
		actions[action.ID] = action
	}
	resolved := map[string]*resolvedContribution{}
	for _, key := range keys {
		point, spec, ok := enabledContributionPoint(key)
		if !ok {
			return fmt.Errorf("unknown contribution point %q", key)
		}
		raw := bytes.TrimSpace(manifest.Contributes.Points[key])
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if !spec.AllowsKind(manifest.Runtime.Type) {
			return fmt.Errorf("contributes.%s requires runtime.type %s", key, strings.Join(spec.Kinds, " or "))
		}
		if manifest.APIVersion < spec.MinAPIVersion {
			return fmt.Errorf("contributes.%s requires manifest apiVersion %d", key, spec.MinAPIVersion)
		}
		if err := requireContributionCapabilities(manifest, spec); err != nil {
			return err
		}
		ctx := &pointContext{root: root, paths: paths, manifest: manifest, spec: spec, commands: commands, views: views, actions: actions, validateAsset: point.ValidateAsset, resolved: resolved}
		result, err := resolveContributionPoint(ctx, point, raw)
		if err != nil {
			return err
		}
		resolved[key] = result
	}
	if len(resolved) > 0 {
		manifest.contributions = resolved
	}
	return nil
}

// contributionPointDependsRank is 0 for a point without DependsOn, else one
// more than the highest rank of the points it depends on (testProviders
// depends on debuggers, which depends on jdtlsBundles).
func contributionPointDependsRank(key string) int {
	contributionPointsMu.RLock()
	defer contributionPointsMu.RUnlock()
	return contributionPointDependsRankLocked(key, 0)
}

func contributionPointDependsRankLocked(key string, depth int) int {
	point := contributionPoints[key]
	if point == nil || len(point.DependsOn) == 0 || depth > len(contributionPoints) {
		return 0
	}
	rank := 0
	for _, dependency := range point.DependsOn {
		rank = max(rank, contributionPointDependsRankLocked(dependency, depth+1))
	}
	return rank + 1
}

// requireContributionCapabilities enforces the contract's requiresCapabilities
// in strict and lenient mode alike.
func requireContributionCapabilities(manifest *Manifest, spec api.ContributionPointSpec) error {
	for _, required := range spec.RequiresCapabilities {
		granted := false
		for _, capability := range manifest.Capabilities {
			if string(capability) == required {
				granted = true
				break
			}
		}
		if !granted {
			return fmt.Errorf("contributes.%s requires capability %s", spec.Key, required)
		}
	}
	return nil
}

type rawContributionEntry struct {
	index int
	key   string
	raw   json.RawMessage
}

func resolveContributionPoint(ctx *pointContext, point *contributionPoint, raw json.RawMessage) (*resolvedContribution, error) {
	spec := ctx.spec
	noun := point.EntryNoun
	if noun == "" {
		noun = "entries"
	}
	var items []rawContributionEntry
	switch spec.Shape {
	case api.ContributionShapeList:
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("contributes.%s must be a list", spec.Key)
		}
		if len(list) > spec.MaxEntries {
			return nil, fmt.Errorf("contributes.%s must contain at most %d %s", spec.Key, spec.MaxEntries, noun)
		}
		for index, item := range list {
			items = append(items, rawContributionEntry{index: index, raw: item})
		}
	case api.ContributionShapeMap:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("contributes.%s must be an object of lists", spec.Key)
		}
		allowed := map[string]struct{}{}
		for _, key := range spec.Keys {
			allowed[key] = struct{}{}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			if _, ok := allowed[key]; !ok {
				return nil, fmt.Errorf("contributes.%s has unknown key %q", spec.Key, key)
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		total := 0
		for _, key := range keys {
			var list []json.RawMessage
			if err := json.Unmarshal(object[key], &list); err != nil {
				return nil, fmt.Errorf("contributes.%s[%q] must be a list", spec.Key, key)
			}
			if len(list) > spec.MaxPerKey {
				return nil, fmt.Errorf("contributes.%s[%q] must contain at most %d %s", spec.Key, key, spec.MaxPerKey, noun)
			}
			total += len(list)
			for index, item := range list {
				items = append(items, rawContributionEntry{index: index, key: key, raw: item})
			}
		}
		if total > spec.MaxEntries {
			return nil, fmt.Errorf("contributes.%s must contain at most %d %s", spec.Key, spec.MaxEntries, noun)
		}
	case api.ContributionShapeObject:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("contributes.%s must be an object", spec.Key)
		}
		items = append(items, rawContributionEntry{index: 0, raw: raw})
	default:
		return nil, fmt.Errorf("contributes.%s has unsupported shape %q", spec.Key, spec.Shape)
	}

	var entries []contributionEntry
	var issues []ContributionIssue
	seen := map[string]struct{}{}
	for _, item := range items {
		ctx.beginEntryAssets()
		data, err := resolveContributionEntryFile(ctx, point, item.raw)
		var value any
		var id string
		if err == nil {
			value, id, err = point.Decode(ctx, item.index, item.key, data)
		}
		if err == nil && id != "" && point.DuplicateMessage != nil {
			if _, duplicate := seen[id]; duplicate {
				err = errors.New(point.DuplicateMessage(id))
			}
		}
		if err != nil {
			ctx.discardEntryAssets()
			issues = append(issues, ContributionIssue{Point: spec.Key, Entry: item.index, Key: item.key, ID: id, Message: err.Error()})
			continue
		}
		if id != "" {
			seen[id] = struct{}{}
		}
		entries = append(entries, contributionEntry{Index: item.index, Key: item.key, ID: id, Value: value, Assets: ctx.commitEntryAssets()})
	}
	if point.Finalize != nil {
		entries, issues = point.Finalize(ctx, entries, issues)
	}
	result := &resolvedContribution{Entries: make([]any, 0, len(entries)), Issues: issues}
	for _, entry := range entries {
		result.Entries = append(result.Entries, entry.Value)
		for _, key := range entry.Assets {
			asset, ok := ctx.assets[key]
			if !ok {
				continue
			}
			if result.Assets == nil {
				result.Assets = map[string]contributionAsset{}
			}
			result.Assets[key] = asset
		}
	}
	fingerprint, err := json.Marshal(result.Entries)
	if err != nil {
		return nil, fmt.Errorf("contributes.%s: encode entries: %w", spec.Key, err)
	}
	sum := sha256.Sum256(fingerprint)
	result.Fingerprint = hex.EncodeToString(sum[:8])
	return result, nil
}

// resolveContributionEntryFile applies fileRef mode "entry": an object whose
// only key is the reference field is replaced by the referenced file content.
func resolveContributionEntryFile(ctx *pointContext, point *contributionPoint, raw json.RawMessage) (json.RawMessage, error) {
	spec := ctx.spec
	labels := point.FileLabels
	if labels.Noun == "" {
		labels.Noun = spec.Key
	}
	if labels.Entry == "" {
		labels.Entry = spec.Key + " entry"
	}
	if labels.ShapeHint == "" {
		labels.ShapeHint = "an object"
	}
	trimmed := bytes.TrimSpace(raw)
	if spec.InlineMaxBytes > 0 && int64(len(trimmed)) > spec.InlineMaxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", labels.Entry, spec.InlineMaxBytes)
	}
	if spec.FileRef == nil || spec.FileRef.Mode != "entry" {
		return trimmed, nil
	}
	field := spec.FileRef.Field
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("%s entry must be %s or {%q: ...}", labels.Noun, labels.ShapeHint, field)
	}
	if _, isReference := probe[field]; !isReference {
		return trimmed, nil
	}
	var path string
	if point.DecodeReference != nil {
		decoded, err := point.DecodeReference(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s file reference: %w", labels.Noun, err)
		}
		path = decoded
	} else {
		for key := range probe {
			if key != field {
				return nil, fmt.Errorf("%s file reference: json: unknown field %q", labels.Noun, key)
			}
		}
		if err := json.Unmarshal(probe[field], &path); err != nil {
			return nil, fmt.Errorf("%s file reference: %w", labels.Noun, err)
		}
	}
	return ctx.readFile(path, labels.Noun)
}

// pathResolver returns the context's shared package path resolver, creating
// one for root when the context was built without it (tests, ad-hoc decoding).
func (ctx *pointContext) pathResolver() *pluginPathResolver {
	if ctx.paths == nil {
		ctx.paths = newPluginPathResolver(ctx.root)
	}
	return ctx.paths
}

// readFile reads a package-relative contribution file: symlink-safe
// containment, regular files only, allowed extension, size-capped. Points with
// fileRef mode "field" call it from Decode.
func (ctx *pointContext) readFile(relative, noun string) ([]byte, error) {
	ref := ctx.spec.FileRef
	if ref == nil {
		return nil, fmt.Errorf("contributes.%s does not accept file references", ctx.spec.Key)
	}
	if noun == "" {
		noun = ctx.spec.Key
	}
	relative = filepath.ToSlash(strings.TrimSpace(relative))
	extOK := false
	for _, ext := range ref.Ext {
		if strings.HasSuffix(strings.ToLower(relative), ext) {
			extOK = true
			break
		}
	}
	if relative == "" || !extOK || len(relative) > ref.MaxPathLength {
		return nil, fmt.Errorf("%s path must be a package-relative %s file", noun, strings.Join(ref.Ext, " or "))
	}
	resolved, info, err := ctx.pathResolver().resolve(filepath.FromSlash(relative))
	if err != nil {
		return nil, fmt.Errorf("%s path %q: %w", noun, relative, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s path %q must be a regular file", noun, relative)
	}
	if info.Size() > ref.MaxBytes {
		return nil, fmt.Errorf("%s file %q exceeds %d bytes", noun, relative, ref.MaxBytes)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("%s path %q: %w", noun, relative, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, ref.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file %q: %w", noun, relative, err)
	}
	if int64(len(data)) > ref.MaxBytes {
		return nil, fmt.Errorf("%s file %q exceeds %d bytes", noun, relative, ref.MaxBytes)
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Assets.

func (ctx *pointContext) beginEntryAssets() {
	ctx.pending = nil
	ctx.pendingOrder = nil
}

func (ctx *pointContext) discardEntryAssets() {
	ctx.pending = nil
	ctx.pendingOrder = nil
}

// commitEntryAssets moves the current entry's assets into the committed set
// and returns their keys in read order.
func (ctx *pointContext) commitEntryAssets() []string {
	keys := ctx.pendingOrder
	for _, key := range keys {
		if _, exists := ctx.assets[key]; exists {
			continue
		}
		if ctx.assets == nil {
			ctx.assets = map[string]contributionAsset{}
		}
		asset := ctx.pending[key]
		ctx.assets[key] = asset
		ctx.assetBytes += asset.Size
	}
	ctx.pending = nil
	ctx.pendingOrder = nil
	return keys
}

// assetExtension returns the lower-case allowed extension of relative, or "".
func assetExtension(assets *api.ContributionAssets, relative string) string {
	lower := strings.ToLower(relative)
	for _, ext := range assets.Ext {
		if strings.HasSuffix(lower, ext) {
			return ext
		}
	}
	return ""
}

// assetInDirs reports whether relative lies under one of the asset dirs.
func assetInDirs(assets *api.ContributionAssets, relative string) bool {
	for _, dir := range assets.Dirs {
		if strings.HasPrefix(relative, dir+"/") {
			return true
		}
	}
	return false
}

// readContributionAssetFile reads and validates one asset: symlink-safe
// containment under assets.dirs, regular file, allowed extension, path length
// and size (raw and canonical). It returns the canonical bytes and MIME.
func readContributionAssetFile(root string, assets *api.ContributionAssets, validate func(hint, ext string, data []byte) ([]byte, string, error), relative, hint string) (string, []byte, string, error) {
	return readContributionAssetFileWith(newPluginPathResolver(root), assets, validate, relative, hint)
}

// readContributionAssetFileWith is readContributionAssetFile with a shared
// path resolver (one Lstat per asset once its directory is verified).
func readContributionAssetFileWith(paths *pluginPathResolver, assets *api.ContributionAssets, validate func(hint, ext string, data []byte) ([]byte, string, error), relative, hint string) (string, []byte, string, error) {
	relative = filepath.ToSlash(strings.TrimSpace(relative))
	ext := assetExtension(assets, relative)
	if relative == "" || ext == "" || len(relative) > assets.MaxPathLength || !assetInDirs(assets, relative) {
		return "", nil, "", fmt.Errorf("%s path must be a package-relative %s file under %s/", hint, strings.Join(assets.Ext, " or "), strings.Join(assets.Dirs, "/ or "))
	}
	resolved, info, err := paths.resolve(filepath.FromSlash(relative))
	if err != nil {
		return "", nil, "", fmt.Errorf("%s path %q: %w", hint, relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, "", fmt.Errorf("%s path %q must be a regular file", hint, relative)
	}
	if info.Size() > assets.MaxBytes {
		return "", nil, "", fmt.Errorf("%s file %q exceeds %d bytes", hint, relative, assets.MaxBytes)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", nil, "", fmt.Errorf("%s path %q: %w", hint, relative, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, assets.MaxBytes+1))
	if err != nil {
		return "", nil, "", fmt.Errorf("read %s file %q: %w", hint, relative, err)
	}
	if int64(len(data)) > assets.MaxBytes {
		return "", nil, "", fmt.Errorf("%s file %q exceeds %d bytes", hint, relative, assets.MaxBytes)
	}
	canonical, mime, err := validate(hint, ext, data)
	if err != nil {
		return "", nil, "", fmt.Errorf("%s file %q: %w", hint, relative, err)
	}
	if int64(len(canonical)) > assets.MaxBytes || mime == "" {
		return "", nil, "", fmt.Errorf("%s file %q: canonical form exceeds %d bytes or has no type", hint, relative, assets.MaxBytes)
	}
	return ext, canonical, mime, nil
}

// contributionAssetKey is the hex sha256 of an asset's canonical bytes.
func contributionAssetKey(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// readAsset validates a package asset referenced by the entry being decoded
// and returns its content-hash key (64 hex characters), which is what the
// entry value carries into the snapshot. The asset counts toward the
// contract's assets.maxCount/maxTotalBytes for this plugin and point and is
// kept only if the entry is accepted and survives Finalize.
func (ctx *pointContext) readAsset(relative, hint string) (string, error) {
	assets := ctx.spec.Assets
	if assets == nil || ctx.validateAsset == nil {
		return "", fmt.Errorf("contributes.%s does not accept assets", ctx.spec.Key)
	}
	if hint == "" {
		hint = ctx.spec.Key + " asset"
	}
	ext, canonical, mime, err := readContributionAssetFileWith(ctx.pathResolver(), assets, ctx.validateAsset, relative, hint)
	if err != nil {
		return "", err
	}
	key := contributionAssetKey(canonical)
	if _, committed := ctx.assets[key]; committed {
		ctx.trackPending(key, ctx.assets[key])
		return key, nil
	}
	if _, pending := ctx.pending[key]; pending {
		return key, nil
	}
	count, total := len(ctx.assets), ctx.assetBytes
	for _, pendingKey := range ctx.pendingOrder {
		if _, committed := ctx.assets[pendingKey]; !committed {
			count++
			total += ctx.pending[pendingKey].Size
		}
	}
	if count+1 > assets.MaxCount {
		return "", fmt.Errorf("contributes.%s must reference at most %d assets", ctx.spec.Key, assets.MaxCount)
	}
	if total+int64(len(canonical)) > assets.MaxTotalBytes {
		return "", fmt.Errorf("contributes.%s assets must total at most %d bytes", ctx.spec.Key, assets.MaxTotalBytes)
	}
	ctx.trackPending(key, contributionAsset{Path: filepath.ToSlash(strings.TrimSpace(relative)), Hint: hint, Ext: ext, Mime: mime, Size: int64(len(canonical))})
	return key, nil
}

func (ctx *pointContext) trackPending(key string, asset contributionAsset) {
	if _, exists := ctx.pending[key]; exists {
		return
	}
	if ctx.pending == nil {
		ctx.pending = map[string]contributionAsset{}
	}
	ctx.pending[key] = asset
	ctx.pendingOrder = append(ctx.pendingOrder, key)
}

// ---------------------------------------------------------------------------
// Manifest accessors.

func (manifest Manifest) contribution(key string) *resolvedContribution {
	return manifest.contributions[key]
}

// contributionKeys lists the resolved points of a manifest in sorted order.
func (manifest Manifest) contributionKeys() []string {
	keys := make([]string, 0, len(manifest.contributions))
	for key := range manifest.contributions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// contributionIssuesError makes skipped entries fatal; used by install,
// upgrade and install preview so a package never installs with a broken
// contribution. Each point keeps its own prefix ("invalid keymap contribution: …").
func (manifest Manifest) contributionIssuesError() error {
	var parts []string
	for _, key := range manifest.contributionKeys() {
		resolved := manifest.contributions[key]
		if len(resolved.Issues) == 0 {
			continue
		}
		point, _, _ := enabledContributionPoint(key)
		prefix := "invalid " + key + " contribution"
		if point != nil {
			prefix = point.IssuePrefix
		}
		messages := make([]string, 0, len(resolved.Issues))
		for _, issue := range resolved.Issues {
			messages = append(messages, issue.Message)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", prefix, strings.Join(messages, "; ")))
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}

func contributionIssueText(issue ContributionIssue) string {
	if point, _, ok := enabledContributionPoint(issue.Point); ok && point.IssueText != nil {
		return point.IssueText(issue)
	}
	if issue.ID != "" {
		return fmt.Sprintf("skipped %s entry %q: %s", issue.Point, issue.ID, issue.Message)
	}
	if issue.Key != "" {
		return fmt.Sprintf("skipped contributes.%s[%q][%d]: %s", issue.Point, issue.Key, issue.Entry, issue.Message)
	}
	if issue.Entry >= 0 {
		return fmt.Sprintf("skipped contributes.%s[%d]: %s", issue.Point, issue.Entry, issue.Message)
	}
	return fmt.Sprintf("skipped %s entry: %s", issue.Point, issue.Message)
}

// allContributionIssues returns every issue in point-key order.
func (manifest Manifest) allContributionIssues() []ContributionIssue {
	var issues []ContributionIssue
	for _, key := range manifest.contributionKeys() {
		issues = append(issues, manifest.contributions[key].Issues...)
	}
	return issues
}

// logContributionIssuesLocked writes skipped entries to the plugin log; caller
// holds record.mu or owns an unpublished record.
func (record *pluginRecord) logContributionIssuesLocked(manifest Manifest) {
	for _, issue := range manifest.allContributionIssues() {
		record.appendLogLocked("warn", contributionIssueText(issue))
	}
}

// contributionsExpandPermission reports a development generation whose
// contribution changes need a newly approved session.
func contributionsExpandPermission(current, candidate Manifest) bool {
	for _, key := range candidate.contributionKeys() {
		point, _, ok := enabledContributionPoint(key)
		if !ok || point.ExpandsPermission == nil {
			continue
		}
		if point.ExpandsPermission(current.contribution(key), candidate.contribution(key)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Snapshot command and change event.

type pluginContributionPointDTO struct {
	Fingerprint string              `json:"fingerprint"`
	Entries     []any               `json:"entries"`
	Issues      []ContributionIssue `json:"issues"`
	Runtime     any                 `json:"runtime,omitempty"`
}

type pluginContributionsDTO struct {
	PluginID          string                                `json:"pluginId"`
	PluginName        string                                `json:"pluginName"`
	Version           string                                `json:"version"`
	Source            string                                `json:"source"`
	Runtime           string                                `json:"runtime"`
	State             string                                `json:"state"`
	ActiveInWorkspace bool                                  `json:"activeInWorkspace"`
	Points            map[string]pluginContributionPointDTO `json:"points"`
}

type pluginContributionsSnapshot struct {
	Revision uint64                   `json:"revision"`
	Plugins  []pluginContributionsDTO `json:"plugins"`
}

type pluginContributionsChangedDTO struct {
	Revision uint64   `json:"revision"`
	PluginID string   `json:"pluginId,omitempty"`
	Points   []string `json:"points,omitempty"`
}

// contributionRevision is manager-wide and only increases; the frontend uses it
// to discard stale snapshots.
var contributionRevision atomic.Uint64

// Contributions lists contributions of enabled, non-failed plugins. Declarative
// plugins are always listed (they never start a process). Process plugins are
// listed when their static contributions are available; points with scope
// "plugin" additionally require the plugin to be effective in the active
// workspace, while "global" points (keymaps, themes) do not. Nothing is
// activated here.
func (manager *pluginManager) Contributions(points []string) pluginContributionsSnapshot {
	filter := map[string]struct{}{}
	for _, point := range points {
		if point = strings.TrimSpace(point); point != "" {
			filter[point] = struct{}{}
		}
	}
	revision := contributionRevision.Load()
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	result := pluginContributionsSnapshot{Revision: revision, Plugins: []pluginContributionsDTO{}}
	for _, record := range records {
		record.mu.Lock()
		if !record.contributionsListedLocked() {
			record.mu.Unlock()
			continue
		}
		manifest := record.manifest
		declarative := manifest.isDeclarative()
		activeInWorkspace := manager.workspaceActiveLocked(record)
		dto := pluginContributionsDTO{
			PluginID: record.id, PluginName: manifest.Name, Version: manifest.Version, Source: record.source,
			Runtime: manifest.Runtime.Type, State: record.state, ActiveInWorkspace: activeInWorkspace,
			Points: map[string]pluginContributionPointDTO{},
		}
		for _, key := range manifest.contributionKeys() {
			if _, wanted := filter[key]; len(filter) > 0 && !wanted {
				continue
			}
			point, spec, ok := enabledContributionPoint(key)
			if !ok {
				continue
			}
			if spec.Scope == "plugin" && !declarative && !activeInWorkspace {
				continue
			}
			resolved := manifest.contributions[key]
			pointDTO := pluginContributionPointDTO{
				Fingerprint: resolved.Fingerprint,
				Entries:     append([]any{}, resolved.Entries...),
				Issues:      append([]ContributionIssue{}, resolved.Issues...),
			}
			if point.RuntimeOverlay != nil {
				pointDTO.Runtime = point.RuntimeOverlay(record)
			}
			dto.Points[key] = pointDTO
		}
		record.mu.Unlock()
		if len(dto.Points) > 0 {
			result.Plugins = append(result.Plugins, dto)
		}
	}
	sort.SliceStable(result.Plugins, func(i, j int) bool { return result.Plugins[i].PluginID < result.Plugins[j].PluginID })
	return result
}

// contributionsListedLocked reports whether Contributions lists this record
// at all: enabled, has resolved contributions, not failed, and (for process
// plugins) static contributions are available. Caller holds record.mu.
func (record *pluginRecord) contributionsListedLocked() bool {
	if !record.enabled || record.manifest == nil || record.state == pluginStateError || len(record.manifest.contributions) == 0 {
		return false
	}
	return record.manifest.isDeclarative() || record.contributionsAvailableLocked()
}

// emitContributionsChanged bumps the revision and tells the frontend which
// plugin (empty: all) and points (empty: all) to re-read.
func (manager *pluginManager) emitContributionsChanged(pluginID string, points []string) {
	revision := contributionRevision.Add(1)
	core.Emit(pluginContributionsChangedEvent, pluginContributionsChangedDTO{Revision: revision, PluginID: pluginID, Points: points})
	manager.globalShortcutsContributionsChanged(points)
	manager.jdtlsBundlesContributionsChanged(points)
}

// emitUninstalled announces a removed package on both the state and the
// contributions channels.
func (manager *pluginManager) emitUninstalled(id string) {
	core.Emit(pluginStateChangedEvent, pluginStateEvent{ID: id, State: "uninstalled", Enabled: false})
	manager.emitContributionsChanged(id, nil)
}

// maxContributionsRequestPoints bounds one snapshot request. The frontend
// registers a consumer for (almost) every point, so the bound follows the
// contract instead of a fixed number that new points would outgrow.
var maxContributionsRequestPoints = len(api.ContributionPointSpecs())

func pluginHostContributionsHandler(_, args string) (string, error) {
	var request struct {
		Points []string `json:"points"`
	}
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &request); err != nil {
			return "", fmt.Errorf("plugin_host_contributions: invalid arguments: %w", err)
		}
	}
	if len(request.Points) > maxContributionsRequestPoints {
		return "", errors.New("plugin_host_contributions: too many points")
	}
	return core.EncodeResult(host().Contributions(request.Points))
}

// ---------------------------------------------------------------------------
// Seams for points that add runtime behaviour.

type hostMethodHandler func(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError)

type hostMethodRegistration struct {
	name       string
	capability api.Capability
	// gate reports an additional availability condition (for example a manifest
	// flag); nil means available whenever the capability is granted.
	gate func(runtimeHost *processRuntime) bool
	// gateNotFound answers a closed gate with -32601 (as if the method did
	// not exist, checked before the capability) instead of -32001, so SDKs
	// treat it like a host that predates the method.
	gateNotFound      bool
	handler           hostMethodHandler
	generationHandler func(*processRuntime, uint64, json.RawMessage) (any, *rpcError)
}

var (
	hostMethodsMu sync.RWMutex
	hostMethods   = map[string]hostMethodRegistration{}
)

// registerHostMethod adds a host.* JSON-RPC method served by
// processRuntime.handleHostRequest and listed by host.api.describe. An empty
// capability means no capability is required. Call from init().
func registerHostMethod(name string, gate func(runtimeHost *processRuntime) bool, capability api.Capability, handler hostMethodHandler) {
	if !strings.HasPrefix(name, "host.") || handler == nil {
		panic(fmt.Sprintf("invalid host method registration %q", name))
	}
	hostMethodsMu.Lock()
	defer hostMethodsMu.Unlock()
	if _, exists := hostMethods[name]; exists {
		panic(fmt.Sprintf("host method %q registered twice", name))
	}
	hostMethods[name] = hostMethodRegistration{name: name, capability: capability, gate: gate, handler: handler}
}

// registerGenerationHostMethod is the same registration seam, with immutable
// request-generation provenance for persistent mutations that outlive dispatch.
func registerGenerationHostMethod(name string, gate func(*processRuntime) bool, capability api.Capability, handler func(*processRuntime, uint64, json.RawMessage) (any, *rpcError)) {
	registerHostMethod(name, gate, capability, func(*processRuntime, json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: -32001, Message: "generation required"}
	})
	hostMethodsMu.Lock()
	entry := hostMethods[name]
	entry.generationHandler = handler
	hostMethods[name] = entry
	hostMethodsMu.Unlock()
}

// markHostMethodGateNotFound makes a registered method answer a closed gate
// with -32601 "host method not found". Call from init() after registering.
func markHostMethodGateNotFound(name string) {
	hostMethodsMu.Lock()
	defer hostMethodsMu.Unlock()
	entry, ok := hostMethods[name]
	if !ok || entry.gate == nil {
		panic(fmt.Sprintf("host method %q has no gate", name))
	}
	entry.gateNotFound = true
	hostMethods[name] = entry
}

func registeredHostMethod(name string) (hostMethodRegistration, bool) {
	hostMethodsMu.RLock()
	defer hostMethodsMu.RUnlock()
	registration, ok := hostMethods[name]
	return registration, ok
}

func registeredHostMethods() []hostMethodRegistration {
	hostMethodsMu.RLock()
	defer hostMethodsMu.RUnlock()
	result := make([]hostMethodRegistration, 0, len(hostMethods))
	for _, registration := range hostMethods {
		result = append(result, registration)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}

// available reports capability and gate admission for one runtime.
func (registration hostMethodRegistration) available(runtimeHost *processRuntime) bool {
	if registration.capability != "" {
		if _, granted := runtimeHost.capabilities[registration.capability]; !granted {
			return false
		}
	}
	return registration.gate == nil || registration.gate(runtimeHost)
}

// serveRegisteredHostMethod handles a method registered through the seam.
// It returns false when the method is unknown.
func (runtimeHost *processRuntime) serveRegisteredHostMethod(message rpcEnvelope, respond func(any, *rpcError)) bool {
	registration, ok := registeredHostMethod(message.Method)
	if !ok {
		return false
	}
	if registration.gateNotFound && !registration.gate(runtimeHost) {
		respond(nil, &rpcError{Code: -32601, Message: "host method not found"})
		return true
	}
	if registration.capability != "" {
		if _, granted := runtimeHost.capabilities[registration.capability]; !granted {
			respond(nil, &rpcError{Code: -32001, Message: string(registration.capability) + " capability not granted"})
			return true
		}
	}
	if registration.gate != nil && !registration.gate(runtimeHost) {
		respond(nil, &rpcError{Code: -32001, Message: message.Method + " is not available for this plugin"})
		return true
	}
	if registration.generationHandler != nil {
		result, rpcErr := registration.generationHandler(runtimeHost, message.hostGeneration, message.Params)
		respond(result, rpcErr)
		return true
	}
	result, rpcErr := registration.handler(runtimeHost, message.Params)
	respond(result, rpcErr)
	return true
}

var (
	recordLifecycleHooksMu sync.RWMutex
	recordLifecycleHooks   []func(manager *pluginManager, record *pluginRecord)
)

// registerRecordLifecycleHook runs onStop whenever plugin-provided runtime
// state ends: stop, crash, disable, reload, uninstall and development
// generation changes (the same places that clear host.context.set values).
// Hooks run without record.mu held. Call from init().
func registerRecordLifecycleHook(onStop func(manager *pluginManager, record *pluginRecord)) {
	if onStop == nil {
		panic("nil record lifecycle hook")
	}
	recordLifecycleHooksMu.Lock()
	defer recordLifecycleHooksMu.Unlock()
	recordLifecycleHooks = append(recordLifecycleHooks, onStop)
}

func (manager *pluginManager) runRecordLifecycleHooks(record *pluginRecord) {
	recordLifecycleHooksMu.RLock()
	hooks := append([]func(*pluginManager, *pluginRecord){}, recordLifecycleHooks...)
	recordLifecycleHooksMu.RUnlock()
	for _, hook := range hooks {
		hook(manager, record)
	}
}

// removalReason tells a record removal hook why plugin-owned data goes away.
type removalReason string

const (
	removalReasonUninstall removalReason = "uninstall"
	removalReasonClearData removalReason = "clearData"
)

var (
	recordRemovalHooksMu sync.RWMutex
	recordRemovalHooks   []func(manager *pluginManager, pluginID string, reason removalReason)
)

// registerRecordRemovalHook runs when a plugin is uninstalled (after its
// record is removed) or its data is cleared with Clear Plugin Data, so points
// can drop per-plugin host state such as caches or stored preferences. It is
// not called when a package merely disappears during directory sync. Hooks
// run without manager.mu or record.mu held. Call from init().
func registerRecordRemovalHook(hook func(manager *pluginManager, pluginID string, reason removalReason)) {
	if hook == nil {
		panic("nil record removal hook")
	}
	recordRemovalHooksMu.Lock()
	defer recordRemovalHooksMu.Unlock()
	recordRemovalHooks = append(recordRemovalHooks, hook)
}

func (manager *pluginManager) runRecordRemovalHooks(pluginID string, reason removalReason) {
	recordRemovalHooksMu.RLock()
	hooks := append([]func(*pluginManager, string, removalReason){}, recordRemovalHooks...)
	recordRemovalHooksMu.RUnlock()
	for _, hook := range hooks {
		hook(manager, pluginID, reason)
	}
}

func init() {
	core.Register("plugin_host_contributions", pluginHostContributionsHandler)
}
