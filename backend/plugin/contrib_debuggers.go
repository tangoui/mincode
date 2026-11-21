package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"mincode/app/backend/debug"
)

// Debuggers (contributes.debuggers) let a process plugin that declares the
// shell capability offer a Debug Adapter Protocol adapter for editor
// languages. An entry only describes the adapter; the plugin process never
// runs anything that lives for the session:
//
//   - adapter.kind is one of the contract adapterKinds ("stdio", "tcp",
//     "jdtls");
//   - a jdtls adapter starts no process: its command is a jdtls command this
//     plugin declares in an accepted contributes.jdtlsBundles entry, without
//     args or env; the host runs it in the built-in jdtls after confirmation
//     and connects to the port it returns (debuggers_jdtls.go);
//   - adapter.command is a bare program name, "${pluginRoot}/<relative>" or
//     exactly "${resolved.<name>}", a value returned by the plugin's
//     resolveAction that must be an absolute path to an existing regular
//     executable (absolute paths cannot be written in the manifest); args may
//     use ${pluginRoot}, ${projectRoot}, ${resolved.<name>} and, for tcp
//     adapters only, ${port} (required there); env values may use
//     ${pluginRoot} and ${projectRoot}; env names follow the shared denylist;
//   - resolveAction (required), entryPointsAction, checkAction and
//     settingsAction name this plugin's own contributes.actions with risk
//     "read" that are not aiCallable; resolve must have no side effects;
//     settingsAction must allow settingsPayloadMinBytes of payload so a full
//     exception type list fits;
//   - exceptionTypes ({placeholder?, description?} plus *Localized) declares
//     the host's "pause only on these exception types" list for this type and
//     requires settingsAction, which receives the project's saved list
//     (debuggers_launch.go applyDebuggerSettings); the host treats the items
//     only as dotted identifiers, never as names of a particular language;
//   - functionBreakpoints (the same text hints) only words the host's method
//     breakpoint input; whether it is offered depends on the adapter's
//     supportsFunctionBreakpoints, and the host never parses the names;
//   - hotCodeReplace ({event, request, modeSetting?}) names the adapter's
//     custom DAP event that reports recompiled code and the custom request
//     that applies it; each matches ^[a-zA-Z][a-zA-Z0-9]*$, is at most 64
//     characters and is not a DAP standard request or event name
//     (debug.IsStandardDAPName). modeSetting names this plugin's own
//     contributes.configuration property holding the mode the frontend
//     follows: a global, non-secret string with enum exactly auto, manual,
//     never (without it the mode is auto);
//   - configurationSchema ({launch?, attach?}) and configurationSnippets are
//     data for the run-configuration editor, bounded by the contract;
//   - the host resolves, confirms (fingerprint) and starts every adapter
//     itself (debuggers_launch.go); AI tools cannot list or start debuggers.

const (
	debuggerPointKey      = "debuggers"
	debuggerVarPort       = "${port}"
	debuggerKindTCP       = "tcp"
	debuggerKindJdtls     = "jdtls"
	debuggerResolvedNoun  = "${resolved.<name>}"
	debuggerJSONMaxDepth  = 16
	debuggerRequestLaunch = "launch"
	debuggerRequestAttach = "attach"
	// debuggerHotCodeReplaceNameMax bounds a hotCodeReplace event or request
	// name (ASCII, so bytes are characters).
	debuggerHotCodeReplaceNameMax = 64
)

