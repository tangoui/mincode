package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Services (contributes.services) let a process plugin offer a named
// capability ("text.translate", "image.ocr") that other plugins and MinCode
// itself can call through a fixed input/output shape. The host treats the
// service name as an opaque key and only understands the shapes. Each entry is
// backed by one of the plugin's contributes.actions, whose risk becomes the
// service's risk. Mirrors: tools/mincode-cli/services.go (CLI),
// assertServicesEntry (cloud) and vue-app/src/domain/plugins/services.ts
// (frontend revalidation); the MIME table in service_mime.go is mirrored in
// services.ts too.
const (
	servicePointKey        = "services"
	serviceIDPatternSource = "^[a-z0-9][a-z0-9-]{0,31}$"
	serviceNamePattern     = "^[a-z][a-z0-9-]*(\\.[a-z][a-z0-9-]*){1,3}$"

	serviceKindNone         = "none"
	serviceInputText        = "text"
	serviceInputFile        = "file"
	serviceInputFiles       = "files"
	serviceOutputJSON       = "json"
	serviceSurfaceFileMenu  = "fileMenu"
	serviceSurfaceSelection = "selection"
	serviceSurfacePalette   = "palette"
	// Host 0.3.0 surfaces: content the user picked in the SFTP file list, a
	// database result grid, an HTTP response or a terminal selection. They
	// are never part of the default and must be declared explicitly.
	serviceSurfaceSFTPFile          = "sftpFile"
	serviceSurfaceDBResult          = "dbResult"
	serviceSurfaceHTTPResponse      = "httpResponse"
	serviceSurfaceTerminalSelection = "terminalSelection"

	// serviceDBResultFileName is the single file the dbResult surface hands
	// over (application/json); an entry offered there must accept it.
	serviceDBResultFileName = "result.json"
)

var (
	serviceSpec                 = mustContributionSpec(servicePointKey)
	serviceTitleMaxRunes        = int(serviceSpec.Limit("titleMaxRunes"))
	serviceDescriptionMaxRunes  = int(serviceSpec.Limit("descriptionMaxRunes"))
	serviceLocalesMax           = int(serviceSpec.Limit("localesMax"))
	serviceNameMaxLength        = int(serviceSpec.Limit("serviceMaxLength"))
	serviceAcceptsMax           = int(serviceSpec.Limit("acceptsMax"))
	serviceTextDefaultBytes     = serviceSpec.Limit("textDefaultBytes")
	serviceTextMaxBytes         = serviceSpec.Limit("textMaxBytes")
	serviceEnvelopeReserveBytes = serviceSpec.Limit("envelopeReserveBytes")
	serviceFileMaxBytes         = serviceSpec.Limit("fileMaxBytes")
	serviceFilesDefault         = int(serviceSpec.Limit("filesDefault"))
	serviceFilesMax             = int(serviceSpec.Limit("filesMax"))
	serviceParamsMaxBytes       = int(serviceSpec.Limit("paramsMaxBytes"))
	serviceParamsMaxProperties  = int(serviceSpec.Limit("paramsMaxProperties"))
	serviceTimeoutDefaultMs     = int(serviceSpec.Limit("timeoutDefaultMs"))
	serviceTimeoutMaxMs         = int(serviceSpec.Limit("timeoutMaxMs"))
	serviceInputKinds           = serviceSpec.List("inputKinds")
	serviceOutputKinds          = serviceSpec.List("outputKinds")
	serviceSurfaces             = serviceSpec.List("surfaces")
	serviceReservedPrefixes     = serviceSpec.List("reservedPrefixes")

	serviceIDPattern  = regexp.MustCompile(serviceIDPatternSource)
	serviceNameRegexp = regexp.MustCompile(serviceNamePattern)
	serviceAcceptExt  = regexp.MustCompile(`^\.[a-z0-9][a-z0-9_+-]*(\.[a-z0-9][a-z0-9_+-]*)?$`)
	serviceAcceptMime = regexp.MustCompile(`^[a-z][a-z0-9.+-]*/(\*|[a-z0-9][a-z0-9.+-]*)$`)
	serviceOutputMime = regexp.MustCompile(`^[a-z][a-z0-9.+-]*/[a-z0-9][a-z0-9.+-]*$`)

	// serviceCompatibleSurfaces lists the surfaces that can supply each input
	// kind: the editor and terminal selections supply text; the file menu,
	// SFTP file list, database result grid and HTTP response supply a file;
	// the palette can prompt for anything.
	serviceCompatibleSurfaces = map[string][]string{
		serviceKindNone:   {serviceSurfacePalette},
		serviceInputText:  {serviceSurfaceSelection, serviceSurfacePalette, serviceSurfaceTerminalSelection},
		serviceInputFile:  {serviceSurfaceFileMenu, serviceSurfacePalette, serviceSurfaceSFTPFile, serviceSurfaceDBResult, serviceSurfaceHTTPResponse},
		serviceInputFiles: {serviceSurfaceFileMenu, serviceSurfacePalette, serviceSurfaceSFTPFile, serviceSurfaceDBResult, serviceSurfaceHTTPResponse},
	}

	// serviceDefaultSurfaces are the surfaces an entry that omits surfaces may
	// get (those compatible with its input kind). Later surfaces stay opt-in so
	// installed plugins do not appear somewhere their authors never planned.
	serviceDefaultSurfaces = []string{serviceSurfaceFileMenu, serviceSurfaceSelection, serviceSurfacePalette}
)

