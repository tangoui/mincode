package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	backendfs "mincode/app/backend/fs"
)

// Result parsing for the definition, references, codeLens, inlayHints and
// documentSymbol language feature kinds. Every result is read-only plain
// text: a wrong top-level shape is invalid_result; an invalid item is
// dropped (dropped++); items past a kind's cap are cut in order (truncated).
// The whole result is already bounded by resultMaxBytes in
// invokeContributedAction.
//
//   - Range {startLine, startColumn, endLine, endColumn}: 1-based absolute
//     lines (≤ languageFeatureLineMax), columns ≤ columnMax, start not
//     after end.
//   - Location {path?, range}: no path is the requesting document. A path
//     must be a clean project-relative path (validateContributionRelativePath,
//     ≤ locationPathMaxBytes) without URL, shell or confusable characters
//     (languageLocationPathCharsOK), needs workspace.read, never names VCS
//     internals or a sensitive file (workspacePathDenied, as host.workspace.*)
//     and must resolve under the document's project root (request
//     projectRoot; see languageLocationRoots): following symlinks it stays
//     inside the root and does not reach a denied file; a missing target's
//     deepest existing ancestor stays inside the root; a dangling symlink is
//     dropped. Without a usable root, or once the resolve budget (distinct
//     paths, time, ctx) is spent, cross-file locations are dropped.
//   - CodeLens commands: only the plugin's own action commands (not view
//     targets). args only for an effective-read command with an inputSchema
//     (scalar values, ≤ codeLensArgsMaxProperties, ≤ codeLensArgsMaxBytes,
//     matching the schema); write and dangerous commands take no args.
//     A title containing "$(" (Monaco codicon syntax) drops the lens.
//   - Text is single-line (validateContributionLine) and additionally free
//     of every Cc / Cf character and U+2028 / U+2029 (contextRuneRefused).
//     Invalid UTF-8 in an item drops the item.
var (
	languageFeatureDefinitionMax      = int(languageFeatureSpec.Limit("definitionLocationsMax"))
	languageFeatureReferencesMax      = int(languageFeatureSpec.Limit("referencesLocationsMax"))
	languageFeatureCodeLensesMax      = int(languageFeatureSpec.Limit("codeLensesMax"))
	languageFeatureCodeLensTitleMax   = int(languageFeatureSpec.Limit("codeLensTitleMaxRunes"))
	languageFeatureCodeLensArgsBytes  = int(languageFeatureSpec.Limit("codeLensArgsMaxBytes"))
	languageFeatureCodeLensArgsProps  = int(languageFeatureSpec.Limit("codeLensArgsMaxProperties"))
	languageFeatureInlayHintsMax      = int(languageFeatureSpec.Limit("inlayHintsMax"))
	languageFeatureInlayLabelMax      = int(languageFeatureSpec.Limit("inlayHintLabelMaxRunes"))
	languageFeatureSymbolsMax         = int(languageFeatureSpec.Limit("documentSymbolsMax"))
	languageFeatureSymbolDepthMax     = int(languageFeatureSpec.Limit("documentSymbolDepthMax"))
	languageFeatureSymbolNameMax      = int(languageFeatureSpec.Limit("symbolNameMaxRunes"))
	languageFeatureSymbolDetailMax    = int(languageFeatureSpec.Limit("symbolDetailMaxRunes"))
	languageFeatureLocationPathMax    = int(languageFeatureSpec.Limit("locationPathMaxBytes"))
	languageFeatureColumnMax          = int(languageFeatureSpec.Limit("columnMax"))
	languageFeatureSymbolKinds        = contributionListSet(languageFeatureSpec.List("symbolKinds"))
	languageFeatureInlayHintKindsList = contributionListSet(languageFeatureSpec.List("inlayHintKinds"))
)

// LanguageRange is a validated range (1-based, absolute lines).
type LanguageRange struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

// LanguageLocation is a validated definition or reference target; Path ""
// is the requesting document.
type LanguageLocation struct {
	Path           string         `json:"path,omitempty"`
	Range          LanguageRange  `json:"range"`
	SelectionRange *LanguageRange `json:"selectionRange,omitempty"`
}