var (
	debuggerSpec             = mustContributionSpec(debuggerPointKey)
	debuggerTitleMaxRunes    = int(debuggerSpec.Limit("titleMaxRunes"))
	debuggerLocalesMax       = int(debuggerSpec.Limit("localesMax"))
	debuggerLanguagesMax     = int(debuggerSpec.Limit("languagesMax"))
	debuggerArgMaxRunes      = int(debuggerSpec.Limit("argMaxRunes"))
	debuggerSchemaMaxBytes   = int(debuggerSpec.Limit("schemaMaxBytes"))
	debuggerSnippetsMax      = int(debuggerSpec.Limit("snippetsMax"))
	debuggerSnippetMaxBytes  = int(debuggerSpec.Limit("snippetMaxBytes"))
	debuggerPlaceholderRunes = int(debuggerSpec.Limit("placeholderMaxRunes"))
	debuggerDescriptionRunes = int(debuggerSpec.Limit("descriptionMaxRunes"))
	debuggerSettingsMinBytes = int(debuggerSpec.Limit("settingsPayloadMinBytes"))
	debuggerTypePattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	debuggerResolvedName     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]{0,31}$`)
	debuggerResolvedCommand  = regexp.MustCompile(`^\$\{resolved\.([^}]*)\}$`)
	debuggerResolvedVariable = regexp.MustCompile(`\$\{resolved\.([^}]*)\}`)
	// debuggerHotCodeReplaceName is the syntax of a custom DAP event or
	// request name in hotCodeReplace (at most debuggerHotCodeReplaceNameMax).
	debuggerHotCodeReplaceName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*$`)
)

// DebuggerContribution is one validated contributes.debuggers entry as kept
// in the snapshot, with its adapter launch unresolved (variables intact).
// SpecFingerprint hashes the canonical entry.
type DebuggerContribution struct {
	Type                  string                  `json:"type"`
	Title                 string                  `json:"title"`
	TitleLocalized        map[string]string       `json:"titleLocalized,omitempty"`
	Languages             []string                `json:"languages"`
	Adapter               DebuggerAdapter         `json:"adapter"`
	ResolveAction         string                  `json:"resolveAction"`
	EntryPointsAction     string                  `json:"entryPointsAction,omitempty"`
	CheckAction           string                  `json:"checkAction,omitempty"`
	SettingsAction        string                  `json:"settingsAction,omitempty"`
	ExceptionTypes        *DebuggerTextHints      `json:"exceptionTypes,omitempty"`
	FunctionBreakpoints   *DebuggerTextHints      `json:"functionBreakpoints,omitempty"`
	HotCodeReplace        *DebuggerHotCodeReplace `json:"hotCodeReplace,omitempty"`
	ConfigurationSchema   json.RawMessage         `json:"configurationSchema,omitempty"`
	ConfigurationSnippets []json.RawMessage       `json:"configurationSnippets"`
	Remote                *DebuggerRemote         `json:"remote,omitempty"`
	Platforms             []string                `json:"platforms"`
	SpecFingerprint       string                  `json:"specFingerprint"`
}

