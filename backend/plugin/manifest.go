package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

const manifestFilename = "plugin.json"

// maxPluginDescriptionBytes bounds the manifest description in UTF-8 bytes (about
// 660 Chinese characters); the marketplace cloud and its forms use the same limit.
const maxPluginDescriptionBytes = 2000

var (
	pluginIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)
	pluginVersionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	contributionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	pluginViewIcons       = api.PluginIcons
)

// Manifest is the normalized descriptor used by the single local-process
// plugin manager. API v1 and v2 manifests share the same runtime protocol.
type Manifest struct {
	Category     api.Category           `json:"category"`
	Tags         []string               `json:"tags,omitempty"`
	Homepage     string                 `json:"homepage,omitempty"`
	Cover        string                 `json:"cover,omitempty"`
	Icon         string                 `json:"icon,omitempty"`
	APIVersion   int                    `json:"apiVersion"`
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Description  string                 `json:"description,omitempty"`
	Author       string                 `json:"author,omitempty"`
	Platforms    []string               `json:"platforms,omitempty"`
	Capabilities []api.Capability       `json:"capabilities,omitempty"`
	Runtime      ProcessRuntimeManifest `json:"runtime"`
	Contributes  ManifestContributions  `json:"contributes,omitempty"`

	// Validated contribution points by key (contributions.go): entries,
	// skipped-entry issues and a fingerprint. Immutable once validated.
	contributions map[string]*resolvedContribution
}

const (
	pluginActivationOnDemand = "on-demand"
	pluginActivationStartup  = "startup"
)

type ProcessRuntimeManifest struct {
	Activation  string            `json:"activation,omitempty"`
	Type        string            `json:"type"`
	Entry       string            `json:"entry,omitempty"`
	Entrypoints map[string]string `json:"entrypoints,omitempty"`
	Args        []string          `json:"args,omitempty"`
	// Transport selects the stdio encoding: "jsonl" (default) or "framed",
	// which also carries binary frames (process_protocol.go).
	Transport string `json:"transport,omitempty"`
}

type ManifestContributions struct {
	Commands []ManifestCommand  `json:"commands,omitempty"`
	Views    []ManifestView     `json:"views,omitempty"`
	Actions  []ManifestUIAction `json:"actions,omitempty"`
	// Points holds every other contributes.<point> value (keymaps, ...). The
	// custom JSON codec in contributions.go rejects points the host has not
	// registered and enabled; resolveManifestContributionPoints validates them.
	Points map[string]json.RawMessage `json:"-"`
}

type ManifestCommand struct {
	ID             string                 `json:"id"`
	Title          string                 `json:"title"`
	TitleLocalized map[string]string      `json:"titleLocalized,omitempty"`
	Description    string                 `json:"description,omitempty"`
	Keywords       []string               `json:"keywords,omitempty"`
	Action         string                 `json:"action,omitempty"`
	Payload        string                 `json:"payload,omitempty"`
	Risk           string                 `json:"risk,omitempty"`
	Target         *ManifestCommandTarget `json:"target,omitempty"`
	// API v2 only: host-rendered parameter form and visibility expression.
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	When        string         `json:"when,omitempty"`
}

type ManifestCommandTarget struct {
	Type   string `json:"type"`
	ViewID string `json:"viewId"`
}

type ManifestView struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Entry          string            `json:"entry"`
	Icon           string            `json:"icon,omitempty"`
	FileDrop       *ManifestFileDrop `json:"fileDrop,omitempty"`
	// Floating shows the view in a host floating window instead of a tab (view_floating.go).
	Floating *ManifestFloating `json:"floating,omitempty"`
	// Prefetch names one read-only UI action the host invokes for the page's
	// first screen in parallel with the frame load (view_prefetch.go).
	Prefetch *ManifestViewPrefetch `json:"prefetch,omitempty"`
}

type ManifestUIAction struct {
	Permissions     []api.Capability `json:"permissions"`
	Description     string           `json:"description,omitempty"`
	InputSchema     map[string]any   `json:"inputSchema,omitempty"`
	Examples        []map[string]any `json:"examples,omitempty"`
	AICallable      bool             `json:"aiCallable,omitempty"`
	ID              string           `json:"id"`
	Risk            string           `json:"risk,omitempty"`
	UICallable      bool             `json:"uiCallable,omitempty"`
	MaxPayloadBytes int              `json:"maxPayloadBytes,omitempty"`
	ResultHandling  string           `json:"resultHandling,omitempty"`
}

