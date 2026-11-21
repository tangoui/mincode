package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"mincode/app/backend/core"
	backendsftp "mincode/app/backend/sftp"
	"mincode/app/plugins/api"
)

// Host-side service calls (design §7.2, §8.1, §9, §10, §12): the desktop UI
// lists services, starts calls from a surface (file tree, editor selection,
// command palette), follows them through plugin:service-call-changed or
// status, cancels them, saves output files to paths the user picked in a
// native save dialog and manages default providers.
//
// A host call is owned by serviceCallHostOwner. Its input files are
// absolute paths the user handed over (file tree selection or a file
// picker); the host snapshots them into the exchange and the provider only
// sees tokens. The newer surfaces hand over a remote file the host reads from
// an open SFTP / FTP session (sftpFile), content the frontend serialized and
// the host names (dbResult result.json, httpResponse response.<ext>) or a
// terminal selection (text), plus a surfaceContext the host trims to the
// chosen provider's capabilities (service_surface_context.go). Every surface
// hands over only what the user picked there, so none needs workspace.read;
// that the entry still offers the surface is checked before any prompt and again before dispatch.
// Confirmation follows the command palette: only a dangerous entry asks,
// unless the user trusts the provider's dangerous actions
// (dangerousActionsTrusted). When several providers fit and none is the
// default, the user picks one in the same serviceProvider prompt plugin
// callers get.

const (
	// serviceConfirmPromptKind confirms a dangerous host call; the reply is
	// "once" or cancelled.
	serviceConfirmPromptKind = "serviceConfirm"
	// serviceHostPromptName is the origin shown on host prompts.
	serviceHostPromptName = "MinCode"
	// serviceHostPromptTimeout bounds the host's provider choice and
	// confirmation together; the user started the call and is at the UI.
	serviceHostPromptTimeout = hostPromptMaxTimeout
)

func init() {
	core.Register("plugin_services_list", pluginServicesListHandler)
	core.Register("plugin_services_declared", pluginServicesDeclaredHandler)
	core.Register("plugin_services_start", pluginServicesStartHandler)
	core.Register("plugin_services_status", pluginServicesStatusHandler)
	core.Register("plugin_services_cancel", pluginServicesCancelHandler)
	core.Register("plugin_services_output_save", pluginServicesOutputSaveHandler)
	core.Register("plugin_services_release", pluginServicesReleaseHandler)
	core.Register("plugin_services_default_set", pluginServicesDefaultSetHandler)
}