// DebuggerTextHints are plugin texts for a host-drawn input (exceptionTypes:
// the "pause only on these exception types" list; functionBreakpoints: the
// method breakpoint name), all optional single-line text (placeholder at most
// placeholderMaxRunes, description at most descriptionMaxRunes).
type DebuggerTextHints struct {
	Placeholder          string            `json:"placeholder,omitempty"`
	PlaceholderLocalized map[string]string `json:"placeholderLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
}

// DebuggerHotCodeReplace names the adapter's custom DAP event that reports
// recompiled code and the custom request that applies it. Both are custom
// names (debuggerHotCodeReplaceName, not a DAP standard name).
type DebuggerHotCodeReplace struct {
	Event   string `json:"event"`
	Request string `json:"request"`
	// ModeSetting is the plugin's configuration key with the mode (empty: auto).
	ModeSetting string `json:"modeSetting,omitempty"`
}

type debuggerHotCodeReplaceManifest struct {
	Event       *string `json:"event"`
	Request     *string `json:"request"`
	ModeSetting *string `json:"modeSetting"`
}

// debuggerHotCodeReplaceModes are the values a hotCodeReplace modeSetting
// property must offer, exactly.
var debuggerHotCodeReplaceModes = []string{"auto", "manual", "never"}

// DebuggerAdapter is the unresolved adapter launch of one entry.
type DebuggerAdapter struct {
	Kind    string            `json:"kind"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// DebuggerRemote tells the host which SSH forward a remote attach needs and
// which server-side arguments to show (never run by the host).
type DebuggerRemote struct {
	Forward      string `json:"forward"`
	ArgsTemplate string `json:"argsTemplate,omitempty"`
}

type debuggerManifestEntry struct {
	Type                  string                          `json:"type"`
	Title                 string                          `json:"title"`
	TitleLocalized        map[string]string               `json:"titleLocalized"`
	Languages             []string                        `json:"languages"`
	Adapter               *debuggerAdapterManifest        `json:"adapter"`
	ResolveAction         *string                         `json:"resolveAction"`
	EntryPointsAction     *string                         `json:"entryPointsAction"`
	CheckAction           *string                         `json:"checkAction"`
	SettingsAction        *string                         `json:"settingsAction"`
	ExceptionTypes        *DebuggerTextHints              `json:"exceptionTypes"`
	FunctionBreakpoints   *DebuggerTextHints              `json:"functionBreakpoints"`
	HotCodeReplace        *debuggerHotCodeReplaceManifest `json:"hotCodeReplace"`
	ConfigurationSchema   json.RawMessage                 `json:"configurationSchema"`
	ConfigurationSnippets []json.RawMessage               `json:"configurationSnippets"`
	Remote                *DebuggerRemote                 `json:"remote"`
	Platforms             []string                        `json:"platforms"`
}

type debuggerAdapterManifest struct {
	Kind    string            `json:"kind"`
	Command *string           `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type debuggerSnippetManifest struct {
	Label          string            `json:"label"`
	LabelLocalized map[string]string `json:"labelLocalized,omitempty"`
	Request        string            `json:"request"`
	Body           json.RawMessage   `json:"body"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              debuggerPointKey,
		Decode:           decodeDebuggerEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("debugger type %q is duplicated", id) },
		IssuePrefix:      "invalid debuggers contribution",
		IssueText:        debuggerIssueText,
		EntryNoun:        "debuggers",
		// jdtls adapters name commands of this plugin's jdtlsBundles;
		// hotCodeReplace.modeSetting names a property of its configuration.
		DependsOn: []string{jdtlsBundlePointKey, configurationPointKey},
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				debugger, ok := entry.(DebuggerContribution)
				return debugger.Type, debuggerActions(debugger), ok
			})
		},
	})
}

func debuggerIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("debugger %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("debugger entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeDebuggerEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry debuggerManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.Type), fmt.Errorf("parse debugger: %w", err)
	}
	id := strings.TrimSpace(entry.Type)
	if !debuggerTypePattern.MatchString(id) {
		return nil, id, fmt.Errorf("debugger type %q must match %s", id, debuggerTypePattern)
	}
	fail := func(format string, args ...any) (any, string, error) {
		return nil, id, fmt.Errorf("debugger %q "+format, append([]any{id}, args...)...)
	}
	debugger := DebuggerContribution{Type: id, ConfigurationSnippets: []json.RawMessage{}}
	var err error
	if debugger.Title, err = validateContributionLine(entry.Title, debuggerTitleMaxRunes, true); err != nil {
		return fail("title %w", err)
	}
	if debugger.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, debuggerTitleMaxRunes, debuggerLocalesMax); err != nil {
		return fail("titleLocalized %w", err)
	}
	if debugger.Languages, err = validateContributionLanguageIDs(entry.Languages, debuggerLanguagesMax); err != nil {
		return fail("%w", err)
	}
	if debugger.Platforms, err = normalizeContributionPlatforms(debuggerSpec, entry.Platforms); err != nil {
		return fail("%w", err)
	}
	if entry.Adapter == nil {
		return fail("adapter is required")
	}
	if entry.Adapter.Command == nil {
		return fail("adapter command is required")
	}
	if debugger.Adapter, err = validateDebuggerAdapter(ctx.root, entry.Adapter.Kind, *entry.Adapter.Command, entry.Adapter.Args, entry.Adapter.Env); err != nil {
		return fail("adapter %w", err)
	}
	if debugger.Adapter.Kind == debuggerKindJdtls && !slices.Contains(debuggerDeclaredJdtlsCommands(ctx), debugger.Adapter.Command) {
		return fail("adapter command %q must be declared in this plugin's contributes.jdtlsBundles", debugger.Adapter.Command)
	}
	if entry.ResolveAction == nil {
		return fail("resolveAction is required")
	}
	if debugger.ResolveAction, err = debuggerActionRef(ctx, id, "resolveAction", *entry.ResolveAction); err != nil {
		return nil, id, err
	}
	if entry.EntryPointsAction != nil {
		if debugger.EntryPointsAction, err = debuggerActionRef(ctx, id, "entryPointsAction", *entry.EntryPointsAction); err != nil {
			return nil, id, err
		}
	}
	if entry.CheckAction != nil {
		if debugger.CheckAction, err = debuggerActionRef(ctx, id, "checkAction", *entry.CheckAction); err != nil {
			return nil, id, err
		}
	}
	if entry.SettingsAction != nil {
		if debugger.SettingsAction, err = debuggerActionRef(ctx, id, "settingsAction", *entry.SettingsAction); err != nil {
			return nil, id, err
		}
		if size := ctx.actions[debugger.SettingsAction].MaxPayloadBytes; size < debuggerSettingsMinBytes {
			return fail("settingsAction %q needs maxPayloadBytes of at least %d (a full exception type list), has %d", debugger.SettingsAction, debuggerSettingsMinBytes, size)
		}
	}
	if entry.ExceptionTypes != nil {
		if debugger.SettingsAction == "" {
			return fail("exceptionTypes requires settingsAction, which receives the list")
		}
		exceptionTypes, err := validateDebuggerTextHints(*entry.ExceptionTypes)
		if err != nil {
			return fail("exceptionTypes %w", err)
		}
		debugger.ExceptionTypes = &exceptionTypes
	}
	if entry.FunctionBreakpoints != nil {
		hints, err := validateDebuggerTextHints(*entry.FunctionBreakpoints)
		if err != nil {
			return fail("functionBreakpoints %w", err)
		}
		debugger.FunctionBreakpoints = &hints
	}
	if entry.HotCodeReplace != nil {
		hotCodeReplace, err := validateDebuggerHotCodeReplace(*entry.HotCodeReplace)
		if err != nil {
			return fail("hotCodeReplace %w", err)
		}
		if entry.HotCodeReplace.ModeSetting != nil {
			if hotCodeReplace.ModeSetting, err = debuggerHotCodeReplaceModeSetting(ctx, *entry.HotCodeReplace.ModeSetting); err != nil {
				return fail("hotCodeReplace modeSetting %w", err)
			}
		}
		debugger.HotCodeReplace = &hotCodeReplace
	}
	if debugger.ConfigurationSchema, err = canonicalDebuggerSchema(entry.ConfigurationSchema); err != nil {
		return fail("configurationSchema %w", err)
	}
	if len(entry.ConfigurationSnippets) > debuggerSnippetsMax {
		return fail("configurationSnippets has more than %d entries", debuggerSnippetsMax)
	}
	for index, rawSnippet := range entry.ConfigurationSnippets {
		snippet, err := canonicalDebuggerSnippet(rawSnippet)
		if err != nil {
			return fail("configurationSnippets[%d] %w", index, err)
		}
		debugger.ConfigurationSnippets = append(debugger.ConfigurationSnippets, snippet)
	}
	if entry.Remote != nil {
		remote, err := validateDebuggerRemote(*entry.Remote)
		if err != nil {
			return fail("remote %w", err)
		}
		debugger.Remote = &remote
	}
	debugger.SpecFingerprint = debuggerSpecFingerprint(debugger)
	return debugger, id, nil
}

// validateContributionLanguageIDs accepts 1..max unique editor language ids
// (the contributes.languages id syntax).
func validateContributionLanguageIDs(languages []string, max int) ([]string, error) {
	if len(languages) == 0 {
		return nil, errors.New("languages must list at least one language id")
	}
	if len(languages) > max {
		return nil, fmt.Errorf("languages has more than %d ids", max)
	}
	out := make([]string, 0, len(languages))
	for _, language := range languages {
		if !languageIDPattern.MatchString(language) {
			return nil, fmt.Errorf("languages id %q must match %s", language, languageIDPattern)
		}
		if slices.Contains(out, language) {
			return nil, fmt.Errorf("languages lists %q more than once", language)
		}
		out = append(out, language)
	}
	return out, nil
}