type manifestV2 Manifest

// Only plugin.json is accepted. Local callers supply Lstat to reject symlinks.
func selectManifestFilename(stat func(string) (fs.FileInfo, error)) (string, error) {
	name := manifestFilename
	info, err := stat(name)

	if err != nil {
		return "", fmt.Errorf("inspect plugin manifest %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("plugin manifest %s cannot be a symlink", name)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin manifest %s must be a regular file", name)
	}
	return name, nil
}

func readManifest(root string) (Manifest, error) {
	name, err := selectManifestFilename(func(name string) (fs.FileInfo, error) {
		return os.Lstat(filepath.Join(root, name))
	})
	if err != nil {
		return Manifest{}, err
	}
	file, err := os.Open(filepath.Join(root, name))
	if err != nil {
		return Manifest{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	defer file.Close()
	maxBytes := api.ContributionManifestMaxBytes()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return Manifest{}, fmt.Errorf("plugin manifest exceeds %d KiB", maxBytes/1024)
	}
	var version struct {
		APIVersion int `json:"apiVersion"`
	}
	if err := decodeStrictJSON(data, &version, false); err != nil {
		return Manifest{}, fmt.Errorf("parse plugin manifest: %w", err)
	}

	var manifest Manifest
	switch version.APIVersion {
	case api.ManifestAPIVersionV2:
		var wire manifestV2
		if err := decodeStrictJSON(data, &wire, true); err != nil {
			return Manifest{}, fmt.Errorf("parse plugin manifest: %w", err)
		}
		manifest = Manifest(wire)
	default:
		return Manifest{}, fmt.Errorf("unsupported plugin apiVersion %d; expected 2", version.APIVersion)
	}
	if err := validateManifest(newPluginPathResolver(root), &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func decodeStrictJSON(data []byte, destination any, disallowUnknown bool) error {
	return decodeStrictJSONValue(data, destination, disallowUnknown, "manifest")
}

// decodeStrictJSONValue is decodeStrictJSON for a value named label in the
// error about trailing data.
func decodeStrictJSONValue(data []byte, destination any, disallowUnknown bool, label string) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%s must contain exactly one JSON value", label)
		}
		return err
	}
	return nil
}