// LanguageCodeLens is a validated CodeLens; Command "" is display-only.
type LanguageCodeLens struct {
	Range   LanguageRange  `json:"range"`
	Title   string         `json:"title"`
	Command string         `json:"command,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
}

// LanguageInlayHint is a validated inlay hint (plain-text label).
type LanguageInlayHint struct {
	Position     languageFeaturePosition `json:"position"`
	Label        string                  `json:"label"`
	Kind         string                  `json:"kind,omitempty"`
	PaddingLeft  bool                    `json:"paddingLeft,omitempty"`
	PaddingRight bool                    `json:"paddingRight,omitempty"`
}

// LanguageSymbol is a validated document symbol; SelectionRange defaults to
// Range.
type LanguageSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           string           `json:"kind"`
	Range          LanguageRange    `json:"range"`
	SelectionRange LanguageRange    `json:"selectionRange"`
	Children       []LanguageSymbol `json:"children,omitempty"`
}

// languageResultScope is what result parsing needs from the provider's
// plugin, taken under record.mu: whether it holds workspace.read, its own
// commands and actions (manifestCommandMaps), the inlayHints request range,
// and for location targets the request's projectRoot and relativePath (the
// document under that root) and the request context.
type languageResultScope struct {
	workspaceRead bool
	commands      map[string]ManifestCommand
	actions       map[string]ManifestUIAction
	hintRange     *languageFeatureRange
	projectRoot   string
	documentPath  string
	ctx           context.Context
}

// languageFeatureProjectRootMaxBytes bounds the request projectRoot.
const languageFeatureProjectRootMaxBytes = 4096

// Resolve budget for cross-file locations in one result: at most
// languageLocationResolveMax distinct paths are resolved on disk, within
// languageLocationResolveBudget; later cross-file locations are dropped.
// Variables so tests can lower them.
var (
	languageLocationResolveMax    = 128
	languageLocationResolveBudget = 250 * time.Millisecond
)

type languageRangeRaw struct {
	StartLine   *int `json:"startLine"`
	StartColumn *int `json:"startColumn"`
	EndLine     *int `json:"endLine"`
	EndColumn   *int `json:"endColumn"`
}

type languagePositionRaw struct {
	Line   *int `json:"line"`
	Column *int `json:"column"`
}

type languageLocationRaw struct {
	Path           *string         `json:"path"`
	Range          json.RawMessage `json:"range"`
	SelectionRange json.RawMessage `json:"selectionRange"`
}

type languageCodeLensRaw struct {
	Range   json.RawMessage `json:"range"`
	Title   string          `json:"title"`
	Command *string         `json:"command"`
	Args    json.RawMessage `json:"args"`
}

type languageInlayHintRaw struct {
	Position     json.RawMessage `json:"position"`
	Label        string          `json:"label"`
	Kind         *string         `json:"kind"`
	PaddingLeft  *bool           `json:"paddingLeft"`
	PaddingRight *bool           `json:"paddingRight"`
}

type languageSymbolRaw struct {
	Name           string            `json:"name"`
	Detail         *string           `json:"detail"`
	Kind           string            `json:"kind"`
	Range          json.RawMessage   `json:"range"`
	SelectionRange json.RawMessage   `json:"selectionRange"`
	Children       []json.RawMessage `json:"children"`
}

// decodeLanguageItem strictly decodes one result item. Invalid UTF-8 is
// refused up front: encoding/json would silently turn it into U+FFFD.
func decodeLanguageItem(raw json.RawMessage, destination any) error {
	if !utf8.Valid(raw) {
		return errors.New("must be valid UTF-8")
	}
	return decodeStrictJSON(raw, destination, true)
}

// languageResultLine validates single-line result text: the contribution
// line rules plus no Cc / Cf character and no U+2028 / U+2029.
func languageResultLine(value string, maxRunes int, required bool) (string, error) {
	normalized, err := validateContributionLine(value, maxRunes, required)
	if err != nil {
		return "", err
	}
	if strings.ContainsFunc(normalized, contextRuneRefused) {
		return "", errors.New("contains a control or format character")
	}
	return normalized, nil
}

// parseLanguageRange validates a range object.
func parseLanguageRange(raw json.RawMessage) (LanguageRange, error) {
	if len(raw) == 0 {
		return LanguageRange{}, errors.New("range is required")
	}
	var value languageRangeRaw
	if err := decodeLanguageItem(raw, &value); err != nil {
		return LanguageRange{}, fmt.Errorf("range %w", err)
	}
	if value.StartLine == nil || value.StartColumn == nil || value.EndLine == nil || value.EndColumn == nil {
		return LanguageRange{}, errors.New("range needs startLine, startColumn, endLine and endColumn")
	}
	out := LanguageRange{StartLine: *value.StartLine, StartColumn: *value.StartColumn, EndLine: *value.EndLine, EndColumn: *value.EndColumn}
	if !languageLineValid(out.StartLine) || !languageLineValid(out.EndLine) || !languageColumnValid(out.StartColumn) || !languageColumnValid(out.EndColumn) {
		return LanguageRange{}, fmt.Errorf("range lines must be 1-%d and columns 1-%d", languageFeatureLineMax, languageFeatureColumnMax)
	}
	if out.EndLine < out.StartLine || (out.EndLine == out.StartLine && out.EndColumn < out.StartColumn) {
		return LanguageRange{}, errors.New("range must not end before it starts")
	}
	return out, nil
}

// parseLanguagePosition validates a {line, column} position.
func parseLanguagePosition(raw json.RawMessage) (languageFeaturePosition, error) {
	if len(raw) == 0 {
		return languageFeaturePosition{}, errors.New("position is required")
	}
	var value languagePositionRaw
	if err := decodeLanguageItem(raw, &value); err != nil {
		return languageFeaturePosition{}, fmt.Errorf("position %w", err)
	}
	if value.Line == nil || value.Column == nil || !languageLineValid(*value.Line) || !languageColumnValid(*value.Column) {
		return languageFeaturePosition{}, fmt.Errorf("position needs line 1-%d and column 1-%d", languageFeatureLineMax, languageFeatureColumnMax)
	}
	return languageFeaturePosition{Line: *value.Line, Column: *value.Column}, nil
}

func languageLineValid(line int) bool { return line >= 1 && line <= languageFeatureLineMax }

func languageColumnValid(column int) bool { return column >= 1 && column <= languageFeatureColumnMax }

// languageRangeContains reports whether inner lies inside outer.
func languageRangeContains(outer, inner LanguageRange) bool {
	before := func(line, column, otherLine, otherColumn int) bool {
		return line < otherLine || (line == otherLine && column <= otherColumn)
	}
	return before(outer.StartLine, outer.StartColumn, inner.StartLine, inner.StartColumn) &&
		before(inner.EndLine, inner.EndColumn, outer.EndLine, outer.EndColumn)
}

// languageLocationResolver checks location paths for one result: verdicts
// are cached per path, the project root is resolved once, and on-disk
// resolution is bounded (languageLocationResolveMax, the time budget, ctx).
type languageLocationResolver struct {
	scope    languageResultScope
	cache    map[string]error
	rootDone bool
	root     string
	rootErr  error
	resolved int
	deadline time.Time
}

func newLanguageLocationResolver(scope languageResultScope) *languageLocationResolver {
	return &languageLocationResolver{scope: scope, cache: map[string]error{}, deadline: time.Now().Add(languageLocationResolveBudget)}
}

func (resolver *languageLocationResolver) check(value string) error {
	if err, seen := resolver.cache[value]; seen {
		return err
	}
	err := resolver.checkUncached(value)
	resolver.cache[value] = err
	return err
}

func (resolver *languageLocationResolver) checkUncached(value string) error {
	if _, err := validateContributionRelativePath(value, languageFeatureLocationPathMax); err != nil {
		return fmt.Errorf("path %w", err)
	}
	if strings.ContainsFunc(value, contextRuneRefused) || !languageLocationPathCharsOK(value) {
		return errors.New("path contains a disallowed character")
	}
	if !resolver.scope.workspaceRead {
		return errors.New("a location in another file needs workspace.read")
	}
	if workspacePathDenied(value, false) {
		return errors.New("path is not accessible to plugins")
	}
	if !resolver.rootDone {
		resolver.rootDone = true
		resolver.root, resolver.rootErr = languageLocationRoot(resolver.scope.projectRoot, resolver.scope.documentPath)
	}
	if resolver.rootErr != nil {
		return resolver.rootErr
	}
	ctx := resolver.scope.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || resolver.resolved >= languageLocationResolveMax || time.Now().After(resolver.deadline) {
		return errors.New("location resolve budget is spent")
	}
	resolver.resolved++
	return checkLanguageLocationTarget(resolver.root, value)
}

// languageLocationRoot canonicalizes the request projectRoot with the
// project rules of tasks, tree views and test providers (absolute, existing,
// not the filesystem root, the home folder or application data) and
// requires the requesting document (relativePath) to be a file under it, so
// the root is the one the document was opened from. Any failure means no
// root: every cross-file location is dropped.
func languageLocationRoot(projectRoot, documentPath string) (string, error) {
	if projectRoot == "" || documentPath == "" {
		return "", errors.New("no project root for cross-file locations")
	}
	root, err := canonicalTaskProjectRoot(projectRoot)
	if err != nil {
		return "", errors.New("project root is not usable")
	}
	document, err := backendfs.ResolveUnderProjectRoot(root, documentPath)
	if err != nil {
		return "", errors.New("the document is not under the project root")
	}
	if info, err := os.Stat(document); err != nil || info.IsDir() {
		return "", errors.New("the document is not under the project root")
	}
	return root, nil
}

// checkLanguageLocationTarget resolves a lexically clean, allowed path under
// the canonical root. The deepest existing ancestor (the path itself when it
// exists) is resolved with EvalSymlinks: it must stay inside the root, and
// the resolved path plus the missing rest must not be denied. A dangling
// symlink on the way is refused.
func checkLanguageLocationTarget(root, value string) error {
	parts := strings.Split(value, "/")
	for depth := len(parts); depth >= 0; depth-- {
		ancestor := filepath.Join(append([]string{root}, parts[:depth]...)...)
		if _, err := os.Lstat(ancestor); err != nil {
			if os.IsNotExist(err) && depth > 0 {
				continue
			}
			return errors.New("path could not be resolved")
		}
		canonical, err := filepath.EvalSymlinks(ancestor)
		if err != nil {
			return errors.New("path resolves through a dangling symlink")
		}
		if canonical != root && !strings.HasPrefix(canonical, root+string(filepath.Separator)) {
			return errors.New("path escapes the project root")
		}
		relative, err := filepath.Rel(root, canonical)
		if err != nil {
			return errors.New("path escapes the project root")
		}
		target := strings.Join(append([]string{filepath.ToSlash(relative)}, parts[depth:]...), "/")
		if workspacePathDenied(strings.TrimPrefix(target, "./"), false) {
			return errors.New("path is not accessible to plugins")
		}
		return nil
	}
	return errors.New("path could not be resolved")
}

// languageLocationPathCharsOK refuses characters a path should never need
// and that URL, shell or path parsers may reinterpret: % ? # NUL backslash
// colon (drive letters, schemes), a leading ~ or $, and fullwidth or
// look-alike dots and slashes.
func languageLocationPathCharsOK(value string) bool {
	if strings.HasPrefix(value, "~") || strings.HasPrefix(value, "$") {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		switch r {
		case '%', '?', '#', 0, '\\', ':',
			'．', '｡', '․', '。', '﹒', // look-alike dots
			'／', '∕', '⁄', '⧸', '＼', '∖', '⧹', '﹨': // look-alike slashes
			return true
		}
		return false
	})
}

// parseLanguageLocationsResult validates {"locations": [...]} for
// definition (allowSelection: optional selectionRange inside range) and
// references (selectionRange drops the item); max is the kind's cap.
func parseLanguageLocationsResult(raw json.RawMessage, scope languageResultScope, max int, allowSelection bool) (languageFeatureResponse, error) {
	var result struct {
		Locations []json.RawMessage `json:"locations"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"locations\": [...]}: %v", err)
	}
	if result.Locations == nil {
		return languageFeatureResponse{}, errors.New("result must contain locations")
	}
	response := languageFeatureResponse{Locations: []LanguageLocation{}}
	items := result.Locations
	if len(items) > max {
		response.Truncated = true
		items = items[:max]
	}
	resolver := newLanguageLocationResolver(scope)
	for _, item := range items {
		location, err := parseLanguageLocation(item, allowSelection, resolver)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Locations = append(response.Locations, location)
	}
	return response, nil
}