// debuggerActionRef accepts only this plugin's own read-risk actions that
// the model cannot call: the host calls them on explicit user actions only.
func debuggerActionRef(ctx *pointContext, id, field, value string) (string, error) {
	actionID, err := contributionReadActionRef(ctx, "debugger", id, field, value)
	if err != nil {
		return "", err
	}
	if ctx.actions[actionID].AICallable {
		return "", fmt.Errorf("debugger %q %s %q must not be aiCallable", id, field, actionID)
	}
	return actionID, nil
}

// debuggerActions lists the entry's action references (empty when unset).
func debuggerActions(debugger DebuggerContribution) []string {
	return []string{debugger.ResolveAction, debugger.EntryPointsAction, debugger.CheckAction, debugger.SettingsAction}
}

// validateDebuggerTextHints validates input texts as trimmed single lines.
func validateDebuggerTextHints(declared DebuggerTextHints) (DebuggerTextHints, error) {
	var out DebuggerTextHints
	var err error
	if out.Placeholder, err = validateContributionLine(declared.Placeholder, debuggerPlaceholderRunes, false); err != nil {
		return DebuggerTextHints{}, fmt.Errorf("placeholder %w", err)
	}
	if out.PlaceholderLocalized, err = validateContributionLocalized(declared.PlaceholderLocalized, debuggerPlaceholderRunes, debuggerLocalesMax); err != nil {
		return DebuggerTextHints{}, fmt.Errorf("placeholderLocalized %w", err)
	}
	if out.Description, err = validateContributionLine(declared.Description, debuggerDescriptionRunes, false); err != nil {
		return DebuggerTextHints{}, fmt.Errorf("description %w", err)
	}
	if out.DescriptionLocalized, err = validateContributionLocalized(declared.DescriptionLocalized, debuggerDescriptionRunes, debuggerLocalesMax); err != nil {
		return DebuggerTextHints{}, fmt.Errorf("descriptionLocalized %w", err)
	}
	return out, nil
}

// validateDebuggerHotCodeReplace requires both names; each must be a custom
// DAP name. Names are not trimmed: the host sends and matches them verbatim.
func validateDebuggerHotCodeReplace(declared debuggerHotCodeReplaceManifest) (DebuggerHotCodeReplace, error) {
	if declared.Event == nil {
		return DebuggerHotCodeReplace{}, errors.New("event is required")
	}
	if declared.Request == nil {
		return DebuggerHotCodeReplace{}, errors.New("request is required")
	}
	for _, member := range [...]struct{ field, name string }{{"event", *declared.Event}, {"request", *declared.Request}} {
		field, name := member.field, member.name
		if len(name) > debuggerHotCodeReplaceNameMax {
			return DebuggerHotCodeReplace{}, fmt.Errorf("%s must be at most %d characters", field, debuggerHotCodeReplaceNameMax)
		}
		if !debuggerHotCodeReplaceName.MatchString(name) {
			return DebuggerHotCodeReplace{}, fmt.Errorf("%s %q must match %s", field, name, debuggerHotCodeReplaceName)
		}
		if debug.IsStandardDAPName(name) {
			return DebuggerHotCodeReplace{}, fmt.Errorf("%s %q is a DAP standard request or event name; declare a custom name", field, name)
		}
	}
	return DebuggerHotCodeReplace{Event: *declared.Event, Request: *declared.Request}, nil
}