type serviceManifestInput struct {
	Kind     string   `json:"kind"`
	Accepts  []string `json:"accepts,omitempty"`
	MaxBytes *int64   `json:"maxBytes,omitempty"`
	MaxFiles *int     `json:"maxFiles,omitempty"`
}

type serviceManifestOutput struct {
	Kind string `json:"kind"`
	Mime string `json:"mime,omitempty"`
}

type serviceManifestEntry struct {
	ID             string                `json:"id"`
	Service        string                `json:"service"`
	Action         string                `json:"action"`
	Title          string                `json:"title"`
	TitleLocalized map[string]string     `json:"titleLocalized,omitempty"`
	Description    string                `json:"description,omitempty"`
	Input          serviceManifestInput  `json:"input"`
	Output         serviceManifestOutput `json:"output"`
	Params         map[string]any        `json:"params,omitempty"`
	Surfaces       *[]string             `json:"surfaces,omitempty"`
	TimeoutMs      *int                  `json:"timeoutMs,omitempty"`
}

// ServiceShape is a normalized input or output shape. Accepts, MaxBytes and
// MaxFiles describe input only; Mime describes output only.
type ServiceShape struct {
	Kind     string   `json:"kind"`
	Accepts  []string `json:"accepts,omitempty"`
	MaxBytes int64    `json:"maxBytes,omitempty"`
	MaxFiles int      `json:"maxFiles,omitempty"`
	Mime     string   `json:"mime,omitempty"`
}

// ServiceContribution is the snapshot entry. Risk is the backing action's
// risk; Surfaces is never nil (empty means plugin callers only).
type ServiceContribution struct {
	ID             string            `json:"id"`
	Service        string            `json:"service"`
	Action         string            `json:"action"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Description    string            `json:"description,omitempty"`
	Input          ServiceShape      `json:"input"`
	Output         ServiceShape      `json:"output"`
	Params         map[string]any    `json:"params,omitempty"`
	Surfaces       []string          `json:"surfaces"`
	TimeoutMs      int               `json:"timeoutMs"`
	Risk           string            `json:"risk"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              servicePointKey,
		Decode:           decodeServiceEntry,
		Finalize:         finalizeServiceEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate service id %q", id) },
		IssuePrefix:      "invalid services contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped service %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.services[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "services",
		ExpandsPermission: servicesExpandPermission,
	})
}

func decodeServiceEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry serviceManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse service: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !serviceIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("service id %q must match %s", id, serviceIDPatternSource)
	}
	if ctx.manifest.Runtime.Type != "process" {
		return nil, id, fmt.Errorf("service %q requires runtime.type process", id)
	}
	service := ServiceContribution{ID: id, Service: entry.Service, Action: strings.TrimSpace(entry.Action)}
	if err := validateServiceName(service.Service); err != nil {
		return nil, id, fmt.Errorf("entry %q %w", id, err)
	}
	var err error
	if service.Title, err = validateContributionLine(entry.Title, serviceTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("service %q title %w", id, err)
	}
	if service.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, serviceTitleMaxRunes, serviceLocalesMax); err != nil {
		return nil, id, fmt.Errorf("service %q titleLocalized %w", id, err)
	}
	if service.Description, err = validateContributionLine(entry.Description, serviceDescriptionMaxRunes, false); err != nil {
		return nil, id, fmt.Errorf("service %q description %w", id, err)
	}
	action, ok := ctx.actions[service.Action]
	if !ok {
		return nil, id, fmt.Errorf("service %q action %q is not one of this plugin's contributes.actions", id, service.Action)
	}
	if action.AICallable {
		return nil, id, fmt.Errorf("service %q action %q must not be aiCallable (declare a separate action for AI)", id, service.Action)
	}
	service.Risk = action.Risk
	if service.Input, err = decodeServiceInput(entry.Input); err != nil {
		return nil, id, fmt.Errorf("service %q input %w", id, err)
	}
	if service.Input.Kind == serviceInputText {
		if need := service.Input.MaxBytes + serviceEnvelopeReserveBytes; int64(action.MaxPayloadBytes) < need {
			return nil, id, fmt.Errorf("service %q action %q needs maxPayloadBytes of at least %d for input.maxBytes %d", id, service.Action, need, service.Input.MaxBytes)
		}
	}
	if service.Output, err = decodeServiceOutput(entry.Output); err != nil {
		return nil, id, fmt.Errorf("service %q output %w", id, err)
	}
	if entry.Params != nil {
		if err := validateServiceParams(entry.Params); err != nil {
			return nil, id, fmt.Errorf("service %q params: %w", id, err)
		}
		service.Params = entry.Params
	}
	if service.Surfaces, err = resolveServiceSurfaces(entry.Surfaces, service.Input.Kind); err != nil {
		return nil, id, fmt.Errorf("service %q %w", id, err)
	}
	if slices.Contains(service.Surfaces, serviceSurfaceDBResult) && !serviceAccepts(service.Input.Accepts, serviceDBResultFileName) {
		return nil, id, fmt.Errorf("service %q surface %q hands over %s (application/json); accepts must include .json, application/json or application/*", id, serviceSurfaceDBResult, serviceDBResultFileName)
	}
	service.TimeoutMs = serviceTimeoutDefaultMs
	if entry.TimeoutMs != nil {
		if *entry.TimeoutMs < 1 || *entry.TimeoutMs > serviceTimeoutMaxMs {
			return nil, id, fmt.Errorf("service %q timeoutMs must be 1-%d", id, serviceTimeoutMaxMs)
		}
		service.TimeoutMs = *entry.TimeoutMs
	}
	return service, id, nil
}

func validateServiceName(name string) error {
	if len(name) > serviceNameMaxLength {
		return fmt.Errorf("service %q must be at most %d characters", name, serviceNameMaxLength)
	}
	if !serviceNameRegexp.MatchString(name) {
		return fmt.Errorf("service %q must match %s", name, serviceNamePattern)
	}
	first, _, _ := strings.Cut(name, ".")
	if slices.Contains(serviceReservedPrefixes, first) {
		return fmt.Errorf("service %q uses the reserved prefix %q", name, first+".")
	}
	return nil
}

