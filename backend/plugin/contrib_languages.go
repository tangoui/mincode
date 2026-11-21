package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Editor languages (contributes.languages) are data only: each entry names a
// Monaco language id with its file associations and, for a new id, optional
// Monarch grammar and language configuration files (strict JSON). Built-in ids
// (contract reservedIds) are association-only: a plugin can map more
// extensions or filenames to them but cannot replace their tokenizer or
// configuration. Declarative and process plugins may contribute; the point is
// global.
//
// The host bounds the structure of the grammar and rejects the Monarch
// features that turn plugin data into runtime-built regular expressions or
// unbounded work: state-substituted rule regexes ($Sn), guard regexes built
// from matched text, include cycles and include blow-up, and log actions.
// Regex syntax and catastrophic backtracking are checked by the frontend
// consumer (vue-app/src/domain/editor/regex-safety.ts) because the patterns
// are JavaScript regular expressions. Grammar and configuration JSON keep the
// file's key order (Monarch evaluates cases and states in declaration order).

const languagePointKey = "languages"

var (
	languageSpec                = mustContributionSpec(languagePointKey)
	languageAliasesMax          = int(languageSpec.Limit("aliasesMax"))
	languageAliasMaxRunes       = int(languageSpec.Limit("aliasMaxRunes"))
	languageExtensionsMax       = int(languageSpec.Limit("extensionsMax"))
	languageFilenamesMax        = int(languageSpec.Limit("filenamesMax"))
	languageFilenameMaxLength   = int(languageSpec.Limit("filenameMaxLength"))
	languagePatternsMax         = int(languageSpec.Limit("patternsMax"))
	languagePatternMaxLength    = int(languageSpec.Limit("patternMaxLength"))
	languageStatesMax           = int(languageSpec.Limit("grammarStatesMax"))
	languageRulesMax            = int(languageSpec.Limit("grammarRulesMax"))
	languageExpandedRulesMax    = int(languageSpec.Limit("grammarExpandedRulesMax"))
	languageRegexMaxLength      = int(languageSpec.Limit("regexMaxLength"))
	languageRegexExpandedMax    = int(languageSpec.Limit("regexExpandedMaxLength"))
	languageTokenMaxLength      = int(languageSpec.Limit("tokenMaxLength"))
	languageAttributeArrayMax   = int(languageSpec.Limit("attributeArrayMax"))
	languageCasesDepthMax       = int(languageSpec.Limit("casesDepthMax"))
	languageJSONDepthMax        = int(languageSpec.Limit("jsonDepthMax"))
	languagePairsMax            = int(languageSpec.Limit("configurationPairsMax"))
	languagePerPluginBytes      = int(languageSpec.Limit("perPluginGrammarBytes"))
	languageGoBackMax           = int(languageSpec.Limit("goBackMax"))
	languageReservedIDs         = stringSet(languageSpec.ReservedIDs)
	languagePairMaxRunes        = 8
	languageCommentMaxRunes     = 16
	languageAutoCloseBeforeMax  = 64
	languageStateNameMaxLength  = 64
	languageActionGroupMax      = 16
	languageIDPattern           = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)
	languageEmbeddedPattern     = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	languageExtensionPattern    = regexp.MustCompile(`^\.[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$`)
	languageFilenamePatternChar = regexp.MustCompile(`^[A-Za-z0-9._\-/*?+@]+$`)
	monarchNamePattern          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	monarchStatePattern         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	monarchStackPattern         = regexp.MustCompile(`^(@pop|@push|@popall)$`)
	monarchStateSubstitution    = regexp.MustCompile(`\$[sS]\d`)
	monarchMatchSubstitution    = regexp.MustCompile(`\$(\$|#|\d|[sS]\d|@\w)`)
	monarchGuardScrutinee       = regexp.MustCompile(`^\$(([sS]?)(\d\d?)|#)(.*)$`)
	monarchGuardWord            = regexp.MustCompile(`^\w*$`)
	monarchGuardWords           = regexp.MustCompile(`^(\w|\|)*$`)
	monarchGuardOperator        = regexp.MustCompile(`^(@|!@|~|!~|==|!=)(.*)$`)
	monarchAttributeReference   = regexp.MustCompile(`@(\w+)`)
)

// monarchReservedAttributes are lexer fields Monaco sets itself or JavaScript
// object members a grammar attribute must not shadow.
var monarchReservedAttributes = stringSet([]string{
	"languageId", "noThrow", "usesEmbedded", "stateNames", "maxStack", "log",
	"constructor", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable",
	"toLocaleString", "toString", "valueOf",
})