// pluginServiceEntryView is one provider entry as the frontend's
// PluginServiceEntry (vue-app/src/domain/plugins/services.ts).
type pluginServiceEntryView struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	EntryID        string            `json:"entryId"`
	Service        string            `json:"service"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Description    string            `json:"description"`
	Input          ServiceShape      `json:"input"`
	Output         ServiceShape      `json:"output"`
	Params         map[string]any    `json:"params,omitempty"`
	Surfaces       []string          `json:"surfaces"`
	Risk           string            `json:"risk"`
	TimeoutMs      int               `json:"timeoutMs"`
	IsDefault      bool              `json:"isDefault"`
}

func newPluginServiceEntryView(provider serviceProvider) pluginServiceEntryView {
	entry := provider.Entry
	surfaces := entry.Surfaces
	if surfaces == nil {
		surfaces = []string{}
	}
	return pluginServiceEntryView{
		PluginID: provider.PluginID, PluginName: provider.PluginName, EntryID: entry.ID, Service: entry.Service, Title: entry.Title,
		TitleLocalized: entry.TitleLocalized, Description: entry.Description, Input: entry.Input, Output: entry.Output, Params: entry.Params, Surfaces: surfaces,
		Risk: entry.Risk, TimeoutMs: entry.TimeoutMs, IsDefault: provider.IsDefault,
	}
}

func pluginServicesListHandler(_, _ string) (string, error) {
	providers := servicesManager().serviceProviders("", "")
	views := make([]pluginServiceEntryView, 0, len(providers))
	for _, provider := range providers {
		views = append(views, newPluginServiceEntryView(provider))
	}
	return core.EncodeResult(map[string]any{"services": views})
}

// pluginServiceDeclaredView is one entry of an installed plugin's manifest
// for the plugin details (spec §12, §2b): active tells whether the entry is
// a live provider now; inactiveReason says why not.
type pluginServiceDeclaredView struct {
	pluginServiceEntryView
	Active         bool   `json:"active"`
	InactiveReason string `json:"inactiveReason,omitempty"`
}

// pluginServicesDeclaredHandler lists every service entry one installed
// plugin declares, whether or not it is enabled or active in this
// workspace. Unknown and data-only plugins have none.
func pluginServicesDeclaredHandler(_, argsJSON string) (string, error) {
	var args struct {
		PluginID string `json:"pluginId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	if !pluginIDPattern.MatchString(args.PluginID) {
		return "", newContributionError(contributionErrInvalidRequest, "pluginId must be a plugin id")
	}
	declared := servicesManager().declaredServices(args.PluginID)
	views := make([]pluginServiceDeclaredView, 0, len(declared))
	for _, service := range declared {
		views = append(views, pluginServiceDeclaredView{
			pluginServiceEntryView: newPluginServiceEntryView(service.serviceProvider),
			Active:                 service.inactiveReason == "", InactiveReason: service.inactiveReason,
		})
	}
	return core.EncodeResult(map[string]any{"services": views})
}

// hostServiceInput is what a host call hands over: text (a JSON string) or
// the base names of input files, from a surface.
type hostServiceInput struct {
	surface string
	text    json.RawMessage
	names   []string
}

// fits reports whether entry is offered on the surface and takes the input's
// shape and file types. Sizes are checked later so the user learns that an
// input is too large instead of finding no provider.
func (input hostServiceInput) fits(entry ServiceContribution) bool {
	if !slices.Contains(entry.Surfaces, input.surface) {
		return false
	}
	switch entry.Input.Kind {
	case serviceInputText:
		return input.text != nil && len(input.names) == 0
	case serviceInputFile, serviceInputFiles:
		maxFiles := 1
		if entry.Input.Kind == serviceInputFiles {
			maxFiles = entry.Input.MaxFiles
		}
		if input.text != nil || len(input.names) == 0 || len(input.names) > maxFiles {
			return false
		}
		for _, name := range input.names {
			if !serviceAccepts(entry.Input.Accepts, name) {
				return false
			}
		}
		return true
	}
	return input.text == nil && len(input.names) == 0
}

// checkHostServiceProviderLocked re-checks that the provider's entry still
// offers the user's surface. Caller holds record.mu.
func checkHostServiceProviderLocked(record *pluginRecord, entryID, surface string) error {
	index := slices.IndexFunc(servicesLocked(record), func(entry ServiceContribution) bool { return entry.ID == entryID })
	if index < 0 || !slices.Contains(servicesLocked(record)[index].Surfaces, surface) {
		return newContributionError(contributionErrForbidden, "service entry %q is not offered on %s", entryID, surface)
	}
	return nil
}

// hostServicePromptError turns a prompt failure into the error the frontend
// gets: a refusal or timeout is cancelled, another open prompt is busy.
func hostServicePromptError(rpcErr *rpcError) error {
	if rpcErrorCodeOf(rpcErr) == contributionErrBusy {
		return newContributionError(contributionErrBusy, "another service prompt is still open")
	}
	return newContributionError(contributionErrCancelled, "the user cancelled the service call")
}

func rpcErrorCodeOf(rpcErr *rpcError) string {
	if data, ok := rpcErr.Data.(map[string]string); ok {
		return data["code"]
	}
	return ""
}

