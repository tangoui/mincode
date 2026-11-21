package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
	backendfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

// Search provider file targets (runtime feature searchFileTargets:1, offered
// only to plugins declaring workspace.read).
//
// Query: a search is file-enabled only when the admitted generation
// negotiated the feature, the manifest still declares workspace.read and the
// published workspace scope is Ready. The payload then gains
// project:{name, root} (the scope's canonical root, never a plugin-supplied
// one); otherwise it stays byte-for-byte the legacy {provider, query,
// locale} and file items are dropped and counted. The query is bound to that
// scope: a scope begin cancels it in the prepare and invoke phases, and a
// response is never stamped with a scope that has since changed. The desktop
// response carries the host-filled fileScope {workspaceId, root, revision}
// and providerRuntime {instance, generation}; neither is plugin-writable nor
// sent to the plugin.
//
// Payload limit (ruling R2): the project block must fit the same limit as
// the legacy payload (payloadMaxBytes, or the action's smaller
// maxPayloadBytes); no limit is widened. When it does not fit, the query
// falls back to the legacy payload with file targets off, exactly as if no
// root were open, so a plugin sees "no project" and returns no file items.
//
// Click: plugin_host_validate_search_file_target revalidates a result before
// the desktop opens it — same scope (workspace, root, revision), provider
// still contributed, workspace.read, the same runtime instance and
// generation with the feature negotiated, the path policy on the requested
// and the resolved path, and an existing regular file. It never reads the
// file; the open itself still goes through the Go file boundary, and this is
// not an OS sandbox (a later swap remains possible).

const searchFileTargetsFeature = "searchFileTargets"

// searchFileScope is the host-filled workspace scope a file result belongs to.
type searchFileScope struct {
	WorkspaceID string `json:"workspaceId"`
	Root        string `json:"root"`
	Revision    string `json:"revision"`
}

// searchFileTargetRequest is the strict click-time request; Line and Column
// are pointers so a present 0 is refused rather than read as "absent".
type searchFileTargetRequest struct {
	PluginID        string          `json:"pluginId"`
	ProviderID      string          `json:"providerId"`
	FileScope       searchFileScope `json:"fileScope"`
	ProviderRuntime runtimeIdentity `json:"providerRuntime"`
	Path            string          `json:"path"`
	Line            *int            `json:"line,omitempty"`
	Column          *int            `json:"column,omitempty"`
}

// searchFilePayload is searchPayload plus the opt-in project block.
type searchFilePayload struct {
	searchPayload
	Project *contributionProject `json:"project"`
}

// searchFileBinding is the scope and runtime a file-enabled query was
// admitted under. stop releases the scope cancellation hook.
type searchFileBinding struct {
	scope   workspaceScopeSnapshot
	ctx     context.Context
	runtime runtimeIdentity
	stop    func() bool
}

// searchFileStageHook lets tests act at "prepared" (after the scope is bound,
// before dispatch) and "invoked" (after the provider answered). Nil in
// production.
var searchFileStageHook func(stage string)

// check reports a scope change since the binding; nil binding is legacy.
func (binding *searchFileBinding) check() error {
	if binding == nil || binding.ctx.Err() == nil {
		return nil
	}
	return newContributionError(contributionErrCancelled, "workspace scope changed during the search; search again")
}

// bindSearchFileScope decides at admission whether the query is
// file-enabled and returns the payload to send. Negotiation comes from the
// admitted generation, never from a record that has not started.
func (manager *pluginManager) bindSearchFileScope(admission *contributedAdmission, pluginID, actionID string, payload searchPayload, legacy []byte) (*searchFileBinding, []byte) {
	if !admission.Runtime.featureAtGeneration(admission.Generation, searchFileTargetsFeature) {
		return nil, legacy
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return nil, legacy
	}
	record.mu.Lock()
	canRead := manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
	limit := searchProviderPayloadMaxBytes
	if action, ok := contributedActionLocked(record, actionID); ok && action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	record.mu.Unlock()
	if !canRead {
		return nil, legacy
	}
	scope := manager.workspaceScope()
	if !scope.Ready {
		return nil, legacy
	}
	scopeCtx, err := manager.workspaceScopeContext(scope)
	if err != nil {
		return nil, legacy
	}
	encoded, err := encodeContributionJSON(searchFilePayload{searchPayload: payload, Project: &contributionProject{Name: filepath.Base(scope.Root), Root: scope.Root}})
	if err != nil || len(encoded) > limit {
		// R2: never widen the limit; treat as "no project" (see file comment).
		return nil, legacy
	}
	return &searchFileBinding{
		scope: scope, ctx: scopeCtx, runtime: admission.Runtime.identity(admission.Generation),
		stop: context.AfterFunc(scopeCtx, admission.cancel),
	}, encoded
}

// checkSearchFilePathSyntax adds the language-location character rules and
// the requested-path denial (VCS internals, sensitive names) to the clean
// relative path check.
func checkSearchFilePathSyntax(value string) error {
	if strings.ContainsFunc(value, contextRuneRefused) || !languageLocationPathCharsOK(value) {
		return errors.New("file target path contains a disallowed character")
	}
	if workspacePathDenied(value, false) {
		return errors.New("file target path is not accessible to plugins")
	}
	return nil
}

