package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_language_feature asks a language feature provider's read
// action for one feature kind. The WebView names the provider by reference
// and sends the document (or a window of whole lines) with the fields the
// kind takes; the host builds the fixed payload {provider, feature,
// languageId, fileName, relativePath?, text, startLine, position?, range?,
// diagnostics?, includeDeclaration?, triggerCharacter?, locale} where
// relativePath is sent only with workspace.read. Per kind
// (checkLanguageFeatureKindFields): position for hover, completion,
// definition and references; includeDeclaration (required) for references;
// a line range for inlayHints; codeLens, documentSymbol and codeAction take
// the whole document (startLine 1), codeAction with the selected range and
// the diagnostics there. The hover and completion payloads are
// byte-for-byte those of host 0.1.0.
// Bounds: text ≤ textMaxBytes, payload ≤ min(action maxPayloadBytes,
// payloadMaxBytes), timeout and slots per kind (languageFeatureCallShape),
// result ≤ resultMaxBytes. Results are plain text; invalid items are
// dropped (definition, references, codeLens, inlayHints and documentSymbol:
// language_feature_results.go). A code action carries only edits of the
// requested document (no URI, no command; an action with any invalid edit
// is dropped whole). The plugin is activated lazily by the first request.
//
// projectRoot (definition and references only, never sent to the plugin)
// is the absolute root of the document's project, the root the WebView
// joins location paths under. It needs relativePath; the host canonicalizes
// it (canonicalTaskProjectRoot), requires the document to exist under it
// and resolves cross-file locations against it. Without a usable root
// every cross-file location is dropped.
var (
	languageFeatureTextMaxBytes          = int(languageFeatureSpec.Limit("textMaxBytes"))
	languageFeaturePayloadMaxBytes       = int(languageFeatureSpec.Limit("payloadMaxBytes"))
	languageFeatureResultMaxBytes        = int(languageFeatureSpec.Limit("resultMaxBytes"))
	languageFeatureHoverContentsMax      = int(languageFeatureSpec.Limit("hoverContentsMax"))
	languageFeatureHoverContentMax       = int(languageFeatureSpec.Limit("hoverContentMaxRunes"))
	languageFeatureCompletionItemsMax    = int(languageFeatureSpec.Limit("completionItemsMax"))
	languageFeatureLabelMaxRunes         = int(languageFeatureSpec.Limit("completionLabelMaxRunes"))
	languageFeatureInsertTextMaxRunes    = int(languageFeatureSpec.Limit("completionInsertTextMaxRunes"))
	languageFeatureDetailMaxRunes        = int(languageFeatureSpec.Limit("completionDetailMaxRunes"))
	languageFeatureDocMaxRunes           = int(languageFeatureSpec.Limit("completionDocumentationMaxRunes"))
	languageFeatureTimeout               = time.Duration(languageFeatureSpec.Limit("timeoutMs")) * time.Millisecond
	languageFeatureReferencesTimeout     = time.Duration(languageFeatureSpec.Limit("referencesTimeoutMs")) * time.Millisecond
	languageFeatureConcurrency           = int(languageFeatureSpec.Limit("concurrentPerPlugin"))
	languageFeaturePassiveConcurrency    = int(languageFeatureSpec.Limit("passiveConcurrentPerPlugin"))
	languageFeatureCompletionKinds       = contributionListSet(languageFeatureSpec.List("completionKinds"))
	languageFeatureCodeActionItemsMax    = int(languageFeatureSpec.Limit("codeActionItemsMax"))
	languageFeatureCodeActionTitleMax    = int(languageFeatureSpec.Limit("codeActionTitleMaxRunes"))
	languageFeatureCodeActionEditsMax    = int(languageFeatureSpec.Limit("codeActionEditsMax"))
	languageFeatureCodeActionEditTextMax = int(languageFeatureSpec.Limit("codeActionEditTextMaxBytes"))
	languageFeatureDiagnosticsMax        = int(languageFeatureSpec.Limit("codeActionDiagnosticsMax"))
	languageFeatureDiagnosticMessageMax  = int(languageFeatureSpec.Limit("codeActionDiagnosticMessageMaxRunes"))
	languageFeatureCodeActionKinds       = contributionListSet(languageFeatureSpec.List("codeActionKinds"))
)

