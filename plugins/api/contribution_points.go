package api

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// contributionPointsJSON is the single source of truth for plugin contribution
// points. vue-app/scripts/build-plugin-contracts.mjs copies it to the CLI,
// cloud, devkit and frontend; check:plugin-contracts fails on drift.
//
//go:embed contribution-points.json
var contributionPointsJSON []byte

// Contribution point status values. Only enabled points are accepted by the
// desktop host, the CLI and the cloud; planned points are reserved names that
// still fail closed as unknown until their phase registers an implementation.
const (
	ContributionStatusEnabled = "enabled"
	ContributionStatusPlanned = "planned"
)

// Contribution point shapes: list is a JSON array of entries, map is an object
// of fixed keys to arrays, object is a single object (configuration).
const (
	ContributionShapeList   = "list"
	ContributionShapeMap    = "map"
	ContributionShapeObject = "object"
)

// ContributionFileRef describes how a point may reference package files.
// Mode "entry" means an entry may be exactly {"path": "..."} whose file holds
// the entry; mode "field" means each entry carries a path field. In mode
// "field", Fields optionally lists every entry field that may hold a package
// path (Field is the primary one and must be among them); it defaults to
// [Field]. All fields share Dirs, Ext, MaxBytes and MaxPathLength.
type ContributionFileRef struct {
	Mode          string   `json:"mode"`
	Field         string   `json:"field"`
	Fields        []string `json:"fields,omitempty"`
	Dirs          []string `json:"dirs"`
	Ext           []string `json:"ext"`
	MaxBytes      int64    `json:"maxBytes"`
	MaxPathLength int      `json:"maxPathLength"`
}

// FieldNames returns every entry field that may hold a package path: Fields
// when set, otherwise [Field].
func (ref ContributionFileRef) FieldNames() []string {
	if len(ref.Fields) > 0 {
		return append([]string(nil), ref.Fields...)
	}
	return []string{ref.Field}
}

// ContributionAssets describes package files a point references from inside
// its entries (after fileRef inlining) and that the host serves on demand
// instead of inlining into the contributions snapshot. Selectors are dotted
// paths into an entry; "*" matches every value of an object or every element
// of an array (e.g. "steps.*.image"). MaxCount and
// MaxTotalBytes bound one plugin's assets for this point; MaxBytes bounds one
// file (and its canonical form) and never exceeds MaxAssetBytesLimit so a
// single base64 asset always fits a plugin_host_contribution_assets response.
type ContributionAssets struct {
	Dirs          []string `json:"dirs"`
	Ext           []string `json:"ext"`
	MaxBytes      int64    `json:"maxBytes"`
	MaxPathLength int      `json:"maxPathLength"`
	MaxCount      int      `json:"maxCount"`
	MaxTotalBytes int64    `json:"maxTotalBytes"`
	Selectors     []string `json:"selectors"`
}

// MaxAssetBytesLimit caps assets.maxBytes: base64 of 1.5 MiB is 2 MiB, the
// asset command's response budget.
const MaxAssetBytesLimit = 1572864

// ContributionThemeTokens lists the semantic theme tokens a theme may define.
type ContributionThemeTokens struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

