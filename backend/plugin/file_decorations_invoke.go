package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_file_decorations calls a file decoration provider for one
// directory of the open project tree. The WebView names the provider, the
// project root, the directory ("" is the root) and the names of the
// directory's visible children; the host checks the directory with the
// languageFeatures location rules (clean relative path, no VCS directory,
// still inside the canonical project root once symlinks are resolved),
// removes duplicate names and leaves out sensitive ones (counted in skipped,
// never sent), and sends
//
//	{provider, project: {name, root}, directory, names, locale}
//
// The names are never stat'ed: they only label nodes the WebView already
// shows. The answer {"decorations": {"<name>": {badge?, color?, tooltip?}}}
// is accepted only for names that were sent; any other key, or an item with
// an invalid badge, colour or tooltip, is dropped and counted in dropped.
// Bounds: payload ≤ min(action maxPayloadBytes, payloadMaxBytes), timeoutMs,
// concurrentPerPlugin, result ≤ resultMaxBytes.
var (
	fileDecorationsNamesMax        = int(fileDecorationSpec.Limit("namesMax"))
	fileDecorationsNameMaxBytes    = int(fileDecorationSpec.Limit("nameMaxBytes"))
	fileDecorationsDirMaxBytes     = int(fileDecorationSpec.Limit("directoryMaxBytes"))
	fileDecorationsPayloadMaxBytes = int(fileDecorationSpec.Limit("payloadMaxBytes"))
	fileDecorationsResultMaxBytes  = int(fileDecorationSpec.Limit("resultMaxBytes"))
	fileDecorationsBadgeMaxRunes   = int(fileDecorationSpec.Limit("badgeMaxRunes"))
	fileDecorationsTooltipMaxRunes = int(fileDecorationSpec.Limit("tooltipMaxRunes"))
	fileDecorationsTimeout         = time.Duration(fileDecorationSpec.Limit("timeoutMs")) * time.Millisecond
	fileDecorationsConcurrency     = int(fileDecorationSpec.Limit("concurrentPerPlugin"))
	fileDecorationsColors          = fileDecorationSpec.List("colors")
)

type fileDecorationsRequest struct {
	PluginID    string   `json:"pluginId"`
	ProviderID  string   `json:"providerId"`
	RequestID   string   `json:"requestId"`
	ProjectRoot string   `json:"projectRoot"`
	Directory   string   `json:"directory"`
	Names       []string `json:"names"`
	Locale      string   `json:"locale,omitempty"`
}

type fileDecorationsPayload struct {
	Provider  string              `json:"provider"`
	Project   contributionProject `json:"project"`
	Directory string              `json:"directory"`
	Names     []string            `json:"names"`
	Locale    string              `json:"locale"`
}

// FileDecoration is one validated decoration: a badge of at most
// badgeMaxRunes letters, digits, punctuation or symbols and/or a colour token
// from lists.colors, with an optional single-line tooltip.
type FileDecoration struct {
	Badge   string `json:"badge,omitempty"`
	Color   string `json:"color,omitempty"`
	Tooltip string `json:"tooltip,omitempty"`
}

type fileDecorationsResponse struct {
	Decorations map[string]FileDecoration `json:"decorations"`
	Dropped     int                       `json:"dropped"`
	Skipped     int                       `json:"skipped"`
}

func (manager *pluginManager) fileDecorations(ctx context.Context, request fileDecorationsRequest) (fileDecorationsResponse, error) {
	if err := normalizeFileDecorationsRequest(&request); err != nil {
		return fileDecorationsResponse{}, err
	}
	// The provider is checked before the filesystem is touched, so an
	// unknown plugin learns nothing about paths.
	provider, actionLimit, err := manager.lookupFileDecorationProvider(request.PluginID, request.ProviderID)
	if err != nil {
		return fileDecorationsResponse{}, err
	}
	project, err := fileDecorationsDirectory(request.ProjectRoot, request.Directory)
	if err != nil {
		return fileDecorationsResponse{}, err
	}
	names, skipped := fileDecorationsSendableNames(request.Directory, request.Names)
	response := fileDecorationsResponse{Decorations: map[string]FileDecoration{}, Skipped: skipped}
	if len(names) == 0 {
		return response, nil
	}
	limit := fileDecorationsPayloadMaxBytes
	if actionLimit > 0 && actionLimit < limit {
		limit = actionLimit
	}
	payload, err := encodeContributionJSON(fileDecorationsPayload{
		Provider: provider.ID, Project: *project, Directory: request.Directory, Names: names, Locale: request.Locale,
	})
	if err != nil {
		return fileDecorationsResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	if len(payload) > limit {
		return fileDecorationsResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, file decoration provider %q allows %d", len(payload), provider.ID, limit)
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: fileDecorationPointKey, EntryID: provider.ID, ActionID: provider.Action,
		RequestID: request.RequestID, Payload: payload, Timeout: fileDecorationsTimeout,
		MaxResultBytes: fileDecorationsResultMaxBytes, Concurrency: fileDecorationsConcurrency,
		Validate: fileDecorationProviderUnchanged(provider),
	})
	if err != nil {
		return fileDecorationsResponse{}, err
	}
	requested := make(map[string]bool, len(names))
	for _, name := range names {
		requested[name] = true
	}
	parsed, err := parseFileDecorationsResult(raw, requested)
	if err != nil {
		return fileDecorationsResponse{}, newContributionError(contributionErrInvalidResult, "file decoration provider %q: %v", provider.ID, err)
	}
	parsed.Skipped = skipped
	return parsed, nil
}