func decodeServiceInput(input serviceManifestInput) (ServiceShape, error) {
	shape := ServiceShape{Kind: input.Kind}
	if !slices.Contains(serviceInputKinds, input.Kind) {
		return shape, fmt.Errorf("kind %q must be one of %s", input.Kind, strings.Join(serviceInputKinds, ", "))
	}
	isFile := input.Kind == serviceInputFile || input.Kind == serviceInputFiles
	if input.Accepts != nil && !isFile {
		return shape, fmt.Errorf("accepts is only valid for kind file or files")
	}
	if input.MaxBytes != nil && input.Kind == serviceKindNone {
		return shape, fmt.Errorf("maxBytes is not valid for kind none")
	}
	if input.MaxFiles != nil && input.Kind != serviceInputFiles {
		return shape, fmt.Errorf("maxFiles is only valid for kind files")
	}
	switch input.Kind {
	case serviceInputText:
		shape.MaxBytes = serviceTextDefaultBytes
		if input.MaxBytes != nil {
			if *input.MaxBytes < 1 || *input.MaxBytes > serviceTextMaxBytes {
				return shape, fmt.Errorf("maxBytes must be 1-%d for kind text", serviceTextMaxBytes)
			}
			shape.MaxBytes = *input.MaxBytes
		}
	case serviceInputFile, serviceInputFiles:
		accepts, err := validateServiceAccepts(input.Accepts)
		if err != nil {
			return shape, err
		}
		shape.Accepts = accepts
		shape.MaxBytes = serviceFileMaxBytes
		if input.MaxBytes != nil {
			if *input.MaxBytes < 1 || *input.MaxBytes > serviceFileMaxBytes {
				return shape, fmt.Errorf("maxBytes must be 1-%d for kind %s", serviceFileMaxBytes, input.Kind)
			}
			shape.MaxBytes = *input.MaxBytes
		}
		if input.Kind == serviceInputFiles {
			shape.MaxFiles = serviceFilesDefault
			if input.MaxFiles != nil {
				if *input.MaxFiles < 1 || *input.MaxFiles > serviceFilesMax {
					return shape, fmt.Errorf("maxFiles must be 1-%d", serviceFilesMax)
				}
				shape.MaxFiles = *input.MaxFiles
			}
		}
	}
	return shape, nil
}

func validateServiceAccepts(accepts []string) ([]string, error) {
	if len(accepts) == 0 {
		return nil, fmt.Errorf("accepts is required for file input")
	}
	if len(accepts) > serviceAcceptsMax {
		return nil, fmt.Errorf("accepts must have at most %d entries", serviceAcceptsMax)
	}
	// Matching is case-insensitive, so entries are stored lower-cased.
	normalized := make([]string, 0, len(accepts))
	seen := map[string]bool{}
	for _, raw := range accepts {
		accept := strings.ToLower(raw)
		if !serviceAcceptExt.MatchString(accept) && !serviceAcceptMime.MatchString(accept) {
			return nil, fmt.Errorf("accepts %q must be an extension like .png or a MIME type like image/*", raw)
		}
		if seen[accept] {
			return nil, fmt.Errorf("accepts lists %q twice", accept)
		}
		seen[accept] = true
		normalized = append(normalized, accept)
	}
	return normalized, nil
}

func decodeServiceOutput(output serviceManifestOutput) (ServiceShape, error) {
	shape := ServiceShape{Kind: output.Kind}
	if !slices.Contains(serviceOutputKinds, output.Kind) {
		return shape, fmt.Errorf("kind %q must be one of %s", output.Kind, strings.Join(serviceOutputKinds, ", "))
	}
	if output.Mime != "" {
		if output.Kind == serviceKindNone || output.Kind == serviceOutputJSON {
			return shape, fmt.Errorf("mime is not valid for kind %s", output.Kind)
		}
		if !serviceOutputMime.MatchString(output.Mime) {
			return shape, fmt.Errorf("mime %q must be a lower-case MIME type like text/markdown", output.Mime)
		}
		shape.Mime = output.Mime
	}
	return shape, nil
}