func parseLanguageLocation(raw json.RawMessage, allowSelection bool, resolver *languageLocationResolver) (LanguageLocation, error) {
	var item languageLocationRaw
	if err := decodeLanguageItem(raw, &item); err != nil {
		return LanguageLocation{}, err
	}
	out := LanguageLocation{}
	var err error
	if out.Range, err = parseLanguageRange(item.Range); err != nil {
		return LanguageLocation{}, err
	}
	if len(item.SelectionRange) > 0 {
		if !allowSelection {
			return LanguageLocation{}, errors.New("selectionRange is not allowed")
		}
		selection, err := parseLanguageRange(item.SelectionRange)
		if err != nil {
			return LanguageLocation{}, fmt.Errorf("selectionRange: %w", err)
		}
		if !languageRangeContains(out.Range, selection) {
			return LanguageLocation{}, errors.New("selectionRange must lie inside range")
		}
		out.SelectionRange = &selection
	}
	if item.Path != nil {
		if err := resolver.check(*item.Path); err != nil {
			return LanguageLocation{}, err
		}
		out.Path = *item.Path
	}
	return out, nil
}

// parseLanguageCodeLensResult validates {"lenses": [...]} (≤ codeLensesMax).
func parseLanguageCodeLensResult(raw json.RawMessage, scope languageResultScope) (languageFeatureResponse, error) {
	var result struct {
		Lenses []json.RawMessage `json:"lenses"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"lenses\": [...]}: %v", err)
	}
	if result.Lenses == nil {
		return languageFeatureResponse{}, errors.New("result must contain lenses")
	}
	response := languageFeatureResponse{Lenses: []LanguageCodeLens{}}
	items := result.Lenses
	if len(items) > languageFeatureCodeLensesMax {
		response.Truncated = true
		items = items[:languageFeatureCodeLensesMax]
	}
	for _, item := range items {
		lens, err := parseLanguageCodeLens(item, scope)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Lenses = append(response.Lenses, lens)
	}
	return response, nil
}

func parseLanguageCodeLens(raw json.RawMessage, scope languageResultScope) (LanguageCodeLens, error) {
	var item languageCodeLensRaw
	if err := decodeLanguageItem(raw, &item); err != nil {
		return LanguageCodeLens{}, err
	}
	out := LanguageCodeLens{}
	var err error
	if out.Range, err = parseLanguageRange(item.Range); err != nil {
		return LanguageCodeLens{}, err
	}
	if out.Title, err = languageResultLine(item.Title, languageFeatureCodeLensTitleMax, true); err != nil {
		return LanguageCodeLens{}, fmt.Errorf("title %w", err)
	}
	if strings.Contains(out.Title, "$(") {
		return LanguageCodeLens{}, errors.New("title must not use codicon syntax")
	}
	hasArgs := len(item.Args) > 0
	if item.Command == nil {
		if hasArgs {
			return LanguageCodeLens{}, errors.New("args need a command")
		}
		return out, nil
	}
	id := *item.Command
	if !hasArgs {
		// Any risk: the click runs the same path as a menu, with its
		// confirmation and parameter form.
		command, ok := scope.commands[id]
		if id == "" || !ok || command.Action == "" || command.Target != nil {
			return LanguageCodeLens{}, fmt.Errorf("command %q is not one of this plugin's action commands", id)
		}
		out.Command = id
		return out, nil
	}
	command, err := contributionReadCommand(scope.commands, scope.actions, id)
	if err != nil {
		return LanguageCodeLens{}, err
	}
	if command.InputSchema == nil {
		return LanguageCodeLens{}, fmt.Errorf("command %q declares no inputSchema for args", id)
	}
	var args map[string]any
	if err := decodeStrictJSON(item.Args, &args, false); err != nil || args == nil {
		return LanguageCodeLens{}, errors.New("args must be an object")
	}
	if err := validateContributionScalarArgs(args, languageFeatureCodeLensArgsProps, languageFeatureCodeLensArgsBytes); err != nil {
		return LanguageCodeLens{}, err
	}
	if err := validateCommandInputValues(command.InputSchema, args); err != nil {
		return LanguageCodeLens{}, fmt.Errorf("args: %w", err)
	}
	out.Command, out.Args = id, nonEmptyArgs(args)
	return out, nil
}

// parseLanguageInlayHintsResult validates {"hints": [...]} (≤ inlayHintsMax);
// a hint outside scope.hintRange (the request range) is dropped.
func parseLanguageInlayHintsResult(raw json.RawMessage, scope languageResultScope) (languageFeatureResponse, error) {
	var result struct {
		Hints []json.RawMessage `json:"hints"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"hints\": [...]}: %v", err)
	}
	if result.Hints == nil {
		return languageFeatureResponse{}, errors.New("result must contain hints")
	}
	response := languageFeatureResponse{Hints: []LanguageInlayHint{}}
	items := result.Hints
	if len(items) > languageFeatureInlayHintsMax {
		response.Truncated = true
		items = items[:languageFeatureInlayHintsMax]
	}
	for _, item := range items {
		hint, err := parseLanguageInlayHint(item, scope)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Hints = append(response.Hints, hint)
	}
	return response, nil
}