func (manager *pluginManager) validateSearchFileTarget(ctx context.Context, request searchFileTargetRequest) (SearchResultTarget, error) {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	forbidden := func(format string, args ...any) error {
		return newContributionError(contributionErrForbidden, format, args...)
	}
	if !pluginIDPattern.MatchString(request.PluginID) {
		return SearchResultTarget{}, invalid("pluginId %q is invalid", request.PluginID)
	}
	if !searchProviderIDPattern.MatchString(request.ProviderID) {
		return SearchResultTarget{}, invalid("providerId %q is invalid", request.ProviderID)
	}
	if request.FileScope.Root == "" || request.FileScope.Revision == "" {
		return SearchResultTarget{}, invalid("fileScope is required")
	}
	if _, ok := parseRuntimeIdentityPart(request.ProviderRuntime.Instance); !ok {
		return SearchResultTarget{}, invalid("providerRuntime is invalid")
	}
	generation, ok := parseRuntimeIdentityPart(request.ProviderRuntime.Generation)
	if !ok {
		return SearchResultTarget{}, invalid("providerRuntime is invalid")
	}
	raw, err := json.Marshal(searchRawTarget{Type: "file", Path: request.Path, Line: request.Line, Column: request.Column})
	if err != nil {
		return SearchResultTarget{}, invalid("target: %v", err)
	}
	// The same normalizer as the query, with the result's own root.
	target, err := normalizeSearchResultTarget(raw, searchTargetScope{fileRoot: request.FileScope.Root})
	if err != nil {
		return SearchResultTarget{}, invalid("%v", err)
	}
	current := func() error {
		if err := ctx.Err(); err != nil {
			return newContributionError(contributionErrCancelled, "request was cancelled")
		}
		scope := manager.workspaceScope()
		if !scope.Ready || scope.WorkspaceID != request.FileScope.WorkspaceID || scope.Root != request.FileScope.Root || scope.Revision != request.FileScope.Revision {
			return newContributionError(contributionErrUnavailable, "the workspace changed since this search; search again")
		}
		return manager.checkSearchFileProvider(request, generation)
	}
	if err := current(); err != nil {
		return SearchResultTarget{}, err
	}
	root := request.FileScope.Root
	if err := checkLanguageLocationTarget(root, target.Path); err != nil {
		return SearchResultTarget{}, forbidden("file target %v", err)
	}
	file, err := backendfs.ResolveProjectFile(root, target.Path)
	switch {
	case backendfs.IsProjectPathNotExist(err):
		return SearchResultTarget{}, newContributionError(contributionErrNotFound, "file %q does not exist", target.Path)
	case err != nil:
		return SearchResultTarget{}, forbidden("file target could not be resolved")
	case workspacePathDenied(file.ResolvedRelativePath, false):
		return SearchResultTarget{}, forbidden("file target is not accessible to plugins")
	}
	info, err := os.Stat(file.Path)
	switch {
	case err != nil && os.IsNotExist(err):
		return SearchResultTarget{}, newContributionError(contributionErrNotFound, "file %q does not exist", target.Path)
	case err != nil:
		return SearchResultTarget{}, forbidden("file target could not be inspected")
	case !info.Mode().IsRegular():
		return SearchResultTarget{}, forbidden("file target is not a regular file")
	}
	// The filesystem checks took time: confirm nothing changed meanwhile.
	if err := current(); err != nil {
		return SearchResultTarget{}, err
	}
	return target, nil
}

// checkSearchFileProvider requires the provider to still be contributed by a
// plugin with workspace.read whose current runtime is the one that answered
// (same instance and generation) and negotiated searchFileTargets.
func (manager *pluginManager) checkSearchFileProvider(request searchFileTargetRequest, generation uint64) error {
	record, err := manager.record(request.PluginID)
	if err != nil {
		return newContributionError(contributionErrUnavailable, "search plugin %q was removed; search again", request.PluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if _, err := searchProviderLocked(record, request.ProviderID); err != nil {
		return newContributionError(contributionErrUnavailable, "search provider %q was removed; search again", request.ProviderID)
	}
	if !manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead) {
		return newContributionError(contributionErrUnavailable, "plugin %q no longer has workspace.read; search again", request.PluginID)
	}
	if err := manager.validateRuntimeIdentityLocked(record, request.ProviderRuntime); err != nil {
		return err
	}
	if !record.runtime.featureAtGeneration(generation, searchFileTargetsFeature) {
		return newContributionError(contributionErrUnavailable, "plugin %q runtime does not provide file targets", request.PluginID)
	}
	return nil
}

func pluginHostValidateSearchFileTargetHandler(ctx context.Context, _, args string) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(args), "{") {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_validate_search_file_target: arguments must be an object")
	}
	var request searchFileTargetRequest
	if err := decodeStrictJSONValue([]byte(args), &request, true, "args"); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_validate_search_file_target: invalid arguments: %v", err)
	}
	target, err := host().validateSearchFileTarget(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(target)
}

func init() {
	core.RegisterCtx("plugin_host_validate_search_file_target", pluginHostValidateSearchFileTargetHandler)
}