// hostServiceProviderChooser asks the user to pick a provider for a host
// call before deadline. A prompt failure or refusal is stored in failure.
func hostServiceProviderChooser(deadline time.Time, failure *error) providerChooser {
	return func(ctx context.Context, service string, candidates []serviceProvider) (serviceProvider, bool, error) {
		chosen, remember, rpcErr := promptServiceProvider(ctx, serviceCallHostOwner, serviceHostPromptName, service, candidates, time.Until(deadline))
		if rpcErr != nil {
			*failure = hostServicePromptError(rpcErr)
			return serviceProvider{}, false, *failure
		}
		return chosen, remember, nil
	}
}

// confirmHostServiceCall applies the command palette's rule: a dangerous
// entry is confirmed unless the provider is fully trusted (full_trust.go) or,
// for records written by earlier versions, its dangerous actions are trusted
// for its installed version; other risks run without a prompt. While the app
// is locked neither applies. Every skip is audited on the provider.
func confirmHostServiceCall(ctx context.Context, manager *pluginManager, provider serviceProvider, preview string, timeout time.Duration) error {
	entry := provider.Entry
	if entry.Risk != "dangerous" {
		return nil
	}
	audit := FullTrustAuditEntry{Surface: "hostService", Risk: entry.Risk, Subject: entry.Service}
	if manager.fullTrustSkip(provider.PluginID, audit) {
		return nil
	}
	trusted, err := manager.dangerousActionsTrusted(provider.PluginID)
	if err != nil {
		return newContributionError(contributionErrNotFound, "plugin %q is not installed", provider.PluginID)
	}
	if trusted && !appLocked() {
		audit.Kind, audit.Reason = fullTrustAuditSkipped, "legacy"
		if err := appendFullTrustAudit(provider.PluginID, audit); err != nil {
			log.Printf("plugin %s: full trust audit: %v", provider.PluginID, err)
		}
		return nil
	}
	name := cleanHostText(provider.PluginName, hostUITitleMaxRunes, false)
	event := promptEvent{Kind: serviceConfirmPromptKind, PluginID: serviceCallHostOwner, PluginName: serviceHostPromptName,
		Title: cleanHostText(entry.Title, hostUITitleMaxRunes, false), Items: []quickPickItem{{ID: "once", Label: "once"}},
		Call: &pluginCallDetails{TargetPluginID: provider.PluginID, TargetName: name, Action: entry.Action,
			Description: cleanHostText(entry.Description, hostUITextMaxRunes, false), Risk: entry.Risk, Payload: preview,
			Service: entry.Service, ServiceTitle: cleanHostText(entry.Title, hostUITitleMaxRunes, false)}}
	reply, rpcErr := hostPrompts.ask(ctx, event, &pendingPrompt{pluginID: serviceCallHostOwner, kind: serviceConfirmPromptKind, items: map[string]bool{"once": true}}, timeout)
	if rpcErr == nil && reply.Cancelled {
		rpcErr = pluginCallDeclined()
	}
	if rpcErr != nil {
		return hostServicePromptError(rpcErr)
	}
	return nil
}

type pluginServicesStartArgs struct {
	Service  string              `json:"service"`
	Provider *serviceProviderRef `json:"provider"`
	Surface  string              `json:"surface"`
	Input    *string             `json:"input"`
	Paths    []string            `json:"paths"`
	Params   map[string]any      `json:"params"`
	// RemoteFile names a file in an SFTP / FTP session the user has open
	// (sftpFile only); the host reads it, the provider gets a snapshot.
	RemoteFile *pluginServicesRemoteFile `json:"remoteFile"`
	// Content is host-serialized content (dbResult, httpResponse).
	Content *pluginServicesContent `json:"content"`
	// SurfaceContext is the surface's extra context (new surfaces only).
	SurfaceContext json.RawMessage `json:"surfaceContext"`
}

type pluginServicesRemoteFile struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

type pluginServicesContent struct {
	Text        string `json:"text"`
	ContentType string `json:"contentType"`
}