func parseLanguageInlayHint(raw json.RawMessage, scope languageResultScope) (LanguageInlayHint, error) {
	var item languageInlayHintRaw
	if err := decodeLanguageItem(raw, &item); err != nil {
		return LanguageInlayHint{}, err
	}
	out := LanguageInlayHint{}
	var err error
	if out.Position, err = parseLanguagePosition(item.Position); err != nil {
		return LanguageInlayHint{}, err
	}
	if scope.hintRange != nil && (out.Position.Line < scope.hintRange.StartLine || out.Position.Line > scope.hintRange.EndLine) {
		return LanguageInlayHint{}, errors.New("position is outside the requested range")
	}
	if out.Label, err = languageResultLine(item.Label, languageFeatureInlayLabelMax, true); err != nil {
		return LanguageInlayHint{}, fmt.Errorf("label %w", err)
	}
	if item.Kind != nil {
		if !languageFeatureInlayHintKindsList[*item.Kind] {
			return LanguageInlayHint{}, fmt.Errorf("kind %q is not a known inlay hint kind", *item.Kind)
		}
		out.Kind = *item.Kind
	}
	out.PaddingLeft = item.PaddingLeft != nil && *item.PaddingLeft
	out.PaddingRight = item.PaddingRight != nil && *item.PaddingRight
	return out, nil
}