// fileDecorationsDirectory checks the (already lexically valid) directory
// against the project: the root passes contributionProjectFor (no
// filesystem root, home or application data folder), the directory is not
// a VCS or credential path, and, with symlinks resolved, it stays inside the
// canonical root and does not land on a denied path (checkLanguageLocationTarget).
// Returns the payload's project block with the canonical root.
func fileDecorationsDirectory(projectRoot, directory string) (*contributionProject, error) {
	project, err := contributionProjectFor(projectRoot, true)
	if err != nil {
		return nil, err
	}
	if directory == "" {
		return project, nil
	}
	if workspacePathDenied(directory, false) {
		return nil, newContributionError(contributionErrForbidden, "directory is not accessible to plugins")
	}
	if err := checkLanguageLocationTarget(project.Root, directory); err != nil {
		return nil, newContributionError(contributionErrForbidden, "directory %v", err)
	}
	return project, nil
}

// fileDecorationsSendableNames removes duplicate names (first kept) and
// leaves out names whose path in the directory host.workspace.* refuses
// (VCS directories, credential file names); those count as skipped.
func fileDecorationsSendableNames(directory string, names []string) ([]string, int) {
	seen := make(map[string]bool, len(names))
	sendable := make([]string, 0, len(names))
	skipped := 0
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if workspacePathDenied(path.Join(directory, name), false) {
			skipped++
			continue
		}
		sendable = append(sendable, name)
	}
	return sendable, skipped
}

// lookupFileDecorationProvider finds the provider in the plugin's current
// generation and returns it with its action's maxPayloadBytes. A plugin
// without workspace.read is never given file names.
func (manager *pluginManager) lookupFileDecorationProvider(pluginID, providerID string) (FileDecorationContribution, int, error) {
	if _, _, ok := enabledContributionPoint(fileDecorationPointKey); !ok {
		return FileDecorationContribution{}, 0, newContributionError(contributionErrUnavailable, "file decoration providers are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return FileDecorationContribution{}, 0, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	provider, err := fileDecorationProviderLocked(record, providerID)
	if err != nil {
		return FileDecorationContribution{}, 0, err
	}
	if !manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead) {
		return FileDecorationContribution{}, 0, newContributionError(contributionErrForbidden, "file decoration provider %q requires capability workspace.read", providerID)
	}
	actionLimit := 0
	if action, ok := contributedActionLocked(record, provider.Action); ok {
		actionLimit = action.MaxPayloadBytes
	}
	return provider, actionLimit, nil
}

// fileDecorationProviderUnchanged is the Validate callback of a provider
// call: the entry must still be contributed, with the same action, by the
// generation that will answer, and that generation must still declare
// workspace.read.
func fileDecorationProviderUnchanged(provider FileDecorationContribution) func(*pluginRecord) error {
	return func(record *pluginRecord) error {
		current, err := fileDecorationProviderLocked(record, provider.ID)
		if err != nil {
			return newContributionError(contributionErrUnavailable, "file decoration provider %q is no longer contributed", provider.ID)
		}
		if current.Action != provider.Action || !manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead) {
			return newContributionError(contributionErrUnavailable, "file decoration provider %q changed", provider.ID)
		}
		return nil
	}
}

// fileDecorationProviderLocked finds the provider in the record's current
// generation. Called with record.mu held.
func fileDecorationProviderLocked(record *pluginRecord, providerID string) (FileDecorationContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(fileDecorationPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if provider, ok := entry.(FileDecorationContribution); ok && provider.ID == providerID {
					return provider, nil
				}
			}
		}
	}
	return FileDecorationContribution{}, newContributionError(contributionErrNotFound, "file decoration provider %q is not contributed by %q", providerID, record.id)
}