// ContributionPointSpec is one contract entry. Limits holds point-specific
// numeric bounds read by the point implementation. ReservedIDs (languages)
// lists entry IDs owned by the host; SensitiveFilenames (customEditors) lists
// sample filenames that default-priority patterns must never match.
type ContributionPointSpec struct {
	Key                string                   `json:"-"`
	Status             string                   `json:"status"`
	Kinds              []string                 `json:"kinds"`
	MinAPIVersion      int                      `json:"minApiVersion"`
	SinceHost          string                   `json:"sinceHost"`
	Shape              string                   `json:"shape"`
	MaxEntries         int                      `json:"maxEntries,omitempty"`
	MaxPerKey          int                      `json:"maxPerKey,omitempty"`
	MaxProperties      int                      `json:"maxProperties,omitempty"`
	InlineMaxBytes     int64                    `json:"inlineMaxBytes,omitempty"`
	Scope              string                   `json:"scope"`
	IDPrefix           string                   `json:"idPrefix,omitempty"`
	Keys               []string                 `json:"keys,omitempty"`
	FileRef            *ContributionFileRef     `json:"fileRef,omitempty"`
	Limits             map[string]int64         `json:"limits,omitempty"`
	Tokens             *ContributionThemeTokens `json:"tokens,omitempty"`
	MonacoColors       []string                 `json:"monacoColors,omitempty"`
	TerminalKeys       []string                 `json:"terminalKeys,omitempty"`
	ReservedIDs        []string                 `json:"reservedIds,omitempty"`
	SensitiveFilenames []string                 `json:"sensitiveFilenames,omitempty"`
	// RequiresCapabilities lists manifest capabilities a plugin must declare
	// to contribute to this point (fatal in strict and lenient mode).
	RequiresCapabilities []string `json:"requiresCapabilities,omitempty"`
	// Assets describes package files served through the asset command.
	Assets *ContributionAssets `json:"assets,omitempty"`
	// Lists holds named point-specific string lists read with List.
	Lists map[string][]string `json:"lists,omitempty"`
	// KeySinceHost overrides SinceHost for additions made after the point
	// itself shipped: for map points a map key, for list points an optional
	// entry field (for example testProviders debugRun, host 0.3.0); a package
	// that uses the key or field needs that host. Read it through KeySince.
	KeySinceHost map[string]string `json:"keySinceHost,omitempty"`
	// ValueSinceHost (list points only) overrides SinceHost for values of a
	// named list added after the point itself: the outer key is a Lists name
	// (and the entry field that takes those values), the inner map gives each
	// value's first host version. Read it through ValueSince.
	ValueSinceHost map[string]map[string]string `json:"valueSinceHost,omitempty"`
}

// ValueSince returns the first host version that accepts a value of a named
// list: its ValueSinceHost override, otherwise the point's SinceHost. Like
// KeySince the host itself does not call it; it mirrors the cloud
// marketplace's min_host_version floor computation for parity tests.
func (spec ContributionPointSpec) ValueSince(list, value string) string {
	if version, ok := spec.ValueSinceHost[list][value]; ok {
		return version
	}
	return spec.SinceHost
}

// KeySince returns the first host version that accepts a map key: its
// KeySinceHost override, otherwise the point's SinceHost. The host itself does
// not call it; it mirrors the cloud marketplace's min_host_version floor
// computation and is kept so parity tests can pin the contract to it.
func (spec ContributionPointSpec) KeySince(key string) string {
	if version, ok := spec.KeySinceHost[key]; ok {
		return version
	}
	return spec.SinceHost
}

// Enabled reports whether hosts accept this point.
func (spec ContributionPointSpec) Enabled() bool { return spec.Status == ContributionStatusEnabled }

// AllowsKind reports whether a runtime.type may contribute to this point.
func (spec ContributionPointSpec) AllowsKind(kind string) bool {
	for _, allowed := range spec.Kinds {
		if allowed == kind {
			return true
		}
	}
	return false
}

// Limit returns a named point limit; it panics on a missing limit because a
// point implementation reading an undefined bound is a programming error.
func (spec ContributionPointSpec) Limit(name string) int64 {
	value, ok := spec.Limits[name]
	if !ok {
		panic(fmt.Sprintf("contribution point %q has no limit %q", spec.Key, name))
	}
	return value
}

// List returns a named point string list; like Limit it panics on a missing
// list because reading an undefined list is a programming error.
func (spec ContributionPointSpec) List(name string) []string {
	values, ok := spec.Lists[name]
	if !ok {
		panic(fmt.Sprintf("contribution point %q has no list %q", spec.Key, name))
	}
	return append([]string(nil), values...)
}