func validateManifest(paths *pluginPathResolver, manifest *Manifest) error {
	if err := validateCatalogMetadata(manifest); err != nil {
		return err
	}
	manifest.ID = strings.TrimSpace(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.Author = strings.TrimSpace(manifest.Author)
	manifest.Icon = strings.TrimSpace(manifest.Icon)
	if manifest.Icon != "" {
		if _, allowed := pluginViewIcons[manifest.Icon]; !allowed {
			return errors.New("plugin icon is not supported")
		}
	}
	manifest.Runtime.Type = strings.TrimSpace(strings.ToLower(manifest.Runtime.Type))

	if manifest.APIVersion != api.ManifestAPIVersionV2 {
		return fmt.Errorf("unsupported plugin apiVersion %d; expected 2", manifest.APIVersion)
	}
	if !pluginIDPattern.MatchString(manifest.ID) {
		return errors.New("plugin id must use lowercase letters, digits, dots, underscores, or hyphens")
	}
	if manifest.Name == "" || len(manifest.Name) > 120 {
		return errors.New("plugin name must contain 1-120 characters")
	}
	if !pluginVersionPattern.MatchString(manifest.Version) {
		return errors.New("plugin version must be semantic version text such as 1.0.0")
	}
	if len(manifest.Description) > maxPluginDescriptionBytes || len(manifest.Author) > 120 {
		return errors.New("plugin description or author is too long")
	}
	if err := validatePlatforms(manifest); err != nil {
		return err
	}
	if err := validateCapabilities(manifest); err != nil {
		return err
	}
	if err := validateRuntime(paths, manifest); err != nil {
		return err
	}
	if err := validateContributions(paths, manifest); err != nil {
		return err
	}
	return nil
}

func validateCatalogMetadata(manifest *Manifest) error {
	if !api.IsCategory(manifest.Category) {
		return errors.New("plugin category is required and must be a known category")
	}
	if len(manifest.Tags) > 8 {
		return errors.New("plugin tags must contain at most 8 entries")
	}
	tags := make([]string, 0, len(manifest.Tags))
	seen := map[string]bool{}
	for _, tag := range manifest.Tags {
		tag = strings.TrimSpace(tag)
		if utf8.RuneCountInString(tag) > 24 {
			return errors.New("plugin tag exceeds 24 characters")
		}
		if tag != "" && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	manifest.Tags = tags
	manifest.Homepage = strings.TrimSpace(manifest.Homepage)
	if manifest.Homepage != "" {
		address, err := url.Parse(manifest.Homepage)
		if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" || address.User != nil {
			return errors.New("plugin homepage must be an absolute HTTP/HTTPS URL without credentials")
		}
	}
	return nil
}

func validatePlatforms(manifest *Manifest) error {
	if len(manifest.Platforms) == 0 {
		return nil
	}
	supported := false
	seen := map[string]struct{}{}
	for index, platform := range manifest.Platforms {
		platform = strings.TrimSpace(strings.ToLower(platform))
		if platform != "darwin" && platform != "windows" && platform != "linux" {
			return fmt.Errorf("unsupported platform %q", platform)
		}
		if _, duplicate := seen[platform]; duplicate {
			return fmt.Errorf("duplicate platform %q", platform)
		}
		seen[platform] = struct{}{}
		manifest.Platforms[index] = platform
		if platform == runtime.GOOS {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("plugin does not support %s", runtime.GOOS)
	}
	return nil
}

func validateCapabilities(manifest *Manifest) error {
	known := map[api.Capability]struct{}{}
	for _, capability := range append(api.CapabilitiesV1(), api.CapabilitiesV2Only()...) {
		known[capability] = struct{}{}
	}
	capabilities := make([]api.Capability, 0, len(manifest.Capabilities)+2)
	seen := map[api.Capability]struct{}{}
	for _, capability := range manifest.Capabilities {
		capability = api.Capability(strings.TrimSpace(string(capability)))
		if _, ok := known[capability]; !ok {
			return fmt.Errorf("unknown plugin capability %q", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	if len(manifest.Contributes.Commands) > 0 {
		if _, ok := seen[api.CapabilityCommands]; !ok {
			seen[api.CapabilityCommands] = struct{}{}
			capabilities = append(capabilities, api.CapabilityCommands)
		}
	}
	if len(manifest.Contributes.Views) > 0 || len(manifest.Contributes.Actions) > 0 {
		if _, ok := seen[api.CapabilityUI]; !ok {
			return errors.New("plugin UI contributions require the ui capability")
		}
	}
	// ui.wasm only relaxes the CSP of the plugin's own pages.
	if _, ok := seen[api.CapabilityUIWasm]; ok {
		if _, ui := seen[api.CapabilityUI]; !ui {
			return errors.New("plugin capability ui.wasm requires the ui capability")
		}
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	manifest.Capabilities = capabilities
	return nil
}

func validateRuntime(paths *pluginPathResolver, manifest *Manifest) error {
	if manifest.Runtime.Type == pluginRuntimeDeclarative {
		return validateDeclarativeRuntime(manifest)
	}
	switch manifest.Runtime.Activation {
	case "", pluginActivationOnDemand:
		manifest.Runtime.Activation = pluginActivationOnDemand
	case pluginActivationStartup:
	default:
		return errors.New("plugin runtime.activation must be on-demand or startup")
	}
	if manifest.Runtime.Type != pluginRuntimeProcess {
		return errors.New("plugin runtime.type must be process or declarative")
	}
	switch manifest.Runtime.Transport {
	case "", pluginTransportJSONL, pluginTransportFramed:
	default:
		return errors.New("plugin runtime.transport must be jsonl or framed")
	}
	entry, err := manifest.Runtime.selectedEntry()
	if err != nil {
		return err
	}
	// The resolver returns the Lstat info of the real (link-free) target, which
	// is what os.Stat of the entry reports.
	_, info, err := paths.resolve(entry)
	if err != nil {
		return fmt.Errorf("invalid plugin runtime entry: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("plugin runtime entry must be a regular file")
	}
	if len(manifest.Runtime.Args) > 64 {
		return errors.New("plugin runtime args exceed 64 entries")
	}
	for _, argument := range manifest.Runtime.Args {
		if len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
			return errors.New("plugin runtime argument is invalid")
		}
	}
	return nil
}

// validateDeclarativeRuntime accepts data-only packages: nothing to execute,
// no capabilities, no commands/views/actions — only contributed data.
func validateDeclarativeRuntime(manifest *Manifest) error {
	runtime := manifest.Runtime
	if runtime.Activation != "" || runtime.Entry != "" || len(runtime.Entrypoints) > 0 || len(runtime.Args) > 0 || runtime.Transport != "" {
		return errors.New("declarative plugins must not declare runtime activation, entry, entrypoints, args or transport")
	}
	if len(manifest.Capabilities) > 0 {
		return errors.New("declarative plugins must not request capabilities")
	}
	if len(manifest.Contributes.Commands) > 0 || len(manifest.Contributes.Views) > 0 || len(manifest.Contributes.Actions) > 0 {
		return errors.New("declarative plugins may only contribute data such as keymaps")
	}
	if !declarativeDataContributed(manifest) {
		return errors.New("declarative plugins must contribute at least one " + declarativeContributionNouns())
	}
	return nil
}

func validateContributions(paths *pluginPathResolver, manifest *Manifest) error {
	if len(manifest.Contributes.Commands) > 100 || len(manifest.Contributes.Actions) > 100 || len(manifest.Contributes.Views) > 20 {
		return errors.New("plugin contribution count exceeds the supported limit")
	}
	seenViews := map[string]struct{}{}
	floatingViews := map[string]struct{}{}
	for index := range manifest.Contributes.Views {
		view := &manifest.Contributes.Views[index]
		view.ID = strings.TrimSpace(view.ID)
		view.Title = strings.TrimSpace(view.Title)
		view.Entry = filepath.ToSlash(strings.TrimSpace(view.Entry))
		view.Icon = strings.TrimSpace(view.Icon)
		if !contributionIDPattern.MatchString(view.ID) || view.Title == "" || len(view.Title) > 200 {
			return fmt.Errorf("plugin view %q is invalid", view.ID)
		}
		if view.Icon != "" {
			if _, allowed := pluginViewIcons[view.Icon]; !allowed {
				return fmt.Errorf("plugin view %q icon is not supported", view.ID)
			}
		}
		if _, duplicate := seenViews[view.ID]; duplicate {
			return fmt.Errorf("duplicate plugin view %q", view.ID)
		}
		seenViews[view.ID] = struct{}{}
		if !strings.HasPrefix(view.Entry, "ui/") || !strings.HasSuffix(strings.ToLower(view.Entry), ".html") {
			return fmt.Errorf("plugin view %q entry must be an HTML file under ui/", view.ID)
		}
		entryRelative := strings.TrimPrefix(view.Entry, "ui/")
		_, info, err := paths.resolveUnder("ui", filepath.FromSlash(entryRelative))
		if err != nil {
			return fmt.Errorf("invalid plugin view %q entry: %w", view.ID, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin view %q entry must be a regular file", view.ID)
		}
		if err := validateLocalizedTitles(view.TitleLocalized); err != nil {
			return fmt.Errorf("plugin view %q: %w", view.ID, err)
		}
		if err := normalizeFileDrop(view.FileDrop); err != nil {
			return fmt.Errorf("plugin view %q: %w", view.ID, err)
		}
		if err := normalizeFloating(view.Floating); err != nil {
			return fmt.Errorf("plugin view %q: %w", view.ID, err)
		}
		if view.Floating != nil {
			if view.FileDrop != nil {
				return fmt.Errorf("plugin view %q: floating views cannot accept file drops", view.ID)
			}
			floatingViews[view.ID] = struct{}{}
		}
	}
	if len(floatingViews) > floatingViewsPerPlugin {
		return fmt.Errorf("a plugin may declare at most %d floating views", floatingViewsPerPlugin)
	}

	declaresBackup := slices.Contains(manifest.Capabilities, api.CapabilityBackup)
	seenActions := map[string]struct{}{}
	for index := range manifest.Contributes.Actions {
		action := &manifest.Contributes.Actions[index]
		action.ID = strings.TrimSpace(action.ID)
		// AI gate: the model must never run code of a plugin that can export
		// and restore all MinCode data (host.backup.*). Mirrored by the plugin
		// CLI and the cloud marketplace.
		if declaresBackup && action.AICallable {
			return fmt.Errorf("plugin UI action %q: aiCallable actions are not allowed in a plugin that declares the backup capability", action.ID)
		}
		risk, err := normalizeRisk(action.Risk)
		if err != nil {
			return fmt.Errorf("plugin UI action %q: %w", action.ID, err)
		}
		action.Risk = risk
		if _, err := resolveAIActionPermissions(*action, manifest.Capabilities); err != nil {
			return fmt.Errorf("invalid action permissions: %w", err)
		}
		if !contributionIDPattern.MatchString(action.ID) {
			return fmt.Errorf("plugin UI action %q is invalid", action.ID)
		}
		if _, duplicate := seenActions[action.ID]; duplicate {
			return fmt.Errorf("duplicate plugin UI action %q", action.ID)
		}
		seenActions[action.ID] = struct{}{}
		if action.MaxPayloadBytes == 0 {
			action.MaxPayloadBytes = 16 * 1024
		}
		if action.MaxPayloadBytes < 1 || action.MaxPayloadBytes > 256*1024 {
			return fmt.Errorf("plugin UI action %q has an invalid maxPayloadBytes", action.ID)
		}
		action.ResultHandling = strings.TrimSpace(strings.ToLower(action.ResultHandling))
		if action.ResultHandling != "" && action.ResultHandling != "save-text-file" && action.ResultHandling != "open-url" {
			return fmt.Errorf("plugin UI action %q has unsupported resultHandling", action.ID)
		}
		if action.ResultHandling != "" && (!action.UICallable || action.AICallable) {
			return fmt.Errorf("plugin UI action %q resultHandling requires a UI-only action", action.ID)
		}
		if action.ResultHandling == "save-text-file" && action.Risk == "read" {
			return fmt.Errorf("plugin UI action %q save-text-file resultHandling requires a write or dangerous action", action.ID)
		}
	}
	// View prefetch references validated actions, so it runs after them.
	for index := range manifest.Contributes.Views {
		view := &manifest.Contributes.Views[index]
		if err := normalizeViewPrefetch(manifest, view); err != nil {
			return fmt.Errorf("plugin view %q: %w", view.ID, err)
		}
	}

	seenCommands := map[string]struct{}{}
	for index := range manifest.Contributes.Commands {
		command := &manifest.Contributes.Commands[index]
		command.ID = strings.TrimSpace(command.ID)
		command.Title = strings.TrimSpace(command.Title)
		command.Action = strings.TrimSpace(command.Action)
		risk, err := normalizeRisk(command.Risk)
		if err != nil {
			return fmt.Errorf("plugin command %q: %w", command.ID, err)
		}
		command.Risk = risk
		if !contributionIDPattern.MatchString(command.ID) || command.Title == "" || len(command.Title) > 200 ||
			len(command.Description) > 500 || len(command.Payload) > 4_096 {
			return fmt.Errorf("plugin command %q is invalid", command.ID)
		}
		if _, duplicate := seenCommands[command.ID]; duplicate {
			return fmt.Errorf("duplicate plugin command %q", command.ID)
		}
		seenCommands[command.ID] = struct{}{}
		if err := validateLocalizedTitles(command.TitleLocalized); err != nil {
			return fmt.Errorf("plugin command %q: %w", command.ID, err)
		}
		hasAction := command.Action != ""
		hasTarget := command.Target != nil
		if hasAction == hasTarget {
			return fmt.Errorf("plugin command %q must define exactly one of action or target", command.ID)
		}
		command.When = strings.TrimSpace(command.When)
		if command.When != "" {
			if !hasAction {
				return fmt.Errorf("plugin command %q when requires an action command", command.ID)
			}
			if err := validateWhenExpression(command.When); err != nil {
				return fmt.Errorf("plugin command %q: %w", command.ID, err)
			}
		}
		if command.InputSchema != nil {
			if !hasAction {
				return fmt.Errorf("plugin command %q inputSchema requires an action command", command.ID)
			}
			if err := validateCommandInputSchema(command.InputSchema); err != nil {
				return fmt.Errorf("plugin command %q: %w", command.ID, err)
			}
		}
		if hasAction && !contributionIDPattern.MatchString(command.Action) {
			return fmt.Errorf("plugin command %q action is invalid", command.ID)
		}
		if hasTarget {
			command.Target.Type = strings.TrimSpace(strings.ToLower(command.Target.Type))
			command.Target.ViewID = strings.TrimSpace(command.Target.ViewID)
			if command.Target.Type != "view" {
				return fmt.Errorf("plugin command %q has an invalid target", command.ID)
			}
			if _, exists := seenViews[command.Target.ViewID]; !exists {
				return fmt.Errorf("plugin command %q targets unknown view %q", command.ID, command.Target.ViewID)
			}
			if _, floating := floatingViews[command.Target.ViewID]; floating {
				return fmt.Errorf("plugin command %q targets floating view %q", command.ID, command.Target.ViewID)
			}
		}
	}
	// Contribution points run last so they can reference validated commands.
	return resolveManifestContributionPointsWith(paths, manifest)
}

func validateLocalizedTitles(titles map[string]string) error {
	if len(titles) > 16 {
		return errors.New("localized titles exceed 16 locales")
	}
	for locale, title := range titles {
		if strings.TrimSpace(locale) == "" || len(locale) > 32 || strings.TrimSpace(title) == "" || len(title) > 200 {
			return errors.New("localized title is invalid")
		}
	}
	return nil
}

func normalizeRisk(risk string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "", "read":
		return "read", nil
	case "write":
		return "write", nil
	case "dangerous":
		return "dangerous", nil
	default:
		return "", errors.New("risk must be read, write, or dangerous")
	}
}

func (runtimeManifest ProcessRuntimeManifest) selectedEntry() (string, error) {
	entry := strings.TrimSpace(runtimeManifest.Entry)
	if platformEntry := strings.TrimSpace(runtimeManifest.Entrypoints[runtime.GOOS]); platformEntry != "" {
		entry = platformEntry
	}
	if entry == "" {
		return "", errors.New("plugin runtime entry is required for this platform")
	}
	return entry, nil
}

// resolvePluginPath validates one package-relative path against root. Callers
// that resolve many paths in the same package (manifest validation, asset
// reads) share a pluginPathResolver instead, so the root is resolved once and
// verified directories are not checked again.
//
// Unlike pluginPathResolver.resolve it returns the platform's canonical
// spelling (on Windows the on-disk case and long names, as the historical
// EvalSymlinks-based implementation did), because callers feed the result into
// persisted values such as process-launch fingerprints (argv[0]).
func resolvePluginPath(root, relative string) (string, error) {
	resolver := newPluginPathResolver(root)
	resolved, _, err := resolver.resolve(relative)
	if err != nil {
		return "", err
	}
	return resolver.canonical(resolved)
}

// resolvePluginSubdirPath is resolvePluginPath for a path that must stay inside
// the package subdirectory dir (see pluginPathResolver.resolveUnder).
func resolvePluginSubdirPath(root, dir, relative string) (string, error) {
	resolver := newPluginPathResolver(root)
	resolved, _, err := resolver.resolveUnder(dir, relative)
	if err != nil {
		return "", err
	}
	return resolver.canonical(resolved)
}

// errPluginPathLink is returned when a package path crosses a symlink, a
// Windows junction or another reparse point whose fully resolved target lies
// outside the resolved package root.
var errPluginPathLink = errors.New("path escapes the plugin directory through a symlink")

// pluginPathLstat is os.Lstat; tests replace it to simulate reparse points
// that are hard to create on a test host (OneDrive placeholders, Dedup files).
var pluginPathLstat = os.Lstat

// pluginPathResolver validates package-relative paths against one plugin
// directory with the same acceptance rules as the historical
// EvalSymlinks-based check, but without resolving every path:
//
//   - the directory itself is resolved with filepath.EvalSymlinks once and
//     reused (a linked development package root stays supported);
//   - the relative path must stay lexically inside the directory;
//   - fast path: every component below the resolved root is checked with
//     os.Lstat; when none is a symlink, junction or other reparse point
//     (pluginPathIsLink) the path is accepted as is. Intermediate components
//     must be directories, and directories verified that way are cached for
//     the resolver's lifetime, so sibling files cost a single Lstat;
//   - fallback: as soon as a component is any kind of link or reparse point,
//     the whole target is resolved with filepath.EvalSymlinks (the historical
//     check) and accepted only when the real path stays strictly inside the
//     resolved root; anything resolving outside is rejected. Components at or
//     below a link are never cached.
//
// On the fast path the returned path is the resolved root joined with the
// cleaned relative path (on case-insensitive file systems it keeps the
// caller's spelling of the relative part); on the fallback it is the fully
// resolved real path. A resolver is scoped to one bounded operation (one manifest
// validation, one asset batch): like the previous EvalSymlinks-then-open
// sequence it cannot notice a directory being swapped for a link after the
// check, and callers keep their own Lstat/regular-file checks on the leaf.
// It is safe for concurrent use.
type pluginPathResolver struct {
	root string

	mu           sync.Mutex
	rootAbs      string
	rootAbsErr   error
	rootResolved bool
	resolvedRoot string
	resolveErr   error
	// realRoot is pluginRealPath(resolvedRoot), computed on the first link
	// fallback (it differs from resolvedRoot only when the root path crosses a
	// Windows junction, which EvalSymlinks leaves in place).
	realRoot    string
	realRootErr error
	dirs        map[string]struct{}
}

func newPluginPathResolver(root string) *pluginPathResolver {
	return &pluginPathResolver{root: root}
}

// resolveUnder resolves relative inside the package subdirectory dir (for
// example "ui"). relative must stay lexically inside dir, and the path is then
// checked from the package root, so dir itself is subject to the same link
// rules as every other component: a symlink or Windows junction named dir is
// accepted only when its real target lies inside the package. (Resolving with
// dir as the root would trust dir blindly, and EvalSymlinks does not follow
// junctions on Windows.)
func (resolver *pluginPathResolver) resolveUnder(dir, relative string) (string, fs.FileInfo, error) {
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || strings.ContainsRune(relative, '\x00') {
		return "", nil, errors.New("path must be relative to the plugin directory")
	}
	cleaned := filepath.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("path escapes the plugin directory")
	}
	return resolver.resolve(filepath.Join(dir, cleaned))
}

// resolve returns the checked path of relative and the leaf's Lstat result.
func (resolver *pluginPathResolver) resolve(relative string) (string, fs.FileInfo, error) {
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || strings.ContainsRune(relative, '\x00') {
		return "", nil, errors.New("path must be relative to the plugin directory")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.rootAbs == "" && resolver.rootAbsErr == nil {
		resolver.rootAbs, resolver.rootAbsErr = filepath.Abs(resolver.root)
	}
	if resolver.rootAbsErr != nil {
		return "", nil, resolver.rootAbsErr
	}
	rootAbs := resolver.rootAbs
	separator := string(filepath.Separator)
	lexical := filepath.Join(rootAbs, filepath.Clean(relative))
	if lexical == rootAbs || !strings.HasPrefix(lexical, rootAbs+separator) {
		return "", nil, errors.New("path escapes the plugin directory")
	}
	if !resolver.rootResolved {
		resolver.rootResolved = true
		resolver.resolvedRoot, resolver.resolveErr = filepath.EvalSymlinks(rootAbs)
		if resolver.resolveErr != nil {
			resolver.resolveErr = fmt.Errorf("resolve plugin directory: %w", resolver.resolveErr)
		}
	}
	if resolver.resolveErr != nil {
		return "", nil, resolver.resolveErr
	}
	base := resolver.resolvedRoot
	if !strings.HasSuffix(base, separator) {
		base += separator
	}
	// After Clean and the prefix check rel has no "." or ".." elements, so each
	// separator-delimited prefix names one real entry below the resolved root.
	rel := lexical[len(rootAbs)+1:]
	for index := 0; index <= len(rel); index++ {
		if index < len(rel) && rel[index] != filepath.Separator {
			continue
		}
		component := rel[:index]
		leaf := index == len(rel)
		if !leaf {
			if _, verified := resolver.dirs[component]; verified {
				continue
			}
		}
		info, err := pluginPathLstat(base + component)
		if err != nil {
			return "", nil, err
		}
		if pluginPathIsLink(info) {
			return resolver.resolveThroughLink(base + rel)
		}
		if leaf {
			return base + rel, info, nil
		}
		if !info.IsDir() {
			return "", nil, fmt.Errorf("path component %q is not a directory: %w", filepath.ToSlash(component), fs.ErrNotExist)
		}
		if resolver.dirs == nil {
			resolver.dirs = map[string]struct{}{}
		}
		resolver.dirs[component] = struct{}{}
	}
	return "", nil, errors.New("path escapes the plugin directory")
}

// resolveThroughLink is the fallback for a path with a symlink, junction or
// other reparse point in any component: the historical full-resolution check.
// The real path (pluginRealPath: EvalSymlinks on Unix; the opened file's final
// path on Windows, where EvalSymlinks does not follow junctions) must lie
// strictly inside the real package root. Callers hold resolver.mu; nothing on
// or below the link is cached.
func (resolver *pluginPathResolver) resolveThroughLink(path string) (string, fs.FileInfo, error) {
	if resolver.realRoot == "" && resolver.realRootErr == nil {
		resolver.realRoot, resolver.realRootErr = pluginRealPath(resolver.resolvedRoot)
	}
	if resolver.realRootErr != nil {
		return "", nil, fmt.Errorf("resolve plugin directory: %w", resolver.realRootErr)
	}
	resolvedTarget, err := pluginRealPath(path)
	if err != nil {
		return "", nil, err
	}
	if !pluginPathStrictlyInside(resolvedTarget, resolver.realRoot) {
		return "", nil, errPluginPathLink
	}
	info, err := os.Lstat(resolvedTarget)
	if err != nil {
		return "", nil, err
	}
	return resolvedTarget, info, nil
}

// pluginPathStrictlyInside reports whether path lies below root (not equal).
func pluginPathStrictlyInside(path, root string) bool {
	if root == "" {
		return false
	}
	root = strings.TrimSuffix(root, string(filepath.Separator))
	return path != root && strings.HasPrefix(path, root+string(filepath.Separator))
}

// canonical returns the platform-canonical spelling of a path resolve just
// accepted (pluginPathCanonicalNeeded: Windows only; elsewhere the checked
// path already is canonical because no component is a link). The canonical
// form must still lie below the resolved root, so a component swapped for a
// link after the Lstat walk cannot redirect the result.
func (resolver *pluginPathResolver) canonical(resolved string) (string, error) {
	if !pluginPathCanonicalNeeded {
		return resolved, nil
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// EvalSymlinks cannot see through a Windows junction in the root path;
		// the opened file's final path can.
		if resolver.realRoot == "" && resolver.realRootErr == nil {
			resolver.realRoot, resolver.realRootErr = pluginRealPath(resolver.resolvedRoot)
		}
		if canonical, err = pluginRealPath(resolved); err != nil {
			return "", err
		}
	}
	root, realRoot := resolver.resolvedRoot, resolver.realRoot
	// A fallback result is already a real path and is compared with the real
	// root; a fast-path result is compared with the EvalSymlinks root.
	if !pluginPathStrictlyInside(canonical, root) && !pluginPathStrictlyInside(canonical, realRoot) {
		return "", errPluginPathLink
	}
	return canonical, nil
}

func capabilityStrings(capabilities []api.Capability) []string {
	out := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, string(capability))
	}
	sort.Strings(out)
	return out
}

func resolvePluginViewIcon(pageIcon, pluginIcon string) string {
	for _, icon := range []string{pageIcon, pluginIcon} {
		icon = strings.TrimSpace(icon)
		if _, allowed := pluginViewIcons[icon]; allowed {
			return icon
		}
	}
	return "box"
}