// parseFileDecorationsResult validates {"decorations": {...}}: keys that
// were not requested and items that fail validFileDecoration are dropped
// and counted. A result of another shape is an error.
func parseFileDecorationsResult(raw json.RawMessage, requested map[string]bool) (fileDecorationsResponse, error) {
	var result struct {
		Decorations map[string]json.RawMessage `json:"decorations"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return fileDecorationsResponse{}, fmt.Errorf("result must be {\"decorations\": {...}}: %v", err)
	}
	if result.Decorations == nil {
		return fileDecorationsResponse{}, errors.New("result must contain decorations")
	}
	response := fileDecorationsResponse{Decorations: map[string]FileDecoration{}}
	for name, item := range result.Decorations {
		if !requested[name] {
			response.Dropped++
			continue
		}
		decoration, ok := validFileDecoration(item)
		if !ok {
			response.Dropped++
			continue
		}
		response.Decorations[name] = decoration
	}
	return response, nil
}

// validFileDecoration accepts {badge?, color?, tooltip?} with at least a
// badge or a colour; any present field that is invalid rejects the item:
// the badge must pass validDecorationBadge, the colour must be a token name
// of lists.colors (never a CSS value) and the tooltip one line of at most
// tooltipMaxRunes characters without control or format characters.
func validFileDecoration(raw json.RawMessage) (FileDecoration, bool) {
	var item struct {
		Badge   *string `json:"badge"`
		Color   *string `json:"color"`
		Tooltip *string `json:"tooltip"`
	}
	if decodeStrictJSON(raw, &item, true) != nil {
		return FileDecoration{}, false
	}
	var decoration FileDecoration
	if item.Badge != nil {
		badge, ok := validDecorationBadge(*item.Badge)
		if !ok {
			return FileDecoration{}, false
		}
		decoration.Badge = badge
	}
	if item.Color != nil {
		if !slices.Contains(fileDecorationsColors, *item.Color) {
			return FileDecoration{}, false
		}
		decoration.Color = *item.Color
	}
	if item.Tooltip != nil {
		tooltip, err := validateContributionLine(*item.Tooltip, fileDecorationsTooltipMaxRunes, false)
		if err != nil || strings.ContainsFunc(tooltip, contextRuneRefused) {
			return FileDecoration{}, false
		}
		decoration.Tooltip = tooltip
	}
	if decoration.Badge == "" && decoration.Color == "" {
		return FileDecoration{}, false
	}
	return decoration, true
}

// validDecorationBadge trims the badge and accepts 1–badgeMaxRunes visible
// characters, each a letter, digit, punctuation mark or symbol: no spaces,
// control or format characters (zero-width joiners, bidi controls) and no
// combining marks, so a badge cannot hide or reorder text.
func validDecorationBadge(value string) (string, bool) {
	badge := strings.TrimSpace(value)
	count := utf8.RuneCountInString(badge)
	if !utf8.ValidString(badge) || count < 1 || count > fileDecorationsBadgeMaxRunes {
		return "", false
	}
	for _, r := range badge {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || contextRuneRefused(r) {
			return "", false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return "", false
		}
	}
	return badge, true
}

// validFileDecorationName accepts one path segment: non-empty, at most
// nameMaxBytes bytes of valid UTF-8, no slash or backslash, not "." or "..",
// and no control, format, line separator or other unsafe character.
func validFileDecorationName(name string) bool {
	return name != "" && len(name) <= fileDecorationsNameMaxBytes && utf8.ValidString(name) &&
		!strings.ContainsAny(name, `/\`) && name != "." && name != ".." &&
		checkContributionRunes(name, false, false) == nil && !strings.ContainsFunc(name, contextRuneRefused)
}

func normalizeFileDecorationsRequest(request *fileDecorationsRequest) error {
	if !pluginIDPattern.MatchString(request.PluginID) {
		return newContributionError(contributionErrInvalidRequest, "pluginId %q is invalid", request.PluginID)
	}
	if !fileDecorationIDPattern.MatchString(request.ProviderID) {
		return newContributionError(contributionErrInvalidRequest, "providerId %q is invalid", request.ProviderID)
	}
	if strings.TrimSpace(request.ProjectRoot) == "" {
		return newContributionError(contributionErrInvalidRequest, "projectRoot is required")
	}
	if request.Directory != "" {
		if _, err := validateContributionRelativePath(request.Directory, fileDecorationsDirMaxBytes); err != nil ||
			!languageLocationPathCharsOK(request.Directory) || strings.ContainsFunc(request.Directory, contextRuneRefused) {
			return newContributionError(contributionErrInvalidRequest, "directory must be \"\" or a clean project-relative path of at most %d bytes", fileDecorationsDirMaxBytes)
		}
	}
	if len(request.Names) == 0 || len(request.Names) > fileDecorationsNamesMax {
		return newContributionError(contributionErrInvalidRequest, "names must list 1 to %d names", fileDecorationsNamesMax)
	}
	for index, name := range request.Names {
		if !validFileDecorationName(name) {
			return newContributionError(contributionErrInvalidRequest, "names[%d] must be a single file name of at most %d bytes", index, fileDecorationsNameMaxBytes)
		}
	}
	if request.Locale == "" {
		request.Locale = "en"
	}
	if len(request.Locale) > 35 || !searchLocalePattern.MatchString(request.Locale) {
		return newContributionError(contributionErrInvalidRequest, "locale %q is invalid", request.Locale)
	}
	return nil
}

func pluginHostFileDecorationsHandler(_, args string) (string, error) {
	var request fileDecorationsRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_file_decorations: invalid arguments: %v", err)
	}
	response, err := host().fileDecorations(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_file_decorations", pluginHostFileDecorationsHandler)
}