// knownContributionCapabilities is every capability a point may require.
func knownContributionCapabilities() map[string]bool {
	known := map[string]bool{}
	for _, capability := range append(CapabilitiesV1(), CapabilitiesV2Only()...) {
		known[string(capability)] = true
	}
	return known
}

// ContributionPointsContract is the decoded contract file.
type ContributionPointsContract struct {
	Comment          string                           `json:"$comment,omitempty"`
	Version          int                              `json:"version"`
	ManifestMaxBytes int64                            `json:"manifestMaxBytes"`
	Points           map[string]ContributionPointSpec `json:"points"`
	// CapabilitySinceHost lists capabilities added after host 0.1.0 with the
	// first host version that knows them; read it through CapabilitySince.
	CapabilitySinceHost map[string]string `json:"capabilitySinceHost,omitempty"`
}

// CapabilitySince returns the first host version that knows a capability, or
// "" when the capability has no entry (it predates the field). Like KeySince
// it is unused by the host: it mirrors the cloud floor computation and is kept
// for parity tests.
func (contract ContributionPointsContract) CapabilitySince(capability Capability) string {
	return contract.CapabilitySinceHost[string(capability)]
}

var (
	contributionContractOnce sync.Once
	contributionContract     ContributionPointsContract
	contributionContractErr  error

	contractVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	// contractEntryFieldPattern is a list point entry field named by keySinceHost.
	contractEntryFieldPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
)

func loadContributionContract() {
	contributionContract, contributionContractErr = parseContributionContract(contributionPointsJSON)
}

// parseContributionContract strictly decodes and validates contract bytes.
func parseContributionContract(data []byte) (ContributionPointsContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var contract ContributionPointsContract
	if err := decoder.Decode(&contract); err != nil {
		return ContributionPointsContract{}, fmt.Errorf("decode contribution-points.json: %w", err)
	}
	for key, spec := range contract.Points {
		spec.Key = key
		if err := validateContributionSpec(spec); err != nil {
			return ContributionPointsContract{}, fmt.Errorf("contribution point %q: %w", key, err)
		}
		contract.Points[key] = spec
	}
	if contract.Version < 1 || contract.ManifestMaxBytes <= 0 {
		return ContributionPointsContract{}, fmt.Errorf("contribution-points.json needs version and manifestMaxBytes")
	}
	known := knownContributionCapabilities()
	for capability, version := range contract.CapabilitySinceHost {
		if !known[capability] {
			return ContributionPointsContract{}, fmt.Errorf("capabilitySinceHost: unknown capability %q", capability)
		}
		if !contractVersionPattern.MatchString(version) {
			return ContributionPointsContract{}, fmt.Errorf("capabilitySinceHost.%s: version %q must be MAJOR.MINOR.PATCH", capability, version)
		}
	}
	return contract, nil
}