// languageSymbolWalk carries the documentSymbol totals across the tree.
type languageSymbolWalk struct {
	count     int
	dropped   int
	truncated bool
}

// parseLanguageDocumentSymbolResult validates {"symbols": [...]}: at most
// documentSymbolsMax nodes in document order and documentSymbolDepthMax
// levels (deeper children and nodes past the total are cut: truncated); an
// invalid node is dropped with its children (dropped + 1).
func parseLanguageDocumentSymbolResult(raw json.RawMessage) (languageFeatureResponse, error) {
	var result struct {
		Symbols []json.RawMessage `json:"symbols"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return languageFeatureResponse{}, fmt.Errorf("result must be {\"symbols\": [...]}: %v", err)
	}
	if result.Symbols == nil {
		return languageFeatureResponse{}, errors.New("result must contain symbols")
	}
	walk := &languageSymbolWalk{}
	symbols := parseLanguageSymbols(result.Symbols, 1, walk)
	if symbols == nil {
		symbols = []LanguageSymbol{}
	}
	return languageFeatureResponse{Symbols: symbols, Dropped: walk.dropped, Truncated: walk.truncated}, nil
}

func parseLanguageSymbols(items []json.RawMessage, depth int, walk *languageSymbolWalk) []LanguageSymbol {
	var out []LanguageSymbol
	for _, item := range items {
		if walk.count >= languageFeatureSymbolsMax {
			walk.truncated = true
			break
		}
		symbol, children, err := parseLanguageSymbol(item)
		if err != nil {
			walk.dropped++
			continue
		}
		walk.count++
		if len(children) > 0 {
			if depth >= languageFeatureSymbolDepthMax {
				walk.truncated = true
			} else {
				symbol.Children = parseLanguageSymbols(children, depth+1, walk)
			}
		}
		out = append(out, symbol)
	}
	return out
}

// parseLanguageSymbol validates one node and returns its raw children
// (decoded lazily, so nesting past the depth cap is never walked).
func parseLanguageSymbol(raw json.RawMessage) (LanguageSymbol, []json.RawMessage, error) {
	var item languageSymbolRaw
	if err := decodeLanguageItem(raw, &item); err != nil {
		return LanguageSymbol{}, nil, err
	}
	out := LanguageSymbol{}
	var err error
	if out.Name, err = languageResultLine(item.Name, languageFeatureSymbolNameMax, true); err != nil {
		return LanguageSymbol{}, nil, fmt.Errorf("name %w", err)
	}
	if item.Detail != nil {
		if out.Detail, err = languageResultLine(*item.Detail, languageFeatureSymbolDetailMax, false); err != nil {
			return LanguageSymbol{}, nil, fmt.Errorf("detail %w", err)
		}
	}
	if !languageFeatureSymbolKinds[item.Kind] {
		return LanguageSymbol{}, nil, fmt.Errorf("kind %q is not a known symbol kind", item.Kind)
	}
	out.Kind = item.Kind
	if out.Range, err = parseLanguageRange(item.Range); err != nil {
		return LanguageSymbol{}, nil, err
	}
	out.SelectionRange = out.Range
	if len(item.SelectionRange) > 0 {
		if out.SelectionRange, err = parseLanguageRange(item.SelectionRange); err != nil {
			return LanguageSymbol{}, nil, fmt.Errorf("selectionRange: %w", err)
		}
		if !languageRangeContains(out.Range, out.SelectionRange) {
			return LanguageSymbol{}, nil, errors.New("selectionRange must lie inside range")
		}
	}
	return out, item.Children, nil
}
