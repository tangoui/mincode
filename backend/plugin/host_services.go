package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"mincode/app/plugins/api"
)

// host.services.* (design §7.1, §8): process plugins call services offered
// by other plugins. Callers need the plugins capability, like
// host.plugins.invoke, and the same approval rules and grant store apply
// (confirmPluginCall): a call of a read-risk entry can be remembered per
// caller → provider plugin, everything else is confirmed each time; a fully
// trusted side (caller or provider) is not asked (full_trust.go). The
// provider is chosen by selectServiceProvider; when several remain the user
// picks one in a "serviceProvider" prompt. The prompts share the caller's
// timeoutMs. With interactive:false a call that would show either prompt
// fails at once with interaction_required; it never passes one that would
// have been asked.
//
// Every handle is checked against the calling plugin: call IDs belong to the
// caller that started them, upload tokens to their uploader, input tokens to
// the provider they were given to, outputs to the caller they were handed to,
// and output sessions to the provider of a running call. Anything else is
// not_found. No result carries a host path.
//
// The file methods a provider needs (read, release, create.*) are also open
// to plugins that declare contributes.services without the plugins
// capability: providing a service needs no extra authorization (design §10),
// and every handle they accept is still bound to the plugin's identity.

const (
	serviceProviderPromptKind = "serviceProvider"
	// serviceProviderRememberID may accompany the picked item in a
	// serviceProvider reply: make the pick the service's default.
	serviceProviderRememberID = "remember"

	serviceSourceWorkspace = "workspace"
	serviceSourceStorage   = "storage"
	serviceSourceUpload    = "upload"
)

var (
	serviceCallIDPattern  = regexp.MustCompile(`^` + serviceCallIDPrefix + `[0-9a-f]{48}$`)
	serviceTokenPattern   = regexp.MustCompile(`^` + serviceExchangeTokenPrefix + `[0-9a-f]{48}$`)
	serviceSessionPattern = regexp.MustCompile(`^` + serviceExchangeSessionPrefix + `[0-9a-f]{48}$`)

	// servicesManager is the manager the handlers use (test seam).
	servicesManager = host
)

func init() {
	registerGenerationHostMethod("host.services.invoke", nil, api.CapabilityPlugins, hostServicesInvokeGeneration)
	registerGenerationHostMethod("host.services.start", nil, api.CapabilityPlugins, hostServicesStartGeneration)
	for name, handler := range map[string]hostMethodHandler{
		"host.services.list":               hostServicesList,
		"host.services.status":             hostServicesStatus,
		"host.services.cancel":             hostServicesCancel,
		"host.services.file.save":          hostServicesFileSave,
		"host.services.file.upload.begin":  hostServicesUploadBegin,
		"host.services.file.upload.append": hostServicesUploadAppend,
		"host.services.file.upload.commit": hostServicesUploadCommit,
	} {
		registerHostMethod(name, nil, api.CapabilityPlugins, handler)
	}
	for name, handler := range map[string]hostMethodHandler{
		"host.services.file.read":          hostServicesFileRead,
		"host.services.file.release":       hostServicesFileRelease,
		"host.services.file.create.begin":  hostServicesCreateBegin,
		"host.services.file.create.append": hostServicesCreateAppend,
		"host.services.file.create.commit": hostServicesCreateCommit,
	} {
		registerHostMethod(name, serviceFileGate, "", handler)
	}
}

// serviceFileGate admits callers (plugins capability) and providers
// (plugins that declare services, read from the live record when there is
// one).
func serviceFileGate(runtimeHost *processRuntime) bool {
	if _, ok := runtimeHost.capabilities[api.CapabilityPlugins]; ok {
		return true
	}
	if record := runtimeHost.ownerRecord; record != nil {
		record.mu.Lock()
		defer record.mu.Unlock()
		return len(servicesLocked(record)) > 0
	}
	resolved := runtimeHost.manifest.contribution(servicePointKey)
	return resolved != nil && len(resolved.Entries) > 0
}