// monarchReservedStates are Object/Array members: Monaco keeps compiled
// states on an array and looks state names up on plain objects.
var monarchReservedStates = stringSet([]string{
	"constructor", "length", "at", "concat", "copyWithin", "entries", "every", "fill", "filter",
	"find", "findIndex", "findLast", "findLastIndex", "flat", "flatMap", "forEach", "includes",
	"indexOf", "join", "keys", "lastIndexOf", "map", "pop", "push", "reduce", "reduceRight",
	"reverse", "shift", "slice", "some", "sort", "splice", "toLocaleString", "toReversed",
	"toSorted", "toSpliced", "toString", "unshift", "values", "with", "hasOwnProperty",
	"isPrototypeOf", "propertyIsEnumerable", "valueOf",
})

// LanguageContribution is one validated contributes.languages entry as sent to
// the frontend consumer. Grammar and Configuration are the compacted file JSON
// with the original key order.
type LanguageContribution struct {
	ID                string          `json:"id"`
	Aliases           []string        `json:"aliases"`
	Extensions        []string        `json:"extensions"`
	Filenames         []string        `json:"filenames"`
	FilenamePatterns  []string        `json:"filenamePatterns"`
	GrammarPath       string          `json:"grammarPath,omitempty"`
	Grammar           json.RawMessage `json:"grammar,omitempty"`
	ConfigurationPath string          `json:"configurationPath,omitempty"`
	Configuration     json.RawMessage `json:"configuration,omitempty"`
}