// compareContractVersions compares two MAJOR.MINOR.PATCH versions already
// checked against contractVersionPattern.
func compareContractVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for index := range 3 {
		x, _ := strconv.Atoi(left[index])
		y, _ := strconv.Atoi(right[index])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func validateContributionSpec(spec ContributionPointSpec) error {
	if spec.Status != ContributionStatusEnabled && spec.Status != ContributionStatusPlanned {
		return fmt.Errorf("unknown status %q", spec.Status)
	}
	if len(spec.Kinds) == 0 {
		return fmt.Errorf("kinds must not be empty")
	}
	for _, kind := range spec.Kinds {
		if kind != "process" && kind != "declarative" {
			return fmt.Errorf("unknown kind %q", kind)
		}
	}
	if spec.MinAPIVersion != ManifestAPIVersionV2 {
		return fmt.Errorf("minApiVersion must be 2")
	}
	if spec.SinceHost == "" {
		return fmt.Errorf("sinceHost is required")
	}
	switch spec.Shape {
	case ContributionShapeList, ContributionShapeMap:
		if spec.MaxEntries <= 0 {
			return fmt.Errorf("maxEntries must be positive")
		}
	case ContributionShapeObject:
		if spec.MaxProperties <= 0 {
			return fmt.Errorf("maxProperties must be positive")
		}
	default:
		return fmt.Errorf("unknown shape %q", spec.Shape)
	}
	if spec.Shape == ContributionShapeMap && (len(spec.Keys) == 0 || spec.MaxPerKey <= 0) {
		return fmt.Errorf("map points need keys and maxPerKey")
	}
	if len(spec.KeySinceHost) > 0 {
		if spec.Shape != ContributionShapeMap && spec.Shape != ContributionShapeList {
			return fmt.Errorf("keySinceHost is only valid for map and list points")
		}
		if !contractVersionPattern.MatchString(spec.SinceHost) {
			return fmt.Errorf("sinceHost %q must be MAJOR.MINOR.PATCH when keySinceHost is set", spec.SinceHost)
		}
		for key, version := range spec.KeySinceHost {
			if spec.Shape == ContributionShapeMap && !slices.Contains(spec.Keys, key) {
				return fmt.Errorf("keySinceHost key %q is not one of keys", key)
			}
			if spec.Shape == ContributionShapeList && !contractEntryFieldPattern.MatchString(key) {
				return fmt.Errorf("keySinceHost key %q must be an entry field name", key)
			}
			if !contractVersionPattern.MatchString(version) {
				return fmt.Errorf("keySinceHost.%s: version %q must be MAJOR.MINOR.PATCH", key, version)
			}
			if compareContractVersions(version, spec.SinceHost) < 0 {
				return fmt.Errorf("keySinceHost.%s: version %s is lower than sinceHost %s", key, version, spec.SinceHost)
			}
		}
	}
	for name, values := range map[string][]string{"reservedIds": spec.ReservedIDs, "sensitiveFilenames": spec.SensitiveFilenames} {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || seen[value] {
				return fmt.Errorf("%s must be unique non-empty strings", name)
			}
			seen[value] = true
		}
	}
	if len(spec.ValueSinceHost) > 0 {
		if spec.Shape != ContributionShapeList {
			return fmt.Errorf("valueSinceHost is only valid for list points")
		}
		if !contractVersionPattern.MatchString(spec.SinceHost) {
			return fmt.Errorf("sinceHost %q must be MAJOR.MINOR.PATCH when valueSinceHost is set", spec.SinceHost)
		}
		for list, values := range spec.ValueSinceHost {
			known, ok := spec.Lists[list]
			if !ok {
				return fmt.Errorf("valueSinceHost list %q is not one of lists", list)
			}
			if len(values) == 0 {
				return fmt.Errorf("valueSinceHost.%s must not be empty", list)
			}
			for value, version := range values {
				if !slices.Contains(known, value) {
					return fmt.Errorf("valueSinceHost.%s value %q is not in lists.%s", list, value, list)
				}
				if !contractVersionPattern.MatchString(version) {
					return fmt.Errorf("valueSinceHost.%s.%s: version %q must be MAJOR.MINOR.PATCH", list, value, version)
				}
				if compareContractVersions(version, spec.SinceHost) < 0 {
					return fmt.Errorf("valueSinceHost.%s.%s: version %s is lower than sinceHost %s", list, value, version, spec.SinceHost)
				}
			}
		}
	}
	if spec.Scope != "global" && spec.Scope != "plugin" {
		return fmt.Errorf("unknown scope %q", spec.Scope)
	}
	known := knownContributionCapabilities()
	seenCapabilities := map[string]bool{}
	for _, capability := range spec.RequiresCapabilities {
		if !known[capability] || seenCapabilities[capability] {
			return fmt.Errorf("requiresCapabilities must be unique known capabilities, got %q", capability)
		}
		seenCapabilities[capability] = true
	}
	for name, values := range spec.Lists {
		if name == "" || len(values) == 0 {
			return fmt.Errorf("lists.%s must be a non-empty list", name)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || seen[value] {
				return fmt.Errorf("lists.%s must be unique non-empty strings", name)
			}
			seen[value] = true
		}
	}
	if assets := spec.Assets; assets != nil {
		if len(assets.Dirs) == 0 || len(assets.Ext) == 0 || len(assets.Selectors) == 0 {
			return fmt.Errorf("assets needs dirs, ext and selectors")
		}
		if assets.MaxBytes <= 0 || assets.MaxBytes > MaxAssetBytesLimit || assets.MaxPathLength <= 0 || assets.MaxCount <= 0 || assets.MaxTotalBytes <= 0 {
			return fmt.Errorf("assets bounds must be positive and maxBytes at most %d", MaxAssetBytesLimit)
		}
		for _, ext := range assets.Ext {
			if len(ext) < 2 || ext[0] != '.' || ext != strings.ToLower(ext) {
				return fmt.Errorf("assets.ext %q must be a lower-case extension", ext)
			}
		}
		for _, dir := range assets.Dirs {
			if dir == "" || strings.ContainsAny(dir, "/\\:") || dir == "." || dir == ".." {
				return fmt.Errorf("assets.dirs %q must be a top-level directory name", dir)
			}
		}
		for _, selector := range assets.Selectors {
			for _, part := range strings.Split(selector, ".") {
				if part == "" {
					return fmt.Errorf("assets.selectors %q must be a dotted path", selector)
				}
			}
		}
	}
	if ref := spec.FileRef; ref != nil {
		if (ref.Mode != "entry" && ref.Mode != "field") || ref.Field == "" || len(ref.Dirs) == 0 || len(ref.Ext) == 0 || ref.MaxBytes <= 0 || ref.MaxPathLength <= 0 {
			return fmt.Errorf("fileRef needs mode entry|field, field, dirs, ext, maxBytes and maxPathLength")
		}
		if len(ref.Fields) > 0 {
			if ref.Mode != "field" {
				return fmt.Errorf("fileRef.fields is only valid in mode field")
			}
			seen := map[string]bool{}
			for _, field := range ref.Fields {
				if field == "" || seen[field] {
					return fmt.Errorf("fileRef.fields must be unique non-empty names")
				}
				seen[field] = true
			}
			if !seen[ref.Field] {
				return fmt.Errorf("fileRef.fields must include fileRef.field %q", ref.Field)
			}
		}
	}
	return nil
}