// decodeServiceParams is decodeHostUIParams that also accepts no params.
func decodeServiceParams(params json.RawMessage, target any) *rpcError {
	if trimmed := bytes.TrimSpace(params); len(trimmed) == 0 || string(trimmed) == "null" {
		params = json.RawMessage(`{}`)
	}
	return decodeHostUIParams(params, target)
}

// serviceRPCError reports err to a plugin with its contribution error code.
func serviceRPCError(err error) *rpcError {
	var coded *contributionError
	if errors.As(err, &coded) {
		return &rpcError{Code: -32000, Message: coded.Error(), Data: map[string]string{"code": coded.Code}}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &rpcError{Code: -32000, Message: "the service call was interrupted", Data: map[string]string{"code": contributionErrCancelled}}
	}
	return &rpcError{Code: -32000, Message: err.Error()}
}

func serviceNotFound(message string) *rpcError {
	return &rpcError{Code: -32000, Message: contributionErrNotFound + ": " + message, Data: map[string]string{"code": contributionErrNotFound}}
}

// ---- list ----

type serviceProviderView struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	EntryID        string            `json:"entryId"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Description    string            `json:"description,omitempty"`
	Input          ServiceShape      `json:"input"`
	Output         ServiceShape      `json:"output"`
	Params         map[string]any    `json:"params,omitempty"`
	Risk           string            `json:"risk"`
	IsDefault      bool              `json:"isDefault"`
}

type serviceView struct {
	Service   string                `json:"service"`
	Providers []serviceProviderView `json:"providers"`
}

func hostServicesList(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Service string `json:"service"`
	}
	if rpcErr := decodeServiceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	if request.Service != "" && validateServiceName(request.Service) != nil {
		return nil, hostUIParamsError("service must be a service name")
	}
	views := []serviceView{}
	index := map[string]int{}
	for _, provider := range servicesManager().serviceProviders(request.Service, runtimeHost.pluginID) {
		entry := provider.Entry
		position, ok := index[entry.Service]
		if !ok {
			position = len(views)
			index[entry.Service] = position
			views = append(views, serviceView{Service: entry.Service, Providers: []serviceProviderView{}})
		}
		views[position].Providers = append(views[position].Providers, serviceProviderView{
			PluginID: provider.PluginID, PluginName: provider.PluginName, EntryID: entry.ID, Title: entry.Title, TitleLocalized: entry.TitleLocalized, Description: entry.Description,
			Input: entry.Input, Output: entry.Output, Params: entry.Params, Risk: entry.Risk, IsDefault: provider.IsDefault,
		})
	}
	slices.SortFunc(views, func(a, b serviceView) int { return strings.Compare(a.Service, b.Service) })
	return map[string]any{"services": views}, nil
}

// ---- invoke / start ----

type serviceInvokeRequest struct {
	Service   string              `json:"service"`
	Provider  *serviceProviderRef `json:"provider"`
	Input     json.RawMessage     `json:"input"`
	Params    map[string]any      `json:"params"`
	TimeoutMs int                 `json:"timeoutMs"`
	// Interactive false makes a call that would need a prompt (the approval
	// or the provider picker) fail with interaction_required instead; nil
	// is true.
	Interactive *bool `json:"interactive"`
}

type serviceFileSource struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Token  string `json:"token,omitempty"`
}

// resolvedServiceSource is one input file, checked but not yet copied.
// absPath (workspace and storage sources) never leaves the host.
type resolvedServiceSource struct {
	upload  string
	absPath string
	name    string
	size    int64
}

// parseServiceInput splits the input into text (a JSON string, passed on as
// is) or file sources, resolved against the caller's rights.
func parseServiceInput(runtimeHost *processRuntime, raw json.RawMessage) (json.RawMessage, []resolvedServiceSource, *rpcError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if json.Unmarshal(trimmed, &text) != nil {
			return nil, nil, hostUIParamsError("input must be text or a list of file sources")
		}
		return slices.Clone(trimmed), nil, nil
	case '[':
		var sources []serviceFileSource
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&sources) != nil || len(sources) == 0 || len(sources) > serviceFilesMax {
			return nil, nil, hostUIParamsError(fmt.Sprintf("input must list 1-%d file sources", serviceFilesMax))
		}
		resolved := make([]resolvedServiceSource, 0, len(sources))
		for _, source := range sources {
			item, rpcErr := resolveServiceSource(runtimeHost, source)
			if rpcErr != nil {
				return nil, nil, rpcErr
			}
			resolved = append(resolved, item)
		}
		return nil, resolved, nil
	}
	return nil, nil, hostUIParamsError("input must be text or a list of file sources")
}

func resolveServiceSource(runtimeHost *processRuntime, source serviceFileSource) (resolvedServiceSource, *rpcError) {
	switch source.Source {
	case serviceSourceWorkspace:
		if source.Token != "" {
			return resolvedServiceSource{}, hostUIParamsError("a workspace source takes a path")
		}
		return resolveWorkspaceServiceSource(runtimeHost, source.Path)
	case serviceSourceStorage:
		if source.Token != "" {
			return resolvedServiceSource{}, hostUIParamsError("a storage source takes a path")
		}
		return resolveStorageServiceSource(runtimeHost, source.Path)
	case serviceSourceUpload:
		if source.Path != "" || !serviceTokenPattern.MatchString(source.Token) {
			return resolvedServiceSource{}, hostUIParamsError("an upload source takes an upload token")
		}
		file, err := serviceFiles.uploadInfo(runtimeHost.pluginID, source.Token)
		if err != nil {
			return resolvedServiceSource{}, serviceRPCError(err)
		}
		return resolvedServiceSource{upload: source.Token, name: file.Name, size: file.Size}, nil
	}
	return resolvedServiceSource{}, hostUIParamsError(`a file source is {source:"workspace"|"storage", path} or {source:"upload", token}`)
}

// relativeServicePath reports a clean slash-separated relative path.
func relativeServicePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// resolveWorkspaceServiceSource applies the host.workspace.read rules: the
// caller needs workspace.read, the path stays inside the workspace after
// symlinks, and VCS internals and credential files are refused.
func resolveWorkspaceServiceSource(runtimeHost *processRuntime, relative string) (resolvedServiceSource, *rpcError) {
	if _, ok := runtimeHost.capabilities[api.CapabilityWorkspaceRead]; !ok {
		return resolvedServiceSource{}, hostUIParamsError("workspace sources need the workspace.read capability")
	}
	if !relativeServicePath(relative) {
		return resolvedServiceSource{}, hostUIParamsError("a workspace source path must be relative to the workspace and must not contain '..'")
	}
	file, rpcErr := resolveWorkspaceEntry(relative, false)
	if rpcErr != nil {
		return resolvedServiceSource{}, rpcErr
	}
	info, err := os.Stat(file.Path)
	if err != nil || !info.Mode().IsRegular() {
		return resolvedServiceSource{}, hostUIParamsError("a workspace source must be a regular file")
	}
	return resolvedServiceSource{absPath: file.Path, name: sanitizeExchangeName(filepath.Base(file.Path)), size: info.Size()}, nil
}

// resolveStorageServiceSource applies the host.storage.file rules: a
// sandbox-relative path without symlinks in the caller's own storage, which
// needs the storage capability like every other storage access.
func resolveStorageServiceSource(runtimeHost *processRuntime, name string) (resolvedServiceSource, *rpcError) {
	if _, ok := runtimeHost.capabilities[api.CapabilityStorage]; !ok {
		return resolvedServiceSource{}, hostUIParamsError("storage sources need the storage capability")
	}
	if validPluginStoragePath(name, false) != nil {
		return resolvedServiceSource{}, hostUIParamsError("invalid storage source path")
	}
	root, err := openPluginStorage(runtimeHost.pluginID, false)
	if err != nil {
		return resolvedServiceSource{}, serviceNotFound("storage file not found")
	}
	defer root.Close()
	if err := checkStoragePath(root, name, false, false); err != nil {
		return resolvedServiceSource{}, serviceNotFound("storage file not found")
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return resolvedServiceSource{}, serviceNotFound("storage file not found")
	}
	base, err := pluginStorageRoot(runtimeHost.pluginID)
	if err != nil {
		return resolvedServiceSource{}, serviceNotFound("storage file not found")
	}
	return resolvedServiceSource{absPath: filepath.Join(base, filepath.FromSlash(name)), name: sanitizeExchangeName(path.Base(name)), size: info.Size()}, nil
}

// serviceInputPreview is the approval prompt's input summary: the first
// 2 KiB of text, or each file's name and size.
func serviceInputPreview(text json.RawMessage, sources []resolvedServiceSource) string {
	if len(sources) == 0 {
		var value string
		_ = json.Unmarshal(text, &value)
		return payloadPreview([]byte(value))
	}
	lines := make([]string, 0, len(sources))
	for _, source := range sources {
		lines = append(lines, fmt.Sprintf("%s (%d bytes)", source.name, source.size))
	}
	return payloadPreview([]byte(strings.Join(lines, "\n")))
}

// serviceProviderChooser asks the caller's user to pick a provider before
// deadline. A prompt failure or refusal is stored in failure.
func serviceProviderChooser(runtimeHost *processRuntime, deadline time.Time, failure **rpcError) providerChooser {
	return func(ctx context.Context, service string, candidates []serviceProvider) (serviceProvider, bool, error) {
		chosen, remember, rpcErr := promptServiceProvider(ctx, runtimeHost.pluginID, runtimeHost.displayName(), service, candidates, time.Until(deadline))
		if rpcErr != nil {
			*failure = rpcErr
			return serviceProvider{}, false, errors.New(rpcErr.Message)
		}
		return chosen, remember, nil
	}
}

// nonInteractiveChooser refuses to show the provider picker; through
// serviceRPCError its error reads like interactionRequiredError.
func nonInteractiveChooser() providerChooser {
	return func(context.Context, string, []serviceProvider) (serviceProvider, bool, error) {
		return serviceProvider{}, false, newContributionError(contributionErrInteractionRequired, "the call needs the user's confirmation")
	}
}

// interactionRequiredError reports that a non-interactive call would have
// needed the user's confirmation. It does not say which side lacks trust.
func interactionRequiredError() *rpcError {
	return &rpcError{Code: -32000, Message: contributionErrInteractionRequired + ": the call needs the user's confirmation",
		Data: map[string]string{"code": contributionErrInteractionRequired}}
}

// promptServiceProvider shows a serviceProvider prompt from origin (a plugin,
// or serviceCallHostOwner for the host) and returns the pick; a refusal or
// a reply without a pick is declined.
func promptServiceProvider(ctx context.Context, originID, originName, service string, candidates []serviceProvider, timeout time.Duration) (serviceProvider, bool, *rpcError) {
	items := make([]quickPickItem, 0, len(candidates))
	ids := make(map[string]bool, len(candidates))
	for index, candidate := range candidates {
		id := "provider-" + strconv.Itoa(index)
		ids[id] = true
		items = append(items, quickPickItem{ID: id, Label: cleanHostText(candidate.PluginName, hostUITitleMaxRunes, false),
			Description: cleanHostText(candidate.Entry.Title, hostUITitleMaxRunes, false), Detail: cleanHostText(candidate.Entry.Description, hostUITextMaxRunes, false)})
	}
	event := promptEvent{Kind: serviceProviderPromptKind, PluginID: originID, PluginName: originName, Title: service, Service: service, Items: items}
	reply, rpcErr := hostPrompts.ask(ctx, event, &pendingPrompt{pluginID: originID, kind: serviceProviderPromptKind, items: ids}, timeout)
	if rpcErr == nil && reply.Cancelled {
		rpcErr = pluginCallDeclined()
	}
	if rpcErr != nil {
		return serviceProvider{}, false, rpcErr
	}
	for _, id := range reply.IDs {
		if index, found := strings.CutPrefix(id, "provider-"); found {
			position, _ := strconv.Atoi(index)
			return candidates[position], slices.Contains(reply.IDs, serviceProviderRememberID), nil
		}
	}
	return serviceProvider{}, false, pluginCallDeclined()
}

// validServiceProviderReply accepts exactly one offered item, optionally with
// serviceProviderRememberID.
func validServiceProviderReply(items map[string]bool, reply promptReply) bool {
	if reply.Value != nil || len(reply.IDs) == 0 || len(reply.IDs) > 2 {
		return false
	}
	picked, remember := 0, 0
	for _, id := range reply.IDs {
		switch {
		case items[id]:
			picked++
		case id == serviceProviderRememberID:
			remember++
		default:
			return false
		}
	}
	return picked == 1 && remember <= 1
}

// serviceEffectiveRisk is the risk of the provider's backing action after its
// permissions (network raises read to network, and so on), the way
// host.plugins.invoke rates a call. It also checks that the action still has
// the risk recorded in the entry. Caller holds record.mu.
func serviceEffectiveRiskLocked(record *pluginRecord, entry ServiceContribution) (ManifestUIAction, error) {
	if record.manifest == nil {
		return ManifestUIAction{}, newContributionError(contributionErrNotFound, "plugin %q is not installed", record.id)
	}
	declared, ok := contributedActionLocked(record, entry.Action)
	if !ok || declared.Risk != entry.Risk {
		return ManifestUIAction{}, newContributionError(contributionErrForbidden, "service entry %q changed since it was approved; call it again", entry.ID)
	}
	resolved, err := resolveAIActionPermissions(declared, record.manifest.Capabilities)
	if err != nil {
		return ManifestUIAction{}, newContributionError(contributionErrForbidden, "service entry %q: %v", entry.ID, err)
	}
	return resolved, nil
}

// prepareServiceCall decodes an invoke/start request, resolves its inputs,
// picks the provider and gets the user's approval.
func prepareServiceCall(runtimeHost *processRuntime, generation uint64, params json.RawMessage) (serviceCallRequest, *rpcError) {
	callerSnapshot, err := captureServiceCaller(runtimeHost, generation)
	if err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	manager := servicesManager()
	if err := manager.validateServiceCaller(callerSnapshot); err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	var request serviceInvokeRequest
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return serviceCallRequest{}, rpcErr
	}
	approvalTimeout, rpcErr := promptTimeout(request.TimeoutMs)
	if rpcErr != nil {
		return serviceCallRequest{}, rpcErr
	}
	deadline := time.Now().Add(approvalTimeout)
	if validateServiceName(request.Service) != nil {
		return serviceCallRequest{}, hostUIParamsError("service must be a service name")
	}
	if request.Provider != nil && (!pluginIDPattern.MatchString(request.Provider.PluginID) ||
		request.Provider.EntryID != "" && !serviceIDPattern.MatchString(request.Provider.EntryID)) {
		return serviceCallRequest{}, hostUIParamsError("provider must be {pluginId, entryId?}")
	}
	text, sources, rpcErr := parseServiceInput(runtimeHost, request.Input)
	if rpcErr != nil {
		return serviceCallRequest{}, rpcErr
	}

	interactive := request.Interactive == nil || *request.Interactive
	ctx, cancel := context.WithDeadline(runtimeHost.promptContext(), deadline)
	defer cancel()
	var failure *rpcError
	choose := nonInteractiveChooser()
	if interactive {
		choose = serviceProviderChooser(runtimeHost, deadline, &failure)
	}
	provider, err := manager.selectServiceProvider(ctx, request.Service, request.Provider, runtimeHost.pluginID, choose)
	if failure != nil {
		return serviceCallRequest{}, failure
	}
	if err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	entry := provider.Entry
	if len(sources) > 0 {
		if !serviceShapeHasFiles(entry.Input) {
			return serviceCallRequest{}, serviceRPCError(newContributionError(contributionErrInvalidRequest, "service %q does not take files", entry.Service))
		}
		for _, source := range sources {
			if !serviceAccepts(entry.Input.Accepts, source.name) {
				return serviceCallRequest{}, serviceRPCError(newContributionError(contributionErrInvalidRequest, "service %q does not accept %q", entry.Service, source.name))
			}
			if source.size > entry.Input.MaxBytes {
				return serviceCallRequest{}, serviceRPCError(newContributionError(contributionErrTooLarge, "file %q is %d bytes, the service accepts %d", source.name, source.size, entry.Input.MaxBytes))
			}
		}
	}

	record, err := manager.record(provider.PluginID)
	if err != nil {
		return serviceCallRequest{}, serviceRPCError(newContributionError(contributionErrNotFound, "plugin %q is not installed", provider.PluginID))
	}
	record.mu.Lock()
	action, err := serviceEffectiveRiskLocked(record, entry)
	record.mu.Unlock()
	if err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	action.Description = entry.Description
	target := pluginCallTargetInfo{Name: provider.PluginName, Action: action, Service: entry.Service, ServiceTitle: entry.Title}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return serviceCallRequest{}, pluginCallDeclined()
	}
	if err := manager.validateServiceCaller(callerSnapshot); err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	if rpcErr := confirmPluginCall(runtimeHost, target, provider.PluginID, entry.Action, serviceInputPreview(text, sources), remaining, interactive); rpcErr != nil {
		return serviceCallRequest{}, rpcErr
	}

	if err := manager.validateServiceCaller(callerSnapshot); err != nil {
		return serviceCallRequest{}, serviceRPCError(err)
	}
	caller := callerSnapshot.PluginID
	approvedRisk := action.Risk
	return serviceCallRequest{
		Caller:   callerSnapshot,
		Provider: provider,
		Input:    text,
		Params:   request.Params,
		PrepareInputs: func(callID string) ([]exchangeFile, error) {
			files := make([]exchangeFile, 0, len(sources))
			for _, source := range sources {
				var file exchangeFile
				var err error
				if source.upload != "" {
					file, err = serviceFiles.adoptUpload(callID, caller, source.upload, provider.PluginID)
				} else {
					file, err = serviceFiles.snapshotPath(callID, provider.PluginID, source.absPath)
				}
				if err != nil {
					return nil, err
				}
				files = append(files, file)
			}
			return files, nil
		},
		// The approval covered this effective risk; a provider update that
		// changes it (for example a new network permission) needs a new one.
		Revalidate: func(record *pluginRecord) error {
			current, err := serviceEffectiveRiskLocked(record, entry)
			if err != nil {
				return err
			}
			if current.Risk != approvedRisk {
				return newContributionError(contributionErrForbidden, "service entry %q changed since it was approved; call it again", entry.ID)
			}
			return nil
		},
	}, nil
}

// pluginServiceSnapshot is what a plugin caller sees of a call: file
// outputs are its output.
func pluginServiceSnapshot(snapshot ServiceCallSnapshot) map[string]any {
	view := map[string]any{"callId": snapshot.CallID, "state": snapshot.State, "provider": snapshot.Provider}
	if snapshot.Progress != nil {
		view["progress"] = snapshot.Progress
	}
	if len(snapshot.RawProgress) > 0 {
		view["rawProgress"] = snapshot.RawProgress
	}
	if output := serviceOutputValue(snapshot); output != nil {
		view["output"] = output
	}
	if snapshot.Error != "" {
		view["error"] = snapshot.Error
	}
	if snapshot.Unconfirmed {
		view["unconfirmed"] = true
	}
	return view
}

func serviceOutputValue(snapshot ServiceCallSnapshot) any {
	if len(snapshot.Files) > 0 {
		return snapshot.Files
	}
	if len(snapshot.Output) > 0 {
		return snapshot.Output
	}
	return nil
}

// serviceCallFailure turns a failed or cancelled snapshot into an error;
// Error has the shape "<code>: detail".
func serviceCallFailure(snapshot ServiceCallSnapshot) *rpcError {
	code := contributionErrCancelled
	message := "the service call was cancelled"
	if snapshot.State == serviceCallFailed {
		message = snapshot.Error
		code = contributionErrPluginError
		if prefix, _, found := strings.Cut(snapshot.Error, ":"); found && !strings.ContainsAny(prefix, " \t") {
			code = prefix
		}
	}
	data := map[string]string{"code": code}
	if snapshot.Unconfirmed {
		data["unconfirmed"] = "true"
	}
	return &rpcError{Code: -32000, Message: message, Data: data}
}

func hostServicesInvokeGeneration(runtimeHost *processRuntime, generation uint64, params json.RawMessage) (any, *rpcError) {
	request, rpcErr := prepareServiceCall(runtimeHost, generation, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	snapshot, err := servicesManager().runServiceCallSync(runtimeHost.promptContext(), request)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	if snapshot.State != serviceCallSucceeded {
		return nil, serviceCallFailure(snapshot)
	}
	return map[string]any{"provider": snapshot.Provider, "output": serviceOutputValue(snapshot)}, nil
}

func hostServicesStartGeneration(runtimeHost *processRuntime, generation uint64, params json.RawMessage) (any, *rpcError) {
	request, rpcErr := prepareServiceCall(runtimeHost, generation, params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	started, err := servicesManager().startServiceCall(runtimeHost.promptContext(), request)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]any{"callId": started.CallID, "provider": started.Provider}, nil
}

func decodeServiceCallID(params json.RawMessage) (string, *rpcError) {
	var request struct {
		CallID string `json:"callId"`
	}
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return "", rpcErr
	}
	if !serviceCallIDPattern.MatchString(request.CallID) {
		return "", hostUIParamsError("callId must be a service call id")
	}
	return request.CallID, nil
}

func hostServicesStatus(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	callID, rpcErr := decodeServiceCallID(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	snapshot, err := serviceCallStatus(runtimeHost.pluginID, callID)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return pluginServiceSnapshot(snapshot), nil
}

func hostServicesCancel(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	callID, rpcErr := decodeServiceCallID(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	snapshot, err := cancelServiceCall(runtimeHost.pluginID, callID)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return pluginServiceSnapshot(snapshot), nil
}

// ---- files ----

// decodeServiceChunk decodes base64 chunk data of at most chunkMaxBytes.
func decodeServiceChunk(value string) ([]byte, *rpcError) {
	if len(value) > base64.StdEncoding.EncodedLen(serviceChunkMaxBytes) {
		return nil, serviceRPCError(newContributionError(contributionErrTooLarge, "service file chunk exceeds %d bytes", serviceChunkMaxBytes))
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, hostUIParamsError("data must be base64")
	}
	return data, nil
}

func decodeServiceToken(params json.RawMessage, target any, token *string) *rpcError {
	if rpcErr := decodeHostUIParams(params, target); rpcErr != nil {
		return rpcErr
	}
	if !serviceTokenPattern.MatchString(*token) {
		return hostUIParamsError("token must be a service file token")
	}
	return nil
}

func hostServicesFileRead(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token  string `json:"token"`
		Offset int64  `json:"offset"`
		Length int    `json:"length"`
	}
	if rpcErr := decodeServiceToken(params, &request, &request.Token); rpcErr != nil {
		return nil, rpcErr
	}
	if request.Length == 0 {
		request.Length = serviceChunkMaxBytes
	}
	data, err := serviceFiles.read(runtimeHost.pluginID, request.Token, request.Offset, request.Length)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]any{"data": base64.StdEncoding.EncodeToString(data)}, nil
}

func hostServicesFileRelease(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token string `json:"token"`
	}
	if rpcErr := decodeServiceToken(params, &request, &request.Token); rpcErr != nil {
		return nil, rpcErr
	}
	serviceFiles.release(runtimeHost.pluginID, request.Token)
	return true, nil
}

// serviceSaveOwner owns the staged storage write of one file.save; it is
// never zero-sized, so every save has a distinct owner.
type serviceSaveOwner struct{ _ byte }

// hostServicesFileSave copies a file the caller can read into its own file
// storage through a staged storage write, so the storage path rules, the
// per-file limit and the quota apply exactly as for host.storage.file.write.
func hostServicesFileSave(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if rpcErr := decodeServiceToken(params, &request, &request.Token); rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := runtimeHost.capabilities[api.CapabilityStorage]; !ok {
		return nil, &rpcError{Code: -32001, Message: string(api.CapabilityStorage) + " capability not granted"}
	}
	if validPluginStoragePath(request.Path, false) != nil {
		return nil, hostUIParamsError("invalid storage path")
	}
	pluginID := runtimeHost.pluginID
	// Read the first chunk before touching storage: a token the caller
	// cannot read fails as not_found without leaving a staging file.
	chunk, err := serviceFiles.read(pluginID, request.Token, 0, pluginStorageMaxChunkBytes)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	owner := &serviceSaveOwner{}
	begun, err := pluginStorageBegin(owner, pluginID, request.Path)
	if err != nil {
		return nil, storageRPCError(err)
	}
	var offset int64
	for len(chunk) > 0 {
		if err = pluginStorageAppend(owner, pluginID, begun.Session, offset, chunk); err != nil {
			break
		}
		offset += int64(len(chunk))
		if chunk, err = serviceFiles.read(pluginID, request.Token, offset, pluginStorageMaxChunkBytes); err != nil {
			break
		}
	}
	if err == nil {
		err = pluginStorageCommit(owner, pluginID, begun.Session, offset)
	} else {
		_ = pluginStorageAbort(owner, pluginID, begun.Session)
	}
	if err != nil {
		var coded *contributionError
		if errors.As(err, &coded) {
			return nil, serviceRPCError(err)
		}
		return nil, storageRPCError(err)
	}
	return map[string]any{"path": request.Path, "size": offset}, nil
}

func hostServicesUploadBegin(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct{}
	if rpcErr := decodeServiceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	session, err := serviceFiles.uploadBegin(runtimeHost.pluginID)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]any{"session": session, "maxChunkBytes": serviceChunkMaxBytes}, nil
}

type serviceAppendRequest struct {
	Session string `json:"session"`
	Offset  int64  `json:"offset"`
	Data    string `json:"data"`
}

func decodeServiceAppend(params json.RawMessage) (serviceAppendRequest, []byte, *rpcError) {
	var request serviceAppendRequest
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return request, nil, rpcErr
	}
	if !serviceSessionPattern.MatchString(request.Session) {
		return request, nil, hostUIParamsError("session must be a service file session")
	}
	data, rpcErr := decodeServiceChunk(request.Data)
	return request, data, rpcErr
}

func hostServicesUploadAppend(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	request, data, rpcErr := decodeServiceAppend(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err := serviceFiles.uploadAppend(runtimeHost.pluginID, request.Session, request.Offset, data); err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]int64{"size": request.Offset + int64(len(data))}, nil
}

func hostServicesUploadCommit(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	if !serviceSessionPattern.MatchString(request.Session) {
		return nil, hostUIParamsError("session must be a service file session")
	}
	token, err := serviceFiles.uploadCommit(runtimeHost.pluginID, request.Session, request.Name)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	file, err := serviceFiles.uploadInfo(runtimeHost.pluginID, token)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return file, nil
}

// hostServicesCreateBegin opens an output file of a running call. Only the
// call's provider may: anyone else, or a call that ended, is not_found.
func hostServicesCreateBegin(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		CallID string `json:"callId"`
		Name   string `json:"name"`
	}
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	if !serviceCallIDPattern.MatchString(request.CallID) {
		return nil, hostUIParamsError("callId must be a service call id")
	}
	serviceCalls.mu.Lock()
	call := serviceCalls.calls[request.CallID]
	active := call != nil && call.providerID == runtimeHost.pluginID && !call.ending && call.snapshot.State == serviceCallRunning
	serviceCalls.mu.Unlock()
	if !active {
		return nil, serviceRPCError(exchangeCallNotFound())
	}
	session, err := serviceFiles.createBegin(request.CallID, runtimeHost.pluginID, request.Name)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]any{"session": session, "maxChunkBytes": serviceChunkMaxBytes}, nil
}

func hostServicesCreateAppend(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	request, data, rpcErr := decodeServiceAppend(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err := serviceFiles.createAppend(runtimeHost.pluginID, request.Session, request.Offset, data); err != nil {
		return nil, serviceRPCError(err)
	}
	return map[string]int64{"size": request.Offset + int64(len(data))}, nil
}

func hostServicesCreateCommit(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Session string `json:"session"`
	}
	if rpcErr := decodeHostUIParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	if !serviceSessionPattern.MatchString(request.Session) {
		return nil, hostUIParamsError("session must be a service file session")
	}
	file, err := serviceFiles.createCommit(runtimeHost.pluginID, request.Session)
	if err != nil {
		return nil, serviceRPCError(err)
	}
	return file, nil
}