type languageEntryReference struct {
	ID               string    `json:"id"`
	Aliases          *[]string `json:"aliases"`
	Extensions       []string  `json:"extensions"`
	Filenames        []string  `json:"filenames"`
	FilenamePatterns []string  `json:"filenamePatterns"`
	Grammar          *string   `json:"grammar"`
	Configuration    *string   `json:"configuration"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              languagePointKey,
		Decode:           decodeLanguageEntry,
		Finalize:         finalizeLanguageEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate language id %q", id) },
		IssuePrefix:      "invalid languages contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped language %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.languages[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "languages",
		FileLabels: contributionFileLabels{Noun: "language", Entry: "language entry"},
	})
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// decodeLanguageEntry validates one entry and its grammar/configuration files.
func decodeLanguageEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry languageEntryReference
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse language entry: %w", err)
	}
	id := entry.ID
	if !languageIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("language id %q must match %s", id, languageIDPattern)
	}
	contribution := LanguageContribution{ID: id, Aliases: []string{}}
	var err error
	if contribution.Extensions, err = validateLanguageExtensions(entry.Extensions); err != nil {
		return nil, id, err
	}
	if contribution.Filenames, err = validateLanguageFilenames(entry.Filenames); err != nil {
		return nil, id, err
	}
	if contribution.FilenamePatterns, err = validateLanguageFilenamePatterns(entry.FilenamePatterns); err != nil {
		return nil, id, err
	}
	if _, reserved := languageReservedIDs[id]; reserved {
		if entry.Aliases != nil || entry.Grammar != nil || entry.Configuration != nil {
			return nil, id, fmt.Errorf("language id %q is built in (reserved): only extensions, filenames and filenamePatterns may be contributed", id)
		}
		if len(contribution.Extensions)+len(contribution.Filenames)+len(contribution.FilenamePatterns) == 0 {
			return nil, id, fmt.Errorf("language id %q is built in (reserved): contribute at least one extension, filename or filenamePattern", id)
		}
		return contribution, id, nil
	}
	if contribution.Aliases, err = validateLanguageAliases(entry.Aliases); err != nil {
		return nil, id, err
	}
	if entry.Grammar != nil {
		path := strings.TrimSpace(*entry.Grammar)
		data, err := readLanguageFile(ctx, path, "language grammar", validateMonarchGrammar)
		if err != nil {
			return nil, id, err
		}
		contribution.GrammarPath, contribution.Grammar = path, data
	}
	if entry.Configuration != nil {
		path := strings.TrimSpace(*entry.Configuration)
		data, err := readLanguageFile(ctx, path, "language configuration", validateLanguageConfiguration)
		if err != nil {
			return nil, id, err
		}
		contribution.ConfigurationPath, contribution.Configuration = path, data
	}
	return contribution, id, nil
}

// readLanguageFile reads, strictly parses and validates a grammar or
// configuration file and returns it compacted with its key order intact.
func readLanguageFile(ctx *pointContext, path, noun string, validate func(*languageJSONNode) error) (json.RawMessage, error) {
	data, err := ctx.readFile(path, noun)
	if err != nil {
		return nil, err
	}
	root, err := parseLanguageJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s file %q: %w", noun, path, err)
	}
	if err := validate(root); err != nil {
		return nil, fmt.Errorf("%s file %q: %w", noun, path, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, fmt.Errorf("%s file %q: %w", noun, path, err)
	}
	return json.RawMessage(compact.Bytes()), nil
}

// finalizeLanguageEntries enforces the per-plugin grammar and configuration
// byte total in entry order; entries past the limit are skipped.
func finalizeLanguageEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	total := 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		value := entry.Value.(LanguageContribution)
		size := len(value.Grammar) + len(value.Configuration)
		if total+size > languagePerPluginBytes {
			issues = append(issues, ContributionIssue{
				Point: languagePointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin language grammars and configurations exceed %d bytes in total", languagePerPluginBytes),
			})
			continue
		}
		total += size
		kept = append(kept, entry)
	}
	return kept, issues
}

func validateLanguageAliases(values *[]string) ([]string, error) {
	if values == nil || len(*values) == 0 || len(*values) > languageAliasesMax {
		return nil, fmt.Errorf("a new language needs 1-%d aliases (display names)", languageAliasesMax)
	}
	aliases := make([]string, 0, len(*values))
	seen := map[string]struct{}{}
	for _, value := range *values {
		alias := strings.TrimSpace(value)
		if alias == "" || utf8.RuneCountInString(alias) > languageAliasMaxRunes || strings.IndexFunc(alias, func(r rune) bool {
			return isStatusBarUnsafeRune(r, false)
		}) >= 0 {
			return nil, fmt.Errorf("language alias %q must be 1-%d characters without control or bidirectional override characters", value, languageAliasMaxRunes)
		}
		if _, duplicate := seen[alias]; duplicate {
			return nil, fmt.Errorf("duplicate language alias %q", alias)
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases, nil
}

func validateLanguageExtensions(values []string) ([]string, error) {
	if len(values) > languageExtensionsMax {
		return nil, fmt.Errorf("a language may list at most %d extensions", languageExtensionsMax)
	}
	extensions := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if !languageExtensionPattern.MatchString(value) {
			return nil, fmt.Errorf("language extension %q must match %s", value, languageExtensionPattern)
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate language extension %q", value)
		}
		seen[key] = struct{}{}
		extensions = append(extensions, value)
	}
	return extensions, nil
}

func validateLanguageFilenames(values []string) ([]string, error) {
	if len(values) > languageFilenamesMax {
		return nil, fmt.Errorf("a language may list at most %d filenames", languageFilenamesMax)
	}
	filenames := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" || len(value) > languageFilenameMaxLength || value == "." || value == ".." ||
			strings.ContainsAny(value, `/\`) || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool {
			return isStatusBarUnsafeRune(r, false)
		}) >= 0 {
			return nil, fmt.Errorf("language filename %q must be a plain file name of 1-%d bytes", value, languageFilenameMaxLength)
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate language filename %q", value)
		}
		seen[key] = struct{}{}
		filenames = append(filenames, value)
	}
	return filenames, nil
}

func validateLanguageFilenamePatterns(values []string) ([]string, error) {
	if len(values) > languagePatternsMax {
		return nil, fmt.Errorf("a language may list at most %d filenamePatterns", languagePatternsMax)
	}
	patterns := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if err := validateLanguageFilenamePattern(value); err != nil {
			return nil, err
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("duplicate language filenamePattern %q", value)
		}
		seen[value] = struct{}{}
		patterns = append(patterns, value)
	}
	return patterns, nil
}

// validateLanguageFilenamePattern accepts an exact name/path or a glob whose
// literal suffix after the last wildcard names an extension, so a plugin
// cannot claim every file.
func validateLanguageFilenamePattern(value string) error {
	invalid := func(reason string) error {
		return fmt.Errorf("language filenamePattern %q %s", value, reason)
	}
	if value == "" || len(value) > languagePatternMaxLength || !languageFilenamePatternChar.MatchString(value) {
		return invalid(fmt.Sprintf("must be 1-%d characters of A-Z a-z 0-9 . _ - / * ? + @", languagePatternMaxLength))
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
		return invalid("must be a relative pattern without empty path segments")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." {
			return invalid("must not contain . or .. segments")
		}
	}
	switch value {
	case "*", "**", "**/*", "*.*", "*.?", ".*":
		return invalid("matches every file")
	}
	last := strings.LastIndexAny(value, "*?")
	if last < 0 {
		return nil
	}
	suffix := value[last+1:]
	if len(suffix) < 2 || !strings.Contains(suffix, ".") {
		return invalid("must end in a literal extension after the last wildcard, for example *.acme or **/*.acme.json")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Order-preserving strict JSON.

type languageJSONNode struct {
	kind  byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	text  string
	flag  bool
	keys  []string
	items []*languageJSONNode
}

func (node *languageJSONNode) get(key string) *languageJSONNode {
	for index, name := range node.keys {
		if name == key {
			return node.items[index]
		}
	}
	return nil
}

func languageStrictJSONError(err error) error {
	return fmt.Errorf("must be strict JSON (comments and trailing commas are not allowed): %w", err)
}

// parseLanguageJSON decodes exactly one JSON value, rejecting invalid UTF-8,
// duplicate keys, trailing data and nesting deeper than jsonDepthMax.
func parseLanguageJSON(data []byte) (*languageJSONNode, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	root, err := readLanguageJSONValue(decoder, 1)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain exactly one JSON value (strict JSON: comments and trailing data are not allowed)")
	}
	return root, nil
}

func readLanguageJSONValue(decoder *json.Decoder, depth int) (*languageJSONNode, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, languageStrictJSONError(err)
	}
	switch value := token.(type) {
	case json.Delim:
		if depth > languageJSONDepthMax {
			return nil, fmt.Errorf("nests deeper than %d levels", languageJSONDepthMax)
		}
		node := &languageJSONNode{kind: 'a'}
		if value == '{' {
			node.kind = 'o'
		}
		seen := map[string]struct{}{}
		for decoder.More() {
			if node.kind == 'o' {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, languageStrictJSONError(err)
				}
				key, _ := keyToken.(string)
				if _, duplicate := seen[key]; duplicate {
					return nil, fmt.Errorf("duplicate key %q (strict JSON)", key)
				}
				seen[key] = struct{}{}
				node.keys = append(node.keys, key)
			}
			child, err := readLanguageJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			node.items = append(node.items, child)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, languageStrictJSONError(err)
		}
		return node, nil
	case string:
		return &languageJSONNode{kind: 's', text: value}, nil
	case json.Number:
		return &languageJSONNode{kind: 'n', text: value.String()}, nil
	case bool:
		return &languageJSONNode{kind: 'b', flag: value}, nil
	case nil:
		return &languageJSONNode{kind: 'z'}, nil
	}
	return nil, languageStrictJSONError(fmt.Errorf("unexpected token %v", token))
}

// ---------------------------------------------------------------------------
// Monarch grammar.

type monarchGrammar struct {
	states     map[string]*languageJSONNode
	order      []string
	attributes map[string]*languageJSONNode
	includes   map[string][]string
	direct     map[string]int
	rules      int
}

func hasLanguageControl(value string, allowTab bool) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		if allowTab && r == '\t' {
			return false
		}
		return isStatusBarUnsafeRune(r, false)
	}) >= 0
}

func validateLanguageToken(node *languageJSONNode, where string) error {
	if node == nil || node.kind != 's' || len(node.text) > languageTokenMaxLength || hasLanguageControl(node.text, false) {
		return fmt.Errorf("%s must be a token string of at most %d bytes", where, languageTokenMaxLength)
	}
	return nil
}

// validateMonarchGrammar checks a Monaco Monarch language definition.
func validateMonarchGrammar(root *languageJSONNode) error {
	if root.kind != 'o' {
		return errors.New("grammar must be a JSON object (a Monaco Monarch language definition)")
	}
	if tokenizer := root.get("tokenizer"); tokenizer == nil || tokenizer.kind != 'o' {
		return errors.New("grammar needs a tokenizer object of named states")
	}
	grammar := &monarchGrammar{
		states:     map[string]*languageJSONNode{},
		attributes: map[string]*languageJSONNode{},
		includes:   map[string][]string{},
		direct:     map[string]int{},
	}
	var tokenizer, brackets, start *languageJSONNode
	for index, key := range root.keys {
		value := root.items[index]
		switch key {
		case "defaultToken", "tokenPostfix":
			if err := validateLanguageToken(value, key); err != nil {
				return err
			}
		case "ignoreCase", "unicode", "includeLF":
			if value.kind != 'b' {
				return fmt.Errorf("%s must be a boolean", key)
			}
		case "start":
			start = value
		case "brackets":
			brackets = value
		case "tokenizer":
			tokenizer = value
		default:
			if err := grammar.addAttribute(key, value); err != nil {
				return err
			}
		}
	}
	if len(tokenizer.keys) == 0 || len(tokenizer.keys) > languageStatesMax {
		return fmt.Errorf("grammar tokenizer needs 1-%d states", languageStatesMax)
	}
	for index, name := range tokenizer.keys {
		_, reserved := monarchReservedStates[name]
		if !monarchStatePattern.MatchString(name) || len(name) > languageStateNameMaxLength || reserved || strings.HasPrefix(name, "__") {
			return fmt.Errorf("grammar state name %q must match %s (at most %d characters, not a JavaScript member name)", name, monarchStatePattern, languageStateNameMaxLength)
		}
		grammar.states[name] = tokenizer.items[index]
		grammar.order = append(grammar.order, name)
	}
	if start != nil {
		if start.kind != 's' || !grammar.stateExists(start.text) {
			return errors.New("grammar start must name a tokenizer state")
		}
	}
	if brackets != nil {
		if err := validateMonarchBrackets(brackets); err != nil {
			return err
		}
	}
	for _, name := range grammar.order {
		rules := grammar.states[name]
		if rules.kind != 'a' {
			return fmt.Errorf("grammar state %q must be a list of rules", name)
		}
		for index, rule := range rules.items {
			if err := grammar.validateRule(name, index, rule); err != nil {
				return err
			}
		}
	}
	if grammar.rules > languageRulesMax {
		return fmt.Errorf("grammar has more than %d rules and cases", languageRulesMax)
	}
	return grammar.checkIncludes()
}

func (grammar *monarchGrammar) addAttribute(name string, value *languageJSONNode) error {
	_, reserved := monarchReservedAttributes[name]
	if !monarchNamePattern.MatchString(name) || reserved || strings.HasPrefix(name, "__") {
		return fmt.Errorf("grammar attribute name %q is not allowed", name)
	}
	switch value.kind {
	case 's':
		if len(value.text) > languageRegexMaxLength || hasLanguageControl(value.text, true) {
			return fmt.Errorf("grammar attribute %q must be at most %d bytes without control characters", name, languageRegexMaxLength)
		}
	case 'a':
		if len(value.items) > languageAttributeArrayMax {
			return fmt.Errorf("grammar attribute %q lists more than %d words", name, languageAttributeArrayMax)
		}
		for _, item := range value.items {
			if err := validateLanguageToken(item, fmt.Sprintf("grammar attribute %q word", name)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("grammar attribute %q must be a regex string or a list of words", name)
	}
	grammar.attributes[name] = value
	return nil
}

// stateExists follows Monarch's lookup: "a.b.c" resolves to the first of
// "a.b.c", "a.b", "a" that is a state.
func (grammar *monarchGrammar) stateExists(name string) bool {
	for name != "" {
		if _, ok := grammar.states[name]; ok {
			return true
		}
		dot := strings.LastIndexByte(name, '.')
		if dot < 0 {
			return false
		}
		name = name[:dot]
	}
	return false
}

func (grammar *monarchGrammar) validateRule(state string, index int, rule *languageJSONNode) error {
	where := fmt.Sprintf("grammar rule tokenizer.%s[%d]", state, index)
	grammar.rules++
	switch rule.kind {
	case 'a':
		if len(rule.items) != 2 && len(rule.items) != 3 {
			return fmt.Errorf("%s must be [regex, action] or [regex, action, next]", where)
		}
		if err := grammar.validateRuleRegex(rule.items[0], where); err != nil {
			return err
		}
		if len(rule.items) == 3 {
			if kind := rule.items[1].kind; kind != 's' && kind != 'o' {
				return fmt.Errorf("%s: with a next state the action must be a token string or an action object", where)
			}
			if err := grammar.validateNext(rule.items[2], where+" next", true); err != nil {
				return err
			}
		}
		grammar.direct[state]++
		return grammar.validateAction(rule.items[1], where, 1)
	case 'o':
		if include := rule.get("include"); include != nil {
			if len(rule.keys) != 1 || include.kind != 's' {
				return fmt.Errorf("%s: an include rule must be exactly {\"include\": \"@state\"}", where)
			}
			target := strings.TrimPrefix(include.text, "@")
			if _, ok := grammar.states[target]; !ok {
				return fmt.Errorf("%s includes undefined state %q", where, include.text)
			}
			grammar.includes[state] = append(grammar.includes[state], target)
			return nil
		}
		regex, action := rule.get("regex"), rule.get("action")
		if regex == nil || action == nil || len(rule.keys) != 2 {
			return fmt.Errorf("%s must be {\"regex\", \"action\"} or {\"include\"}", where)
		}
		if err := grammar.validateRuleRegex(regex, where); err != nil {
			return err
		}
		grammar.direct[state]++
		return grammar.validateAction(action, where, 1)
	}
	return fmt.Errorf("%s must be an array or an object", where)
}

func (grammar *monarchGrammar) validateRuleRegex(node *languageJSONNode, where string) error {
	if node.kind != 's' || node.text == "" {
		return fmt.Errorf("%s regex must be a non-empty string", where)
	}
	if monarchStateSubstitution.MatchString(node.text) {
		return fmt.Errorf("%s regex must not use $S state substitution (dynamic regular expressions are not allowed)", where)
	}
	return grammar.validateRegexSource(node.text, where+" regex")
}

// validateRegexSource bounds a regex and its Monarch @attribute expansion
// (up to five rounds, as Monaco's compileRegExp does).
func (grammar *monarchGrammar) validateRegexSource(source, where string) error {
	if len(source) > languageRegexMaxLength || hasLanguageControl(source, true) {
		return fmt.Errorf("%s must be at most %d bytes without control characters", where, languageRegexMaxLength)
	}
	text := strings.ReplaceAll(source, "@@", "\x01")
	for round := 0; round < 5; round++ {
		matches := monarchAttributeReference.FindAllStringSubmatchIndex(text, -1)
		if len(matches) == 0 {
			break
		}
		var builder strings.Builder
		last := 0
		for _, match := range matches {
			name := text[match[2]:match[3]]
			attribute, ok := grammar.attributes[name]
			if !ok {
				return fmt.Errorf("%s references undefined attribute @%s", where, name)
			}
			if attribute.kind != 's' {
				return fmt.Errorf("%s references @%s, which must be a string attribute", where, name)
			}
			builder.WriteString(text[last:match[0]])
			if attribute.text != "" {
				builder.WriteString("(?:")
				builder.WriteString(attribute.text)
				builder.WriteString(")")
			}
			last = match[1]
			if builder.Len() > languageRegexExpandedMax {
				return fmt.Errorf("%s expands beyond %d bytes", where, languageRegexExpandedMax)
			}
		}
		builder.WriteString(text[last:])
		text = builder.String()
		if len(text) > languageRegexExpandedMax {
			return fmt.Errorf("%s expands beyond %d bytes", where, languageRegexExpandedMax)
		}
	}
	return nil
}

func (grammar *monarchGrammar) validateNext(node *languageJSONNode, where string, allowStack bool) error {
	if node.kind != 's' || node.text == "" || len(node.text) > languageStateNameMaxLength+1 || hasLanguageControl(node.text, false) {
		return fmt.Errorf("%s must be a state name", where)
	}
	if allowStack && monarchStackPattern.MatchString(node.text) {
		return nil
	}
	name := strings.TrimPrefix(node.text, "@")
	if strings.Contains(name, "$") {
		return nil
	}
	if !grammar.stateExists(name) {
		return fmt.Errorf("%s %q is not a tokenizer state", where, node.text)
	}
	return nil
}

func (grammar *monarchGrammar) validateAction(action *languageJSONNode, where string, depth int) error {
	if depth > languageCasesDepthMax {
		return fmt.Errorf("%s nests actions deeper than %d levels", where, languageCasesDepthMax)
	}
	switch action.kind {
	case 's':
		return validateLanguageToken(action, where+" action")
	case 'a':
		if len(action.items) == 0 || len(action.items) > languageActionGroupMax {
			return fmt.Errorf("%s action group needs 1-%d actions", where, languageActionGroupMax)
		}
		for _, item := range action.items {
			if item.kind != 's' && item.kind != 'o' {
				return fmt.Errorf("%s action group items must be token strings or action objects", where)
			}
			if err := grammar.validateAction(item, where, depth+1); err != nil {
				return err
			}
		}
		return nil
	case 'o':
		if action.get("log") != nil {
			return fmt.Errorf("%s: the Monarch log action is not allowed", where)
		}
		if cases := action.get("cases"); cases != nil {
			if len(action.keys) != 1 || cases.kind != 'o' || len(cases.keys) == 0 {
				return fmt.Errorf("%s: a cases action must be exactly {\"cases\": {guard: action}}", where)
			}
			for index, guard := range cases.keys {
				grammar.rules++
				if err := grammar.validateGuard(guard, where); err != nil {
					return err
				}
				if err := grammar.validateAction(cases.items[index], fmt.Sprintf("%s case %q", where, guard), depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		token := action.get("token")
		if token == nil {
			return fmt.Errorf("%s action object needs a token or cases", where)
		}
		for index, key := range action.keys {
			value := action.items[index]
			var err error
			switch key {
			case "token":
				err = validateLanguageToken(value, where+" token")
			case "next":
				err = grammar.validateNext(value, where+" next", true)
			case "switchTo":
				err = grammar.validateNext(value, where+" switchTo", false)
			case "goBack":
				count, parseErr := strconv.Atoi(value.text)
				if value.kind != 'n' || parseErr != nil || count < 0 || count > languageGoBackMax {
					err = fmt.Errorf("%s goBack must be an integer 0-%d", where, languageGoBackMax)
				}
			case "bracket":
				if value.kind != 's' || (value.text != "@open" && value.text != "@close") {
					err = fmt.Errorf("%s bracket must be @open or @close", where)
				}
			case "nextEmbedded":
				if value.kind != 's' || (value.text != "@pop" && !languageEmbeddedPattern.MatchString(value.text)) {
					err = fmt.Errorf("%s nextEmbedded must be @pop or a language id", where)
				}
			default:
				err = fmt.Errorf("%s: unsupported action attribute %q", where, key)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%s action must be a token string, an action object or a list of actions", where)
}

// validateGuard mirrors Monaco's createGuard parsing and rejects guards whose
// regular expression would be rebuilt from matched text at runtime.
func (grammar *monarchGrammar) validateGuard(guard, where string) error {
	if len(guard) > languageRegexMaxLength || hasLanguageControl(guard, false) {
		return fmt.Errorf("%s case guard must be at most %d bytes without control characters", where, languageRegexMaxLength)
	}
	switch guard {
	case "@default", "@eos", "@", "":
		return nil
	}
	pattern := guard
	if match := monarchGuardScrutinee.FindStringSubmatch(guard); match != nil {
		pattern = match[4]
	}
	if pattern == "" || monarchGuardWord.MatchString(pattern) {
		return nil
	}
	operator := "~"
	if match := monarchGuardOperator.FindStringSubmatch(pattern); match != nil {
		operator, pattern = match[1], match[2]
	}
	switch operator {
	case "@", "!@":
		if attribute, ok := grammar.attributes[pattern]; !ok || attribute.kind != 'a' {
			return fmt.Errorf("%s case %q must reference a word-list attribute", where, guard)
		}
	case "~", "!~":
		if monarchGuardWords.MatchString(pattern) {
			return nil
		}
		if monarchMatchSubstitution.MatchString(pattern) {
			return fmt.Errorf("%s case %q builds a regular expression from matched text (dynamic regular expressions are not allowed)", where, guard)
		}
		return grammar.validateRegexSource("^"+pattern+"$", fmt.Sprintf("%s case %q", where, guard))
	}
	return nil
}

// checkIncludes rejects include cycles and bounds the rule count after Monaco
// inlines includes into every state.
func (grammar *monarchGrammar) checkIncludes() error {
	const (
		unvisited = iota
		visiting
		done
	)
	status := map[string]int{}
	expanded := map[string]int{}
	limit := languageExpandedRulesMax
	var visit func(state string) (int, error)
	visit = func(state string) (int, error) {
		switch status[state] {
		case visiting:
			return 0, fmt.Errorf("grammar include cycle through state %q", state)
		case done:
			return expanded[state], nil
		}
		status[state] = visiting
		count := grammar.direct[state]
		for _, target := range grammar.includes[state] {
			included, err := visit(target)
			if err != nil {
				return 0, err
			}
			count += included
			if count > limit {
				count = limit + 1
			}
		}
		status[state] = done
		expanded[state] = count
		return count, nil
	}
	total := 0
	for _, state := range grammar.order {
		count, err := visit(state)
		if err != nil {
			return err
		}
		total += count
		if total > limit {
			return fmt.Errorf("grammar expands to more than %d rules after includes", limit)
		}
	}
	return nil
}

func validateMonarchBrackets(brackets *languageJSONNode) error {
	if brackets.kind != 'a' || len(brackets.items) > languagePairsMax {
		return fmt.Errorf("grammar brackets must be a list of at most %d {open, close, token} entries", languagePairsMax)
	}
	for _, item := range brackets.items {
		var open, close, token *languageJSONNode
		switch {
		case item.kind == 'a' && len(item.items) == 3:
			open, close, token = item.items[0], item.items[1], item.items[2]
		case item.kind == 'o' && len(item.keys) == 3:
			open, close, token = item.get("open"), item.get("close"), item.get("token")
		}
		if open == nil || close == nil || token == nil {
			return errors.New("grammar brackets entries must be {open, close, token} or [open, close, token]")
		}
		if err := validateLanguagePairString(open, "grammar bracket open"); err != nil {
			return err
		}
		if err := validateLanguagePairString(close, "grammar bracket close"); err != nil {
			return err
		}
		if open.text == close.text {
			return fmt.Errorf("grammar bracket open and close must differ (%q)", open.text)
		}
		if err := validateLanguageToken(token, "grammar bracket token"); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Language configuration (allowlisted subset of VS Code language-configuration.json).

func validateLanguagePairString(node *languageJSONNode, where string) error {
	if node == nil || node.kind != 's' || node.text == "" || utf8.RuneCountInString(node.text) > languagePairMaxRunes || hasLanguageControl(node.text, false) {
		return fmt.Errorf("%s must be 1-%d characters", where, languagePairMaxRunes)
	}
	return nil
}

func validateLanguagePair(node *languageJSONNode, where string) error {
	if node.kind != 'a' || len(node.items) != 2 {
		return fmt.Errorf("%s must be [open, close]", where)
	}
	if err := validateLanguagePairString(node.items[0], where+" open"); err != nil {
		return err
	}
	return validateLanguagePairString(node.items[1], where+" close")
}

func validateLanguagePairList(node *languageJSONNode, key string, objectKeys map[string]bool) error {
	if node.kind != 'a' || len(node.items) > languagePairsMax {
		return fmt.Errorf("configuration %s must be a list of at most %d pairs", key, languagePairsMax)
	}
	for index, item := range node.items {
		where := fmt.Sprintf("configuration %s[%d]", key, index)
		if item.kind == 'a' {
			if err := validateLanguagePair(item, where); err != nil {
				return err
			}
			continue
		}
		if objectKeys == nil || item.kind != 'o' {
			return fmt.Errorf("%s must be [open, close]", where)
		}
		for itemIndex, name := range item.keys {
			value := item.items[itemIndex]
			switch {
			case name == "open" || name == "close":
				if err := validateLanguagePairString(value, where+" "+name); err != nil {
					return err
				}
			case name == "notIn" && objectKeys["notIn"]:
				if value.kind != 'a' || len(value.items) > 2 {
					return fmt.Errorf("%s notIn must be a list of \"string\" and \"comment\"", where)
				}
				for _, scope := range value.items {
					if scope.kind != 's' || (scope.text != "string" && scope.text != "comment") {
						return fmt.Errorf("%s notIn must be a list of \"string\" and \"comment\"", where)
					}
				}
			default:
				return fmt.Errorf("%s: unsupported key %q", where, name)
			}
		}
		if item.get("open") == nil || item.get("close") == nil {
			return fmt.Errorf("%s needs open and close", where)
		}
	}
	return nil
}

func validateLanguageConfigurationRegex(node *languageJSONNode, where string) error {
	if node == nil || node.kind != 's' || node.text == "" || len(node.text) > languageRegexMaxLength || hasLanguageControl(node.text, true) {
		return fmt.Errorf("configuration %s must be a regex string of 1-%d bytes", where, languageRegexMaxLength)
	}
	return nil
}

func validateLanguageObjectKeys(node *languageJSONNode, where string, allowed ...string) error {
	if node.kind != 'o' {
		return fmt.Errorf("configuration %s must be an object", where)
	}
	for _, key := range node.keys {
		ok := false
		for _, name := range allowed {
			if key == name {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("configuration %s: unsupported key %q (allowed: %s)", where, key, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// validateLanguageConfiguration accepts comments, brackets, auto-closing and
// surrounding pairs, colorized bracket pairs, autoCloseBefore, folding,
// wordPattern and indentationRules. onEnterRules and other keys are rejected.
func validateLanguageConfiguration(root *languageJSONNode) error {
	if err := validateLanguageObjectKeys(root, "file", "comments", "brackets", "autoClosingPairs", "surroundingPairs",
		"colorizedBracketPairs", "autoCloseBefore", "folding", "wordPattern", "indentationRules"); err != nil {
		return err
	}
	for index, key := range root.keys {
		value := root.items[index]
		var err error
		switch key {
		case "comments":
			if err = validateLanguageObjectKeys(value, key, "lineComment", "blockComment"); err != nil {
				return err
			}
			if line := value.get("lineComment"); line != nil {
				if line.kind != 's' || line.text == "" || utf8.RuneCountInString(line.text) > languageCommentMaxRunes || hasLanguageControl(line.text, false) {
					return fmt.Errorf("configuration comments.lineComment must be 1-%d characters", languageCommentMaxRunes)
				}
			}
			if block := value.get("blockComment"); block != nil {
				if block.kind != 'a' || len(block.items) != 2 {
					return errors.New("configuration comments.blockComment must be [open, close]")
				}
				for _, part := range block.items {
					if part.kind != 's' || part.text == "" || utf8.RuneCountInString(part.text) > languageCommentMaxRunes || hasLanguageControl(part.text, false) {
						return fmt.Errorf("configuration comments.blockComment entries must be 1-%d characters", languageCommentMaxRunes)
					}
				}
			}
		case "brackets", "colorizedBracketPairs":
			err = validateLanguagePairList(value, key, nil)
		case "autoClosingPairs":
			err = validateLanguagePairList(value, key, map[string]bool{"notIn": true})
		case "surroundingPairs":
			err = validateLanguagePairList(value, key, map[string]bool{})
		case "autoCloseBefore":
			if value.kind != 's' || utf8.RuneCountInString(value.text) > languageAutoCloseBeforeMax || strings.IndexFunc(value.text, func(r rune) bool {
				return r != '\n' && r != '\t' && isStatusBarUnsafeRune(r, false)
			}) >= 0 {
				err = fmt.Errorf("configuration autoCloseBefore must be a string of at most %d characters", languageAutoCloseBeforeMax)
			}
		case "folding":
			if err = validateLanguageObjectKeys(value, key, "markers", "offSide"); err != nil {
				return err
			}
			if offSide := value.get("offSide"); offSide != nil && offSide.kind != 'b' {
				return errors.New("configuration folding.offSide must be a boolean")
			}
			if markers := value.get("markers"); markers != nil {
				if err = validateLanguageObjectKeys(markers, "folding.markers", "start", "end"); err != nil {
					return err
				}
				if err = validateLanguageConfigurationRegex(markers.get("start"), "folding.markers.start"); err != nil {
					return err
				}
				err = validateLanguageConfigurationRegex(markers.get("end"), "folding.markers.end")
			}
		case "wordPattern":
			err = validateLanguageConfigurationRegex(value, key)
		case "indentationRules":
			if err = validateLanguageObjectKeys(value, key, "increaseIndentPattern", "decreaseIndentPattern", "indentNextLinePattern", "unIndentedLinePattern"); err != nil {
				return err
			}
			for _, name := range []string{"increaseIndentPattern", "decreaseIndentPattern"} {
				if err = validateLanguageConfigurationRegex(value.get(name), "indentationRules."+name); err != nil {
					return err
				}
			}
			for _, name := range []string{"indentNextLinePattern", "unIndentedLinePattern"} {
				if pattern := value.get(name); pattern != nil {
					if err = validateLanguageConfigurationRegex(pattern, "indentationRules."+name); err != nil {
						return err
					}
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