// languageFeatureDiagnosticSeverities are the severities a codeAction
// request may carry (Monaco marker severities).
var languageFeatureDiagnosticSeverities = map[string]bool{"error": true, "warning": true, "info": true, "hint": true}

const languageFeatureLineMax = 10_000_000

// languageFeaturePassiveSlotGroup is the contributedActionCall.SlotGroup of
// the passive kinds, so they never take the slots of the interactive ones.
const languageFeaturePassiveSlotGroup = "passive"

// languageFeatureRange is a request range: for inlayHints the line range
// (absolute, inclusive, inside the sent text; no columns), for codeAction
// the selected range (1-based, columns in UTF-16 units, inside the sent
// document). Columns stay omitempty so the inlayHints payload is
// {startLine, endLine}.
type languageFeatureRange struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn,omitempty"`
}

// documentRange is the codeAction form of the range.
func (r languageFeatureRange) documentRange() LanguageRange {
	return LanguageRange{StartLine: r.StartLine, StartColumn: r.StartColumn, EndLine: r.EndLine, EndColumn: r.EndColumn}
}

// languageFeatureDiagnostic is one editor diagnostic at a codeAction range.
type languageFeatureDiagnostic struct {
	Range    LanguageRange `json:"range"`
	Message  string        `json:"message"`
	Severity string        `json:"severity"`
	Source   string        `json:"source,omitempty"`
}

type languageFeaturePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type languageFeatureRequest struct {
	PluginID     string `json:"pluginId"`
	ProviderID   string `json:"providerId"`
	RequestID    string `json:"requestId"`
	Feature      string `json:"feature"`
	LanguageID   string `json:"languageId"`
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath,omitempty"`
	// ProjectRoot is host-only (never in the payload): definition and
	// references resolve cross-file locations under it.
	ProjectRoot string `json:"projectRoot,omitempty"`
	Text        string `json:"text"`
	StartLine   int    `json:"startLine"`
	// Position, Range, Diagnostics and IncludeDeclaration depend on the
	// feature kind (checkLanguageFeatureKindFields).
	Position           *languageFeaturePosition    `json:"position,omitempty"`
	Range              *languageFeatureRange       `json:"range,omitempty"`
	Diagnostics        []languageFeatureDiagnostic `json:"diagnostics,omitempty"`
	IncludeDeclaration *bool                       `json:"includeDeclaration,omitempty"`
	TriggerCharacter   string                      `json:"triggerCharacter,omitempty"`
	Locale             string                      `json:"locale,omitempty"`
}

type languageFeaturePayload struct {
	Provider     string `json:"provider"`
	Feature      string `json:"feature"`
	LanguageID   string `json:"languageId"`
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath,omitempty"`
	Text         string `json:"text"`
	StartLine    int    `json:"startLine"`
	// New fields stay omitempty so hover and completion payloads are
	// unchanged byte for byte; codeAction always carries diagnostics
	// (possibly empty).
	Position           *languageFeaturePosition     `json:"position,omitempty"`
	Range              *languageFeatureRange        `json:"range,omitempty"`
	Diagnostics        *[]languageFeatureDiagnostic `json:"diagnostics,omitempty"`
	IncludeDeclaration *bool                        `json:"includeDeclaration,omitempty"`
	TriggerCharacter   string                       `json:"triggerCharacter,omitempty"`
	Locale             string                       `json:"locale"`
}