const (
	// serviceRemoteSessionIDMaxBytes and serviceRemotePathMaxBytes bound a
	// remoteFile reference (a session ID the frontend generated, a remote
	// absolute path).
	serviceRemoteSessionIDMaxBytes = 256
	serviceRemotePathMaxBytes      = 4096
)

var (
	// serviceOpenRemoteFile opens a file of an open SFTP / FTP session,
	// waiting for the session while ctx lasts (test seam).
	serviceOpenRemoteFile = backendsftp.OpenSessionFileContext
	// serviceRemoteInputStall fails a remote read that delivers no data for
	// this long.
	serviceRemoteInputStall       = time.Duration(serviceSpec.Limit("remoteInputStallMs")) * time.Millisecond
	serviceSurfaceContextMaxBytes = int(serviceSpec.Limit("surfaceContextMaxBytes"))
)

// hostServiceSourceKind is where a host call's input comes from.
type hostServiceSourceKind int

const (
	hostSourceNone hostServiceSourceKind = iota
	hostSourceInput
	hostSourcePaths
	hostSourceRemoteFile
	hostSourceContent
)

// checkHostServiceSources enforces that each surface takes one kind of
// source: sftpFile a remoteFile, dbResult and httpResponse content,
// terminalSelection text input; the older surfaces text or paths (or nothing
// on the palette) and never the newer sources. Only the newer surfaces carry
// a surfaceContext, a JSON object of at most surfaceContextMaxBytes.
func checkHostServiceSources(args pluginServicesStartArgs) error {
	var given []hostServiceSourceKind
	if args.Input != nil {
		given = append(given, hostSourceInput)
	}
	if len(args.Paths) > 0 {
		given = append(given, hostSourcePaths)
	}
	if args.RemoteFile != nil {
		given = append(given, hostSourceRemoteFile)
	}
	if args.Content != nil {
		given = append(given, hostSourceContent)
	}
	if len(given) > 1 {
		return newContributionError(contributionErrInvalidRequest, "a service call takes one kind of input")
	}
	source := hostSourceNone
	if len(given) == 1 {
		source = given[0]
	}
	var want hostServiceSourceKind
	newSurface := true
	switch args.Surface {
	case serviceSurfaceSFTPFile:
		want = hostSourceRemoteFile
	case serviceSurfaceDBResult, serviceSurfaceHTTPResponse:
		want = hostSourceContent
	case serviceSurfaceTerminalSelection:
		want = hostSourceInput
	default:
		newSurface = false
		if source == hostSourceRemoteFile || source == hostSourceContent {
			return newContributionError(contributionErrInvalidRequest, "surface %s takes text or files", args.Surface)
		}
		want = source
	}
	if source != want {
		return newContributionError(contributionErrInvalidRequest, "surface %s takes a different kind of input", args.Surface)
	}
	trimmed := bytes.TrimSpace(args.SurfaceContext)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if !newSurface {
		return newContributionError(contributionErrInvalidRequest, "surface %s takes no surfaceContext", args.Surface)
	}
	if len(trimmed) > serviceSurfaceContextMaxBytes || trimmed[0] != '{' {
		return newContributionError(contributionErrInvalidRequest, "surfaceContext must be an object of at most %d bytes", serviceSurfaceContextMaxBytes)
	}
	return nil
}

// hostServiceRemoteName checks a remoteFile reference and returns the
// display name of its file (the remote base name, sanitized). The session
// and path never reach the provider.
func hostServiceRemoteName(remote pluginServicesRemoteFile) (string, error) {
	invalid := newContributionError(contributionErrInvalidRequest, "remoteFile must be {sessionId, path} with an absolute remote path")
	if remote.SessionID == "" || len(remote.SessionID) > serviceRemoteSessionIDMaxBytes || strings.ContainsFunc(remote.SessionID, unicode.IsControl) {
		return "", invalid
	}
	if remote.Path == "" || len(remote.Path) > serviceRemotePathMaxBytes || !strings.HasPrefix(remote.Path, "/") || strings.ContainsAny(remote.Path, "\x00\r\n") {
		return "", invalid
	}
	base := path.Base(path.Clean(remote.Path))
	if base == "/" || base == "." || base == ".." {
		return "", invalid
	}
	return sanitizeExchangeName(base), nil
}