// debuggerHotCodeReplaceModeSetting checks that key names a property of this
// plugin's accepted contributes.configuration (resolved before debuggers,
// see DependsOn) that the frontend can read as the mode: a global,
// non-secret string whose enum is exactly debuggerHotCodeReplaceModes.
func debuggerHotCodeReplaceModeSetting(ctx *pointContext, key string) (string, error) {
	var schema *ConfigurationSchema
	if resolved := ctx.resolved[configurationPointKey]; resolved != nil && len(resolved.Entries) == 1 {
		if accepted, ok := resolved.Entries[0].(ConfigurationSchema); ok {
			schema = &accepted
		}
	}
	if schema == nil {
		return "", fmt.Errorf("%q needs this plugin's contributes.configuration", key)
	}
	property := schema.Properties[key]
	if property == nil {
		return "", fmt.Errorf("%q is not a property of this plugin's contributes.configuration", key)
	}
	enum := make([]string, 0, len(property.Enum))
	for _, value := range property.Enum {
		if text, ok := value.(string); ok {
			enum = append(enum, text)
		}
	}
	slices.Sort(enum)
	want := slices.Clone(debuggerHotCodeReplaceModes)
	slices.Sort(want)
	switch {
	case property.Type != "string":
		return "", fmt.Errorf("%q must be a string setting, is %s", key, property.Type)
	case len(property.Enum) != len(want) || !slices.Equal(enum, want):
		return "", fmt.Errorf("%q must have enum exactly %s", key, strings.Join(debuggerHotCodeReplaceModes, ", "))
	case property.Scope != configurationScopeGlobal:
		return "", fmt.Errorf("%q must have scope global, has %s", key, property.Scope)
	case property.Secret:
		return "", fmt.Errorf("%q must not be a secret", key)
	}
	return key, nil
}

// debuggerDeclaredJdtlsCommands lists the commands of the manifest's
// accepted jdtlsBundles entries (resolved before debuggers, see DependsOn).
func debuggerDeclaredJdtlsCommands(ctx *pointContext) []string {
	resolved := ctx.resolved[jdtlsBundlePointKey]
	if resolved == nil {
		return nil
	}
	commands := []string{}
	for _, entry := range resolved.Entries {
		if bundle, ok := entry.(JdtlsBundleContribution); ok {
			commands = append(commands, bundle.Commands...)
		}
	}
	return commands
}

// validateDebuggerAdapter validates an adapter with the current contract
// lists, at install and again at every prepare. root is the plugin directory.
// For kind jdtls it checks only the command syntax; the caller checks that
// the plugin declares the command.
func validateDebuggerAdapter(root, kind, rawCommand string, args []string, env map[string]string) (DebuggerAdapter, error) {
	if !slices.Contains(debuggerSpec.List("adapterKinds"), kind) {
		return DebuggerAdapter{}, fmt.Errorf("kind %q is not supported by this host (supported: %s)", kind, strings.Join(debuggerSpec.List("adapterKinds"), ", "))
	}
	command := strings.TrimSpace(rawCommand)
	if kind == debuggerKindJdtls {
		if !jdtlsBundleCommandPattern.MatchString(command) {
			return DebuggerAdapter{}, fmt.Errorf("command %q of a jdtls adapter must be a jdtls command id matching %s", command, jdtlsBundleCommandPattern)
		}
		if len(args) > 0 || len(env) > 0 {
			return DebuggerAdapter{}, errors.New("args and env are not allowed for a jdtls adapter")
		}
		return DebuggerAdapter{Kind: kind, Command: command, Args: []string{}, Env: map[string]string{}}, nil
	}
	if _, ok, err := debuggerResolvedCommandName(command); err != nil {
		return DebuggerAdapter{}, err
	} else if !ok {
		if strings.Contains(command, "${resolved.") {
			return DebuggerAdapter{}, fmt.Errorf("command may use %s only as the whole command", debuggerResolvedNoun)
		}
		if command, err = validateProjectProcessCommand(debuggerSpec, root, command); err != nil {
			return DebuggerAdapter{}, err
		}
	}
	envVariables := []string{}
	argVariables := []string{}
	for _, variable := range debuggerSpec.List("variables") {
		if variable != debuggerVarPort {
			envVariables = append(envVariables, variable)
			argVariables = append(argVariables, variable)
		}
	}
	usesPort := false
	for index, arg := range args {
		if strings.Contains(arg, debuggerVarPort) {
			if kind != debuggerKindTCP {
				return DebuggerAdapter{}, fmt.Errorf("args[%d] uses %s, which only tcp adapters may use", index, debuggerVarPort)
			}
			usesPort = true
		}
		for _, match := range debuggerResolvedVariable.FindAllStringSubmatch(arg, -1) {
			if !debuggerResolvedName.MatchString(match[1]) {
				return DebuggerAdapter{}, fmt.Errorf("args[%d] variable %s must name ${resolved.<name>} with <name> matching %s", index, match[0], debuggerResolvedName)
			}
			if !slices.Contains(argVariables, match[0]) {
				argVariables = append(argVariables, match[0])
			}
		}
	}
	if kind == debuggerKindTCP {
		if !usesPort {
			return DebuggerAdapter{}, fmt.Errorf("args of a tcp adapter must pass %s to the adapter", debuggerVarPort)
		}
		argVariables = append(argVariables, debuggerVarPort)
	}
	definition, err := validateProjectProcessArgsEnv(debuggerSpec, args, env, argVariables, envVariables)
	if err != nil {
		return DebuggerAdapter{}, err
	}
	return DebuggerAdapter{Kind: kind, Command: command, Args: definition.Args, Env: definition.Env}, nil
}