// LanguageCompletionItem is one validated completion item (plain text).
type LanguageCompletionItem struct {
	Label         string `json:"label"`
	InsertText    string `json:"insertText,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
	Kind          string `json:"kind,omitempty"`
}

type languageFeatureResponse struct {
	Contents   []string                 `json:"contents,omitempty"`
	Items      []LanguageCompletionItem `json:"items,omitempty"`
	Incomplete bool                     `json:"incomplete,omitempty"`
	// Locations (definition, references), Lenses (codeLens), Hints
	// (inlayHints) and Symbols (documentSymbol): language_feature_results.go.
	Locations []LanguageLocation  `json:"locations,omitempty"`
	Lenses    []LanguageCodeLens  `json:"lenses,omitempty"`
	Hints     []LanguageInlayHint `json:"hints,omitempty"`
	Symbols   []LanguageSymbol    `json:"symbols,omitempty"`
	// Actions (codeAction): parseLanguageCodeActionResult.
	Actions   []LanguageCodeAction `json:"actions,omitempty"`
	Dropped   int                  `json:"dropped"`
	Truncated bool                 `json:"truncated"`
}

// LanguageTextEdit replaces Range of the requested document with Text.
type LanguageTextEdit struct {
	Range LanguageRange `json:"range"`
	Text  string        `json:"text"`
}

// LanguageCodeAction is one validated quick fix: edits of the requested
// document only, sorted and non-overlapping.
type LanguageCodeAction struct {
	Title       string             `json:"title"`
	Kind        string             `json:"kind,omitempty"`
	IsPreferred bool               `json:"isPreferred,omitempty"`
	Edits       []LanguageTextEdit `json:"edits"`
}

type languageCodeActionRawResult struct {
	Actions []json.RawMessage `json:"actions"`
}

type languageCodeActionRawItem struct {
	Title       string            `json:"title"`
	Kind        *string           `json:"kind,omitempty"`
	IsPreferred *bool             `json:"isPreferred,omitempty"`
	Edits       []json.RawMessage `json:"edits"`
}

type languageTextEditRaw struct {
	Range LanguageRange `json:"range"`
	Text  string        `json:"text"`
}

type languageHoverRawResult struct {
	Contents []json.RawMessage `json:"contents"`
}

type languageCompletionRawResult struct {
	Items      []json.RawMessage `json:"items"`
	Incomplete *bool             `json:"incomplete,omitempty"`
}

type languageCompletionRawItem struct {
	Label         string  `json:"label"`
	InsertText    *string `json:"insertText,omitempty"`
	Detail        *string `json:"detail,omitempty"`
	Documentation *string `json:"documentation,omitempty"`
	Kind          *string `json:"kind,omitempty"`
}

func (manager *pluginManager) languageFeature(ctx context.Context, request languageFeatureRequest) (languageFeatureResponse, error) {
	if err := normalizeLanguageFeatureRequest(&request); err != nil {
		return languageFeatureResponse{}, err
	}
	if _, _, ok := enabledContributionPoint(languageFeaturePointKey); !ok {
		return languageFeatureResponse{}, newContributionError(contributionErrUnavailable, "language features are not enabled in this host")
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return languageFeatureResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	record.mu.Lock()
	provider, lookupErr := languageFeatureLocked(record, request.ProviderID)
	var action ManifestUIAction
	var scope languageResultScope
	if lookupErr == nil {
		commands, _, actions := manifestCommandMaps(record.manifest)
		action = actions[provider.Action]
		scope = languageResultScope{
			workspaceRead: manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead),
			commands:      commands, actions: actions, hintRange: request.Range,
			projectRoot: request.ProjectRoot, documentPath: request.RelativePath, ctx: ctx,
		}
	}
	record.mu.Unlock()
	workspaceRead := scope.workspaceRead
	if lookupErr != nil {
		return languageFeatureResponse{}, lookupErr
	}
	if !slices.Contains(provider.Features, request.Feature) {
		return languageFeatureResponse{}, newContributionError(contributionErrInvalidRequest, "language feature provider %q does not provide %s", provider.ID, request.Feature)
	}
	if !slices.Contains(provider.Languages, request.LanguageID) {
		return languageFeatureResponse{}, newContributionError(contributionErrInvalidRequest, "language feature provider %q does not cover %q", provider.ID, request.LanguageID)
	}
	if request.TriggerCharacter != "" && (request.Feature != "completion" || !slices.Contains(provider.TriggerCharacters, request.TriggerCharacter)) {
		return languageFeatureResponse{}, newContributionError(contributionErrInvalidRequest, "trigger character %q is not declared", request.TriggerCharacter)
	}
	payload := buildLanguageFeaturePayload(provider.ID, request, workspaceRead)
	encoded, err := encodeContributionJSON(payload)
	if err != nil {
		return languageFeatureResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	limit := languageFeaturePayloadMaxBytes
	if action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return languageFeatureResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, language feature provider %q allows %d", len(encoded), provider.ID, limit)
	}
	timeout, concurrency, slotGroup := languageFeatureCallShape(request.Feature)
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: languageFeaturePointKey, EntryID: provider.ID, ActionID: provider.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: timeout,
		MaxResultBytes: languageFeatureResultMaxBytes, Concurrency: concurrency, SlotGroup: slotGroup,
		Validate: func(record *pluginRecord) error {
			current, err := languageFeatureLocked(record, provider.ID)
			if err != nil {
				return err
			}
			if current.Action != provider.Action || !slices.Contains(current.Features, request.Feature) || !slices.Contains(current.Languages, request.LanguageID) {
				return newContributionError(contributionErrUnavailable, "language feature provider %q changed", provider.ID)
			}
			return nil
		},
	})
	if err != nil {
		return languageFeatureResponse{}, err
	}
	var response languageFeatureResponse
	switch request.Feature {
	case "hover":
		response, err = parseLanguageHoverResult(raw)
	case "completion":
		response, err = parseLanguageCompletionResult(raw)
	case "definition":
		response, err = parseLanguageLocationsResult(raw, scope, languageFeatureDefinitionMax, true)
	case "references":
		response, err = parseLanguageLocationsResult(raw, scope, languageFeatureReferencesMax, false)
	case "codeLens":
		response, err = parseLanguageCodeLensResult(raw, scope)
	case "inlayHints":
		response, err = parseLanguageInlayHintsResult(raw, scope)
	case "documentSymbol":
		response, err = parseLanguageDocumentSymbolResult(raw)
	case "codeAction":
		response, err = parseLanguageCodeActionResult(raw, languageFeatureLines(request.Text))
	default:
		// normalizeLanguageFeatureRequest admits only contract kinds; a kind
		// without a parser fails closed.
		err = fmt.Errorf("%s results are not supported", request.Feature)
	}
	if err != nil {
		return languageFeatureResponse{}, newContributionError(contributionErrInvalidResult, "language feature provider %q: %v", provider.ID, err)
	}
	return response, nil
}

func normalizeLanguageFeatureRequest(request *languageFeatureRequest) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	if !pluginIDPattern.MatchString(request.PluginID) {
		return invalid("pluginId %q is invalid", request.PluginID)
	}
	if !languageFeatureIDPattern.MatchString(request.ProviderID) {
		return invalid("providerId %q is invalid", request.ProviderID)
	}
	if !slices.Contains(languageFeatureFeatures, request.Feature) {
		return invalid("feature %q must be one of %s", request.Feature, strings.Join(languageFeatureFeatures, ", "))
	}
	if !formatterLanguagePattern.MatchString(request.LanguageID) {
		return invalid("languageId %q is invalid", request.LanguageID)
	}
	name := strings.ReplaceAll(request.FileName, "\\", "/")
	name = path.Base(strings.TrimRight(name, "/"))
	if name == "." || name == "/" || name == "" || len(name) > formatterFileNameMaxBytes || !utf8.ValidString(name) || checkContributionRunes(name, false, false) != nil {
		return invalid("fileName is invalid")
	}
	request.FileName = name
	if request.RelativePath != "" {
		relative, err := validateContributionRelativePath(strings.ReplaceAll(request.RelativePath, "\\", "/"), formatterRelativePathMaxBytes)
		if err != nil {
			return invalid("relativePath %v", err)
		}
		request.RelativePath = relative
	}
	if request.ProjectRoot != "" {
		if request.Feature != "definition" && request.Feature != "references" {
			return invalid("%s takes no projectRoot", request.Feature)
		}
		if request.RelativePath == "" {
			return invalid("projectRoot needs relativePath")
		}
		root := request.ProjectRoot
		if len(root) > languageFeatureProjectRootMaxBytes || !utf8.ValidString(root) || checkContributionRunes(root, false, false) != nil || !filepath.IsAbs(root) {
			return invalid("projectRoot must be an absolute path of at most %d bytes", languageFeatureProjectRootMaxBytes)
		}
	}
	if !utf8.ValidString(request.Text) {
		return invalid("text must be valid UTF-8")
	}
	if len(request.Text) > languageFeatureTextMaxBytes {
		return invalid("text is %d bytes, at most %d are allowed", len(request.Text), languageFeatureTextMaxBytes)
	}
	if request.StartLine < 1 || request.StartLine > languageFeatureLineMax {
		return invalid("startLine must be 1-%d", languageFeatureLineMax)
	}
	if err := checkLanguageFeatureKindFields(request, strings.Split(request.Text, "\n")); err != nil {
		return err
	}
	if request.TriggerCharacter != "" && !languageFeatureTriggerPattern.MatchString(request.TriggerCharacter) {
		return invalid("triggerCharacter is invalid")
	}
	if request.Locale == "" {
		request.Locale = "en"
	}
	if len(request.Locale) > 35 || !searchLocalePattern.MatchString(request.Locale) {
		return invalid("locale %q is invalid", request.Locale)
	}
	return nil
}

// checkLanguageFeatureKindFields applies the per-kind field rules: position
// only (and required) for hover, completion, definition and references;
// includeDeclaration only (and required) for references; range only (and
// required) for inlayHints, with startLine <= range.startLine <=
// range.endLine <= the sent text's last line and no columns, and for
// codeAction, a range with columns inside the document; diagnostics only
// for codeAction (≤ codeActionDiagnosticsMax, each inside the document);
// codeLens, documentSymbol and codeAction take the whole document
// (startLine 1); triggerCharacter only for completion. lines is the sent
// text split on "\n".
func checkLanguageFeatureKindFields(request *languageFeatureRequest, lines []string) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	feature := request.Feature
	switch feature {
	case "hover", "completion", "definition", "references":
		if request.Position == nil {
			return invalid("%s needs position", feature)
		}
		if err := checkLanguagePosition(lines, request.StartLine, *request.Position); err != nil {
			return err
		}
	default:
		if request.Position != nil {
			return invalid("%s takes no position", feature)
		}
	}
	if feature == "references" {
		if request.IncludeDeclaration == nil {
			return invalid("references needs includeDeclaration")
		}
	} else if request.IncludeDeclaration != nil {
		return invalid("%s takes no includeDeclaration", feature)
	}
	switch {
	case feature == "inlayHints":
		if request.Range == nil {
			return invalid("inlayHints needs range")
		}
		if request.Range.StartColumn != 0 || request.Range.EndColumn != 0 {
			return invalid("inlayHints range takes no columns")
		}
		lastLine := request.StartLine + len(lines) - 1
		if request.Range.StartLine < request.StartLine || request.Range.StartLine > request.Range.EndLine || request.Range.EndLine > lastLine {
			return invalid("range must satisfy %d <= range.startLine <= range.endLine <= %d", request.StartLine, lastLine)
		}
	case feature == "codeAction":
		if request.Range == nil {
			return invalid("codeAction needs a range")
		}
		if err := checkLanguageFeatureRange(request.Range.documentRange(), languageFeatureLines(request.Text)); err != nil {
			return invalid("range %v", err)
		}
	case request.Range != nil:
		return invalid("%s takes no range", feature)
	}
	if feature == "codeAction" {
		if err := normalizeLanguageFeatureDiagnostics(request.Diagnostics, languageFeatureLines(request.Text)); err != nil {
			return invalid("%v", err)
		}
	} else if request.Diagnostics != nil {
		return invalid("%s takes no diagnostics", feature)
	}
	if (feature == "codeLens" || feature == "documentSymbol" || feature == "codeAction") && request.StartLine != 1 {
		return invalid("%s needs the whole document (startLine 1)", feature)
	}
	if request.TriggerCharacter != "" && feature != "completion" {
		return invalid("%s takes no triggerCharacter", feature)
	}
	return nil
}

// checkLanguagePosition checks that position is inside the sent text, whose
// first line is startLine.
func checkLanguagePosition(lines []string, startLine int, position languageFeaturePosition) error {
	offset := position.Line - startLine
	if offset < 0 || offset >= len(lines) {
		return newContributionError(contributionErrInvalidRequest, "position.line must be inside the sent text")
	}
	// Monaco columns count UTF-16 units, never more than the line's bytes.
	if position.Column < 1 || position.Column > len(strings.TrimSuffix(lines[offset], "\r"))+1 {
		return newContributionError(contributionErrInvalidRequest, "position.column is outside the line")
	}
	return nil
}

// buildLanguageFeaturePayload is the action payload of a normalized
// request: the kind's fields as sent, diagnostics (possibly empty) only for
// codeAction, relativePath only with workspace.read.
func buildLanguageFeaturePayload(providerID string, request languageFeatureRequest, workspaceRead bool) languageFeaturePayload {
	payload := languageFeaturePayload{
		Provider: providerID, Feature: request.Feature, LanguageID: request.LanguageID, FileName: request.FileName,
		Text: request.Text, StartLine: request.StartLine, Position: request.Position, Range: request.Range,
		IncludeDeclaration: request.IncludeDeclaration, TriggerCharacter: request.TriggerCharacter, Locale: request.Locale,
	}
	if request.Feature == "codeAction" {
		diagnostics := request.Diagnostics
		if diagnostics == nil {
			diagnostics = []languageFeatureDiagnostic{}
		}
		payload.Diagnostics = &diagnostics
	}
	if workspaceRead {
		payload.RelativePath = request.RelativePath
	}
	return payload
}

// normalizeLanguageFeatureDiagnostics checks the codeAction diagnostics in
// place: at most codeActionDiagnosticsMax, each range inside lines, a
// non-blank message, a known severity and a single-line source.
func normalizeLanguageFeatureDiagnostics(diagnostics []languageFeatureDiagnostic, lines []string) error {
	if len(diagnostics) > languageFeatureDiagnosticsMax {
		return fmt.Errorf("at most %d diagnostics are allowed", languageFeatureDiagnosticsMax)
	}
	for index := range diagnostics {
		diagnostic := &diagnostics[index]
		if err := checkLanguageFeatureRange(diagnostic.Range, lines); err != nil {
			return fmt.Errorf("diagnostics[%d].range %v", index, err)
		}
		message, err := validateContributionMultiline(diagnostic.Message, languageFeatureDiagnosticMessageMax, 0)
		if err != nil || strings.TrimSpace(message) == "" {
			return fmt.Errorf("diagnostics[%d].message is invalid", index)
		}
		diagnostic.Message = message
		if !languageFeatureDiagnosticSeverities[diagnostic.Severity] {
			return fmt.Errorf("diagnostics[%d].severity must be error, warning, info or hint", index)
		}
		if diagnostic.Source, err = validateContributionLine(diagnostic.Source, languageFeatureTitleMaxRunes, false); err != nil {
			return fmt.Errorf("diagnostics[%d].source %v", index, err)
		}
	}
	return nil
}

// languageFeatureLines splits sent text into lines without their CR.
func languageFeatureLines(text string) []string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// checkLanguageFeatureRange requires a forward (possibly empty) range inside
// lines (1-based; a column may be one past the line end). Monaco columns
// count UTF-16 units, never more than the line's bytes, so the byte length
// is the host's upper bound; the editor re-checks exactly.
func checkLanguageFeatureRange(r LanguageRange, lines []string) error {
	if r.StartLine < 1 || r.EndLine > len(lines) || r.StartLine > r.EndLine {
		return errors.New("is outside the document")
	}
	if r.StartColumn < 1 || r.EndColumn < 1 || r.StartColumn > len(lines[r.StartLine-1])+1 || r.EndColumn > len(lines[r.EndLine-1])+1 {
		return errors.New("column is outside the line")
	}
	if r.StartLine == r.EndLine && r.StartColumn > r.EndColumn {
		return errors.New("ends before it starts")
	}
	return nil
}

// languageFeatureCallShape is the invoke timeout, slot count and slot group
// of one feature kind: references get referencesTimeoutMs, every other kind
// timeoutMs; passive kinds (languageFeaturePassive) take their own
// passiveConcurrentPerPlugin slots, interactive kinds the point's
// concurrentPerPlugin slots.
func languageFeatureCallShape(feature string) (timeout time.Duration, concurrency int, slotGroup string) {
	timeout = languageFeatureTimeout
	if feature == "references" {
		timeout = languageFeatureReferencesTimeout
	}
	if languageFeaturePassive[feature] {
		return timeout, languageFeaturePassiveConcurrency, languageFeaturePassiveSlotGroup
	}
	return timeout, languageFeatureConcurrency, ""
}

// languageFeatureLocked finds the provider in the record's current
// generation. Called with record.mu held.
func languageFeatureLocked(record *pluginRecord, providerID string) (LanguageFeatureContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(languageFeaturePointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if provider, ok := entry.(LanguageFeatureContribution); ok && provider.ID == providerID {
					return provider, nil
				}
			}
		}
	}
	return LanguageFeatureContribution{}, newContributionError(contributionErrNotFound, "language feature provider %q is not contributed by %q", providerID, record.id)
}

// parseLanguageHoverResult validates {"contents": [string]}: blocks past
// hoverContentsMax are cut, invalid or blank blocks dropped.
func parseLanguageHoverResult(raw json.RawMessage) (languageFeatureResponse, error) {
	var result languageHoverRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"contents\": [...]}: %v", err)
	}
	if result.Contents == nil {
		return languageFeatureResponse{}, errors.New("result must contain contents")
	}
	response := languageFeatureResponse{Contents: []string{}}
	rawContents := result.Contents
	if len(rawContents) > languageFeatureHoverContentsMax {
		response.Truncated = true
		rawContents = rawContents[:languageFeatureHoverContentsMax]
	}
	for _, rawContent := range rawContents {
		var content string
		if err := json.Unmarshal(rawContent, &content); err != nil {
			response.Dropped++
			continue
		}
		normalized, err := validateContributionMultiline(content, languageFeatureHoverContentMax, 0)
		if err != nil || strings.TrimSpace(normalized) == "" {
			response.Dropped++
			continue
		}
		response.Contents = append(response.Contents, normalized)
	}
	return response, nil
}

// parseLanguageCompletionResult validates {"items": [...], "incomplete"?}:
// items past completionItemsMax are cut, invalid items dropped.
func parseLanguageCompletionResult(raw json.RawMessage) (languageFeatureResponse, error) {
	var result languageCompletionRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"items\": [...]}: %v", err)
	}
	if result.Items == nil {
		return languageFeatureResponse{}, errors.New("result must contain items")
	}
	response := languageFeatureResponse{Items: []LanguageCompletionItem{}}
	if result.Incomplete != nil {
		response.Incomplete = *result.Incomplete
	}
	rawItems := result.Items
	if len(rawItems) > languageFeatureCompletionItemsMax {
		response.Truncated = true
		rawItems = rawItems[:languageFeatureCompletionItemsMax]
	}
	for _, rawItem := range rawItems {
		item, err := normalizeLanguageCompletionItem(rawItem)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func normalizeLanguageCompletionItem(raw json.RawMessage) (LanguageCompletionItem, error) {
	var item languageCompletionRawItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return LanguageCompletionItem{}, err
	}
	label, err := validateContributionLine(item.Label, languageFeatureLabelMaxRunes, true)
	if err != nil {
		return LanguageCompletionItem{}, fmt.Errorf("label %w", err)
	}
	out := LanguageCompletionItem{Label: label}
	if item.InsertText != nil {
		if out.InsertText, err = validateContributionMultiline(*item.InsertText, languageFeatureInsertTextMaxRunes, 0); err != nil {
			return LanguageCompletionItem{}, fmt.Errorf("insertText %w", err)
		}
	}
	if item.Detail != nil {
		if out.Detail, err = validateContributionLine(*item.Detail, languageFeatureDetailMaxRunes, false); err != nil {
			return LanguageCompletionItem{}, fmt.Errorf("detail %w", err)
		}
	}
	if item.Documentation != nil {
		if out.Documentation, err = validateContributionMultiline(*item.Documentation, languageFeatureDocMaxRunes, 0); err != nil {
			return LanguageCompletionItem{}, fmt.Errorf("documentation %w", err)
		}
	}
	if item.Kind != nil {
		if !languageFeatureCompletionKinds[*item.Kind] {
			return LanguageCompletionItem{}, fmt.Errorf("kind %q is not a known completion kind", *item.Kind)
		}
		out.Kind = *item.Kind
	}
	return out, nil
}

// parseLanguageCodeActionResult validates {"actions": [{title, kind?,
// isPreferred?, edits: [{range, text}]}]}: actions past codeActionItemsMax
// are cut. An action is dropped whole when any edit is invalid (range
// outside lines, oversized or unsafe text), when it has no edits or more
// than codeActionEditsMax, when edits overlap, or when it carries any other
// field (a URI or a command is never accepted).
func parseLanguageCodeActionResult(raw json.RawMessage, lines []string) (languageFeatureResponse, error) {
	var result languageCodeActionRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"actions\": [...]}: %v", err)
	}
	if result.Actions == nil {
		return languageFeatureResponse{}, errors.New("result must contain actions")
	}
	response := languageFeatureResponse{Actions: []LanguageCodeAction{}}
	rawActions := result.Actions
	if len(rawActions) > languageFeatureCodeActionItemsMax {
		response.Truncated = true
		rawActions = rawActions[:languageFeatureCodeActionItemsMax]
	}
	for _, rawAction := range rawActions {
		action, err := normalizeLanguageCodeAction(rawAction, lines)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Actions = append(response.Actions, action)
	}
	return response, nil
}

func normalizeLanguageCodeAction(raw json.RawMessage, lines []string) (LanguageCodeAction, error) {
	var item languageCodeActionRawItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return LanguageCodeAction{}, err
	}
	title, err := validateContributionLine(item.Title, languageFeatureCodeActionTitleMax, true)
	if err != nil {
		return LanguageCodeAction{}, fmt.Errorf("title %w", err)
	}
	action := LanguageCodeAction{Title: title}
	if item.Kind != nil {
		if !languageFeatureCodeActionKinds[*item.Kind] {
			return LanguageCodeAction{}, fmt.Errorf("kind %q is not a known code action kind", *item.Kind)
		}
		action.Kind = *item.Kind
	}
	if item.IsPreferred != nil {
		action.IsPreferred = *item.IsPreferred
	}
	if len(item.Edits) == 0 || len(item.Edits) > languageFeatureCodeActionEditsMax {
		return LanguageCodeAction{}, fmt.Errorf("needs 1-%d edits", languageFeatureCodeActionEditsMax)
	}
	for _, rawEdit := range item.Edits {
		var edit languageTextEditRaw
		if err := decodeStrictJSON(rawEdit, &edit, true); err != nil {
			return LanguageCodeAction{}, err
		}
		if err := checkLanguageFeatureRange(edit.Range, lines); err != nil {
			return LanguageCodeAction{}, fmt.Errorf("edit range %w", err)
		}
		text, err := validateContributionMultiline(edit.Text, 0, languageFeatureCodeActionEditTextMax)
		if err != nil {
			return LanguageCodeAction{}, fmt.Errorf("edit text %w", err)
		}
		action.Edits = append(action.Edits, LanguageTextEdit{Range: edit.Range, Text: text})
	}
	slices.SortStableFunc(action.Edits, func(a, b LanguageTextEdit) int {
		return compareLanguageFeaturePositions(a.Range.StartLine, a.Range.StartColumn, b.Range.StartLine, b.Range.StartColumn)
	})
	for index := 1; index < len(action.Edits); index++ {
		previous, current := action.Edits[index-1].Range, action.Edits[index].Range
		if compareLanguageFeaturePositions(current.StartLine, current.StartColumn, previous.EndLine, previous.EndColumn) < 0 {
			return LanguageCodeAction{}, errors.New("edits overlap")
		}
	}
	return action, nil
}

func compareLanguageFeaturePositions(lineA, columnA, lineB, columnB int) int {
	if lineA != lineB {
		return lineA - lineB
	}
	return columnA - columnB
}

func pluginHostLanguageFeatureHandler(_, args string) (string, error) {
	var request languageFeatureRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_language_feature: invalid arguments: %v", err)
	}
	response, err := host().languageFeature(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_language_feature", pluginHostLanguageFeatureHandler)
}