// ContributionPointsContractDocument returns the decoded contract. It panics if
// the embedded contract is invalid; api tests guard that.
func ContributionPointsContractDocument() ContributionPointsContract {
	contributionContractOnce.Do(loadContributionContract)
	if contributionContractErr != nil {
		panic(contributionContractErr)
	}
	return contributionContract
}

// ContributionPointSpecs returns every point (enabled and planned) sorted by key.
func ContributionPointSpecs() []ContributionPointSpec {
	contract := ContributionPointsContractDocument()
	specs := make([]ContributionPointSpec, 0, len(contract.Points))
	for _, spec := range contract.Points {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Key < specs[j].Key })
	return specs
}

// ContributionPointSpecFor returns one point's spec.
func ContributionPointSpecFor(key string) (ContributionPointSpec, bool) {
	spec, ok := ContributionPointsContractDocument().Points[key]
	return spec, ok
}

// ContributionManifestMaxBytes is the plugin.json size cap shared by host, CLI and cloud.
func ContributionManifestMaxBytes() int64 {
	return ContributionPointsContractDocument().ManifestMaxBytes
}

// ContributionPointsJSON returns the raw embedded contract bytes.
func ContributionPointsJSON() []byte {
	return append([]byte(nil), contributionPointsJSON...)
}