// debuggerResolvedCommandName reports whether command is exactly
// ${resolved.<name>} and returns the name.
func debuggerResolvedCommandName(command string) (string, bool, error) {
	match := debuggerResolvedCommand.FindStringSubmatch(command)
	if match == nil {
		return "", false, nil
	}
	if !debuggerResolvedName.MatchString(match[1]) {
		return "", false, fmt.Errorf("command %s must match ${resolved.<name>} with <name> matching %s", command, debuggerResolvedName)
	}
	return match[1], true, nil
}

// canonicalDebuggerSchema accepts {launch?: object, attach?: object} with at
// least one member, at most schemaMaxBytes in canonical form.
func canonicalDebuggerSchema(raw json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalContributionObject(raw, debuggerSchemaMaxBytes, debuggerJSONMaxDepth)
	if err != nil || canonical == nil {
		return canonical, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &members); err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, errors.New("must declare launch or attach")
	}
	for key, value := range members {
		if key != debuggerRequestLaunch && key != debuggerRequestAttach {
			return nil, fmt.Errorf("member %q must be launch or attach", key)
		}
		if !strings.HasPrefix(string(value), "{") {
			return nil, fmt.Errorf("%s must be a JSON object", key)
		}
	}
	return canonical, nil
}

// canonicalDebuggerSnippet validates {label, labelLocalized?, request, body}
// and returns its canonical form (at most snippetMaxBytes).
func canonicalDebuggerSnippet(raw json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalContributionObject(raw, debuggerSnippetMaxBytes, debuggerJSONMaxDepth)
	if err != nil {
		return nil, err
	}
	if canonical == nil {
		return nil, errors.New("must be a JSON object")
	}
	var snippet debuggerSnippetManifest
	if err := decodeStrictJSON(canonical, &snippet, true); err != nil {
		return nil, err
	}
	if snippet.Label, err = validateContributionLine(snippet.Label, debuggerTitleMaxRunes, true); err != nil {
		return nil, fmt.Errorf("label %w", err)
	}
	if snippet.LabelLocalized, err = validateContributionLocalized(snippet.LabelLocalized, debuggerTitleMaxRunes, debuggerLocalesMax); err != nil {
		return nil, fmt.Errorf("labelLocalized %w", err)
	}
	if snippet.Request != debuggerRequestLaunch && snippet.Request != debuggerRequestAttach {
		return nil, fmt.Errorf("request %q must be launch or attach", snippet.Request)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(snippet.Body)), "{") {
		return nil, errors.New("body must be a JSON object")
	}
	out, err := json.Marshal(snippet)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateDebuggerRemote(remote DebuggerRemote) (DebuggerRemote, error) {
	if !slices.Contains(debuggerSpec.List("remoteForwards"), remote.Forward) {
		return DebuggerRemote{}, fmt.Errorf("forward %q must be one of %s", remote.Forward, strings.Join(debuggerSpec.List("remoteForwards"), ", "))
	}
	template, err := validateContributionLine(remote.ArgsTemplate, debuggerArgMaxRunes, false)
	if err != nil {
		return DebuggerRemote{}, fmt.Errorf("argsTemplate %w", err)
	}
	return DebuggerRemote{Forward: remote.Forward, ArgsTemplate: template}, nil
}

func debuggerSpecFingerprint(debugger DebuggerContribution) string {
	copy := debugger
	copy.SpecFingerprint = ""
	canonical, _ := json.Marshal(copy)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