// hostServiceRemoteOpenError turns an OpenSessionFile failure into a coded
// error with a stable English message (the frontend localizes by code);
// none of them names the host, a local or a remote path.
func hostServiceRemoteOpenError(err error) error {
	switch {
	case errors.Is(err, backendsftp.ErrSessionUnavailable):
		return newContributionError(contributionErrUnavailable, "the remote session is disconnected")
	case errors.Is(err, backendsftp.ErrRemotePathInvalid):
		return newContributionError(contributionErrInvalidRequest, "invalid remote file path")
	case errors.Is(err, backendsftp.ErrRemoteFileNotFound):
		return newContributionError(contributionErrNotFound, "the remote file is not available")
	case errors.Is(err, backendsftp.ErrRemoteNotRegular):
		return newContributionError(contributionErrInvalidRequest, "the remote entry is not a regular file (directories and symbolic links are not supported)")
	}
	return newContributionError(contributionErrUnavailable, "the remote file could not be opened")
}

// remoteServiceInput is a remote file to read into a call's exchange during
// the call's input phase. Nothing is opened before the call starts: waiting
// for a busy session (an FTP session is exclusive and a transfer holds it
// throughout) happens inside the input phase, where the call is visible,
// has an ID and can be cancelled.
type remoteServiceInput struct {
	sessionID, path, name string
	maxBytes              int64
}

// remoteReadError turns a remote read failure into a coded error with a
// stable English message (the frontend localizes by code).
func remoteReadError(err error) error {
	if errors.Is(err, backendsftp.ErrSessionUnavailable) {
		return newContributionError(contributionErrUnavailable, "the remote session is disconnected")
	}
	return newContributionError(contributionErrUnavailable, "reading the remote file failed")
}

// remoteReader maps the session file's read errors (io.EOF passes).
type remoteReader struct{ reader io.Reader }

func (r remoteReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, remoteReadError(err)
	}
	return n, err
}

// prepare opens the remote file (waiting for its session if needed),
// refuses it with too_large before reading when it exceeds the provider's
// maxBytes, then reads it into the call's exchange.
//
// The size is the one Lstat reported when the file was opened: a file that
// grows while it is read is cut at that size (snapshotReader reads at most
// size bytes); one that shrinks yields what was there.
//
// ctx (the call's cancellation) ends the wait or the read as cancelled. A
// read that delivers no data for serviceRemoteInputStall fails with timeout;
// snapshotReader reads in serviceSnapshotChunkBytes (32 KiB) pieces, so the
// stall means "no data for that long", not "a whole large chunk took that
// long". Waiting for a busy session does not count as a stall. Every failure
// removes the partial snapshot and returns the reservation; the session is
// released in the background because the release waits for a Read that may
// still be blocked on the network.
func (input *remoteServiceInput) prepare(ctx context.Context, callID, provider string, report func(done, total int64)) ([]exchangeFile, error) {
	reader, size, release, err := serviceOpenRemoteFile(ctx, input.sessionID, input.path)
	if err != nil {
		if ctxErr := snapshotContextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, hostServiceRemoteOpenError(err)
	}
	defer func() { go release() }()
	if size > input.maxBytes {
		return nil, newContributionError(contributionErrTooLarge, "file %q is %d bytes, the service accepts %d", input.name, size, input.maxBytes)
	}
	stallCtx, stall := context.WithCancelCause(ctx)
	defer stall(nil)
	stallSeconds := serviceRemoteInputStall.Seconds()
	timer := time.AfterFunc(serviceRemoteInputStall, func() {
		stall(newContributionError(contributionErrTimeout, "the remote file sent no data for %g s", stallSeconds))
	})
	defer timer.Stop()
	report(0, size)
	file, err := serviceFiles.snapshotReader(stallCtx, callID, provider, input.name, remoteReader{reader}, size, func(done int64) {
		timer.Reset(serviceRemoteInputStall)
		report(done, size)
	})
	if err != nil {
		return nil, err
	}
	return []exchangeFile{file}, nil
}