// validateServiceParams applies the point's own bounds, then the command
// parameter form dialect the host renders.
func validateServiceParams(params map[string]any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if len(encoded) > serviceParamsMaxBytes {
		return fmt.Errorf("must be at most %d bytes", serviceParamsMaxBytes)
	}
	if properties, ok := params["properties"].(map[string]any); ok && len(properties) > serviceParamsMaxProperties {
		return fmt.Errorf("must have at most %d properties", serviceParamsMaxProperties)
	}
	return validateCommandInputSchema(params)
}

// resolveServiceSurfaces validates explicit surfaces or derives the default:
// every serviceDefaultSurfaces entry compatible with the input kind (newer
// surfaces must be declared explicitly). fileMenu and selection need
// no workspace.read: the provider only gets what the user picked there (a
// snapshot token or the selected text, never a path). The result follows the
// contract order and is never nil.
func resolveServiceSurfaces(requested *[]string, inputKind string) ([]string, error) {
	compatible := serviceCompatibleSurfaces[inputKind]
	wanted := map[string]bool{}
	if requested == nil {
		for _, surface := range compatible {
			if slices.Contains(serviceDefaultSurfaces, surface) {
				wanted[surface] = true
			}
		}
	} else {
		for _, surface := range *requested {
			if !slices.Contains(serviceSurfaces, surface) {
				return nil, fmt.Errorf("surface %q must be one of %s", surface, strings.Join(serviceSurfaces, ", "))
			}
			if wanted[surface] {
				return nil, fmt.Errorf("surfaces list %q twice", surface)
			}
			if !slices.Contains(compatible, surface) {
				return nil, fmt.Errorf("surface %q is not compatible with input kind %s", surface, inputKind)
			}
			wanted[surface] = true
		}
	}
	surfaces := make([]string, 0, len(wanted))
	for _, surface := range serviceSurfaces {
		if wanted[surface] {
			surfaces = append(surfaces, surface)
		}
	}
	return surfaces, nil
}

// serviceSurfacePassesContent reports surfaces that pass content the user
// picked (a project file, the editor or terminal selection, a remote file, a
// result set or an HTTP response) to the provider.
func serviceSurfacePassesContent(surface string) bool {
	switch surface {
	case serviceSurfaceFileMenu, serviceSurfaceSelection, serviceSurfaceSFTPFile,
		serviceSurfaceDBResult, serviceSurfaceHTTPResponse, serviceSurfaceTerminalSelection:
		return true
	}
	return false
}

// finalizeServiceEntries rejects a second entry declaring a service name the
// plugin already provides.
func finalizeServiceEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	owners := map[string]string{}
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		service := entry.Value.(ServiceContribution)
		if owner, duplicate := owners[service.Service]; duplicate {
			issues = append(issues, ContributionIssue{
				Point: servicePointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("service %q is already declared by entry %q", service.Service, owner),
			})
			continue
		}
		owners[service.Service] = service.ID
		kept = append(kept, entry)
	}
	return kept, issues
}

// servicesExpandPermission: a development generation needs a new approval
// when it adds a service, points one at another action, or newly offers one
// on a surface that hands the user's content to the plugin.
func servicesExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	previous := map[string]ServiceContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if service, ok := entry.(ServiceContribution); ok {
				previous[service.ID] = service
			}
		}
	}
	for _, entry := range candidate.Entries {
		service, ok := entry.(ServiceContribution)
		if !ok {
			continue
		}
		old, exists := previous[service.ID]
		if !exists || old.Action != service.Action {
			return true
		}
		for _, surface := range service.Surfaces {
			if serviceSurfacePassesContent(surface) && !slices.Contains(old.Surfaces, surface) {
				return true
			}
		}
	}
	return false
}

// servicesLocked returns the plugin's current services. The caller holds record.mu.
func servicesLocked(record *pluginRecord) []ServiceContribution {
	if record.manifest == nil {
		return nil
	}
	resolved := record.manifest.contributions[servicePointKey]
	if resolved == nil {
		return nil
	}
	out := make([]ServiceContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if service, ok := entry.(ServiceContribution); ok {
			out = append(out, service)
		}
	}
	return out
}