// hostServiceSources checks the user's files: absolute paths of regular
// files, at most filesMax. The paths never leave the host.
func hostServiceSources(paths []string) ([]resolvedServiceSource, error) {
	if len(paths) > serviceFilesMax {
		return nil, newContributionError(contributionErrInvalidRequest, "a service call takes at most %d files", serviceFilesMax)
	}
	sources := make([]resolvedServiceSource, 0, len(paths))
	for _, path := range paths {
		if path == "" || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
			return nil, newContributionError(contributionErrInvalidRequest, "service input files must be absolute paths")
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return nil, newContributionError(contributionErrNotFound, "service input file %q is not available", filepath.Base(path))
		}
		if !info.Mode().IsRegular() {
			return nil, newContributionError(contributionErrInvalidRequest, "service input %q is not a regular file", filepath.Base(path))
		}
		sources = append(sources, resolvedServiceSource{absPath: path, name: sanitizeExchangeName(filepath.Base(path)), size: info.Size()})
	}
	return sources, nil
}

func pluginServicesStartHandler(_, argsJSON string) (string, error) {
	var args pluginServicesStartArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	snapshot, err := startHostServiceCall(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(snapshot)
}

func startHostServiceCall(args pluginServicesStartArgs) (ServiceCallSnapshot, error) {
	if validateServiceName(args.Service) != nil {
		return ServiceCallSnapshot{}, newContributionError(contributionErrInvalidRequest, "service must be a service name")
	}
	if !slices.Contains(serviceSurfaces, args.Surface) {
		return ServiceCallSnapshot{}, newContributionError(contributionErrInvalidRequest, "unknown surface %q", args.Surface)
	}
	if args.Provider != nil && (!pluginIDPattern.MatchString(args.Provider.PluginID) ||
		args.Provider.EntryID != "" && !serviceIDPattern.MatchString(args.Provider.EntryID)) {
		return ServiceCallSnapshot{}, newContributionError(contributionErrInvalidRequest, "provider must be {pluginId, entryId?}")
	}
	if args.Input != nil && len(args.Paths) > 0 {
		return ServiceCallSnapshot{}, newContributionError(contributionErrInvalidRequest, "a service call takes text or files, not both")
	}
	if err := checkHostServiceSources(args); err != nil {
		return ServiceCallSnapshot{}, err
	}
	// The context is checked in full before any prompt; what the chosen
	// provider may see of it is decided once it is chosen.
	if _, err := gateSurfaceContext(args.Surface, args.SurfaceContext, surfaceContextCapabilities(args.Surface)); err != nil {
		return ServiceCallSnapshot{}, err
	}
	sources, err := hostServiceSources(args.Paths)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	input := hostServiceInput{surface: args.Surface}
	if args.Input != nil {
		input.text, _ = json.Marshal(*args.Input)
	}
	for _, source := range sources {
		input.names = append(input.names, source.name)
	}
	remoteName := ""
	if args.RemoteFile != nil {
		if remoteName, err = hostServiceRemoteName(*args.RemoteFile); err != nil {
			return ServiceCallSnapshot{}, err
		}
		input.names = append(input.names, remoteName)
	}
	contentName := ""
	if args.Content != nil {
		if contentName, err = hostServiceContentName(args.Surface, *args.Content); err != nil {
			return ServiceCallSnapshot{}, err
		}
		input.names = append(input.names, contentName)
	}

	manager := servicesManager()
	deadline := time.Now().Add(serviceHostPromptTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var failure error
	provider, err := manager.selectServiceProviderWhere(ctx, args.Service, args.Provider, "",
		func(candidate serviceProvider) bool { return input.fits(candidate.Entry) }, hostServiceProviderChooser(deadline, &failure))
	if failure != nil {
		return ServiceCallSnapshot{}, failure
	}
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	entry := provider.Entry
	for _, source := range sources {
		if source.size > entry.Input.MaxBytes {
			return ServiceCallSnapshot{}, newContributionError(contributionErrTooLarge, "file %q is %d bytes, the service accepts %d", source.name, source.size, entry.Input.MaxBytes)
		}
	}
	record, err := manager.record(provider.PluginID)
	if err != nil {
		return ServiceCallSnapshot{}, newContributionError(contributionErrNotFound, "plugin %q is not installed", provider.PluginID)
	}
	if args.Content != nil && int64(len(args.Content.Text)) > entry.Input.MaxBytes {
		return ServiceCallSnapshot{}, newContributionError(contributionErrTooLarge, "file %q is %d bytes, the service accepts %d", contentName, len(args.Content.Text), entry.Input.MaxBytes)
	}
	record.mu.Lock()
	err = checkHostServiceProviderLocked(record, entry.ID, args.Surface)
	var capabilities []api.Capability
	if record.manifest != nil {
		capabilities = slices.Clone(record.manifest.Capabilities)
	}
	record.mu.Unlock()
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	// Trimmed for this provider: identifiers it lacks the capability for
	// never reach its envelope. The capabilities it was given them for are
	// re-checked before dispatch.
	surfaceContext, err := gateSurfaceContext(args.Surface, args.SurfaceContext, capabilities)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	var relied []api.Capability
	for _, capability := range surfaceContextCapabilities(args.Surface) {
		if slices.Contains(capabilities, capability) {
			relied = append(relied, capability)
		}
	}
	preview := serviceInputPreview(input.text, sources)
	switch {
	case remoteName != "":
		preview = payloadPreview([]byte(remoteName))
	case args.Content != nil:
		preview = serviceInputPreview(nil, []resolvedServiceSource{{name: contentName, size: int64(len(args.Content.Text))}})
	}
	if err := confirmHostServiceCall(ctx, manager, provider, preview, time.Until(deadline)); err != nil {
		return ServiceCallSnapshot{}, err
	}

	surface := args.Surface
	request := serviceCallRequest{
		Caller:         serviceCaller{Kind: "host", Surface: surface},
		Provider:       provider,
		Input:          input.text,
		Params:         args.Params,
		SurfaceContext: surfaceContext,
		PrepareInputs: func(callID string) ([]exchangeFile, error) {
			if args.Content != nil {
				file, err := serviceFiles.snapshotBytes(callID, provider.PluginID, contentName, []byte(args.Content.Text))
				if err != nil {
					return nil, err
				}
				return []exchangeFile{file}, nil
			}
			files := make([]exchangeFile, 0, len(sources))
			for _, source := range sources {
				file, err := serviceFiles.snapshotPath(callID, provider.PluginID, source.absPath)
				if err != nil {
					return nil, err
				}
				files = append(files, file)
			}
			return files, nil
		},
		Revalidate: func(record *pluginRecord) error {
			if err := checkHostServiceProviderLocked(record, entry.ID, surface); err != nil {
				return err
			}
			for _, capability := range relied {
				if record.manifest == nil || !slices.Contains(record.manifest.Capabilities, capability) {
					return newContributionError(contributionErrForbidden, "plugin %q changed its capabilities since the call was approved; call it again", provider.PluginID)
				}
			}
			return nil
		},
	}
	if args.RemoteFile == nil {
		return manager.startServiceCall(context.Background(), request)
	}
	// The call starts at once in phase "input"; the remote file is opened
	// there (a busy session is waited for, cancellably) and checked against
	// the provider's limit before a single byte is read, so a missing,
	// unreadable or too large file ends the call failed rather than failing
	// the start.
	remote := &remoteServiceInput{sessionID: args.RemoteFile.SessionID, path: args.RemoteFile.Path, name: remoteName, maxBytes: entry.Input.MaxBytes}
	request.PrepareInputs = nil
	request.PrepareInputsAsync = func(ctx context.Context, callID string, report func(done, total int64)) ([]exchangeFile, error) {
		return remote.prepare(ctx, callID, provider.PluginID, report)
	}
	return manager.startServiceCall(context.Background(), request)
}

type pluginServicesCallArgs struct {
	CallID string `json:"callId"`
	Token  string `json:"token"`
	Path   string `json:"path"`
}

func decodeHostServiceCall(argsJSON string) (pluginServicesCallArgs, error) {
	var args pluginServicesCallArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return args, newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	if !serviceCallIDPattern.MatchString(args.CallID) {
		return args, newContributionError(contributionErrInvalidRequest, "callId must be a service call id")
	}
	return args, nil
}

func pluginServicesStatusHandler(_, argsJSON string) (string, error) {
	args, err := decodeHostServiceCall(argsJSON)
	if err != nil {
		return "", err
	}
	snapshot, err := serviceCallStatus(serviceCallHostOwner, args.CallID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(snapshot)
}

func pluginServicesCancelHandler(_, argsJSON string) (string, error) {
	args, err := decodeHostServiceCall(argsJSON)
	if err != nil {
		return "", err
	}
	snapshot, err := cancelServiceCall(serviceCallHostOwner, args.CallID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(snapshot)
}

// pluginServicesOutputSaveHandler writes one output file of a host call to
// the path the user chose in a native save dialog.
func pluginServicesOutputSaveHandler(_, argsJSON string) (string, error) {
	args, err := decodeHostServiceCall(argsJSON)
	if err != nil {
		return "", err
	}
	if !serviceTokenPattern.MatchString(args.Token) {
		return "", newContributionError(contributionErrInvalidRequest, "token must be a service file token")
	}
	if args.Path == "" || strings.ContainsRune(args.Path, 0) || !filepath.IsAbs(args.Path) {
		return "", newContributionError(contributionErrInvalidRequest, "path must be an absolute path")
	}
	if !serviceFiles.callFile(serviceCallHostOwner, args.CallID, args.Token) {
		return "", exchangeNotFound()
	}
	if err := serviceFiles.copyOut(serviceCallHostOwner, args.Token, filepath.Clean(args.Path)); err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{})
}

// pluginServicesReleaseHandler deletes a host call's output files early.
func pluginServicesReleaseHandler(_, argsJSON string) (string, error) {
	args, err := decodeHostServiceCall(argsJSON)
	if err != nil {
		return "", err
	}
	serviceFiles.releaseCall(serviceCallHostOwner, args.CallID)
	return core.EncodeResult(map[string]any{})
}

// pluginServicesDefaultSetHandler makes pluginId, a live provider of the
// service, its default; an empty pluginId clears the default.
func pluginServicesDefaultSetHandler(_, argsJSON string) (string, error) {
	var args struct {
		Service  string `json:"service"`
		PluginID string `json:"pluginId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	if validateServiceName(args.Service) != nil {
		return "", newContributionError(contributionErrInvalidRequest, "service must be a service name")
	}
	if args.PluginID != "" {
		if !pluginIDPattern.MatchString(args.PluginID) {
			return "", newContributionError(contributionErrInvalidRequest, "pluginId must be a plugin id")
		}
		providers := servicesManager().serviceProviders(args.Service, "")
		if !slices.ContainsFunc(providers, func(provider serviceProvider) bool { return provider.PluginID == args.PluginID }) {
			return "", newContributionError(contributionErrNotFound, "plugin %q does not provide service %q", args.PluginID, args.Service)
		}
	}
	if err := setServiceDefault(args.Service, args.PluginID); err != nil {
		return "", newContributionError(contributionErrUnavailable, "could not save the default provider: %v", err)
	}
	return core.EncodeResult(map[string]any{"defaults": readServiceDefaults()})
}
