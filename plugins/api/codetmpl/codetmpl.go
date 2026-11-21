// Package codetmpl parses and renders httpCodeGenerators templates (.tmpl):
// a small logic-less, Mustache-like language whose variables, sections and
// filters come from the contract vocabulary (contribution-points.json
// httpCodeGenerators.lists). It is shared verbatim by the desktop host and the
// `mincode plugin` CLI: vue-app/scripts/build-plugin-contracts.mjs copies this
// file to tools/mincode-cli/codetmpl_gen.go (package clause rewritten to main)
// and check:plugin-contracts fails on drift. Keep it dependency-free
// (standard library only), in this single file, and free of identifiers that
// clash with svgsafe (both land in the CLI's package main).
//
// Grammar (a strict Mustache subset; vue-app/src/domain/http/code-template.ts
// mirrors it and the shared corpus tools/verify/fixtures/codetmpl keeps both
// in step):
//
//	{{name}}             variable, escaped with the generator's escape
//	{{name|filter}}      variable with exactly one filter ("raw" = verbatim)
//	{{#section}}…{{/section}}  list section (repeat per item) or flag section
//	{{^section}}…{{/section}}  inverted: empty list or false flag
//	{{! comment}}        dropped
//
// Rejected: "{{{", "{{&", "{{>" (partials), "{{=" (delimiter changes),
// whitespace inside tags, unknown names or filters, item variables and
// first/last outside a list section, a list section inside a list section,
// more than MaxDepth nested sections and more than MaxNodes nodes. A line
// holding only one section or comment tag (plus blanks) is removed entirely,
// as in Mustache.
package codetmpl

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// TemplateLimits bounds a parsed template.
type TemplateLimits struct {
	MaxNodes int
	MaxDepth int
}

// Vocabulary is the contract vocabulary a template may reference.
type Vocabulary struct {
	Variables        []string
	ItemVariables    []string
	ListSections     []string
	FlagSections     []string
	ItemFlagSections []string
	Filters          []string
}

// Error codes shared with the TypeScript mirror.
const (
	CodeSyntax             = "syntax"
	CodeForbiddenTag       = "forbidden_tag"
	CodeUnknownVariable    = "unknown_variable"
	CodeUnknownFilter      = "unknown_filter"
	CodeUnknownSection     = "unknown_section"
	CodeOutsideSection     = "outside_section"
	CodeNestedList         = "nested_list"
	CodeUnclosedSection    = "unclosed_section"
	CodeMismatchedSection  = "mismatched_section"
	CodeDepthExceeded      = "depth_exceeded"
	CodeTooManyNodes       = "too_many_nodes"
	CodeOutputTooLarge     = "output_too_large"
	CodeUnknownEscape      = "unknown_escape"
	codetmplOpen           = "{{"
	codetmplClose          = "}}"
	codetmplKindText       = 0
	codetmplKindVar        = 1
	codetmplKindSection    = 2
	codetmplKindInverted   = 3
	codetmplKindCloseTag   = 4
	codetmplKindCommentTag = 5
)

// TemplateError is a parse or render failure with a stable Code.
type TemplateError struct {
	Code    string
	Line    int
	Message string
}

func (e *TemplateError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("codetmpl: %s (line %d): %s", e.Code, e.Line, e.Message)
	}
	return fmt.Sprintf("codetmpl: %s: %s", e.Code, e.Message)
}

func codetmplFail(code string, line int, format string, args ...any) error {
	return &TemplateError{Code: code, Line: line, Message: fmt.Sprintf(format, args...)}
}

type codetmplToken struct {
	kind   int
	text   string // literal text, or the tag name
	filter string
	line   int
}

type codetmplNode struct {
	kind     int
	text     string
	filter   string
	list     bool
	children []codetmplNode
}

// CodeTemplate is a validated template.
type CodeTemplate struct {
	nodes []codetmplNode
}

// CodeItem is one list-section item.
type CodeItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CodeModel is the (already redacted) data a template renders.
type CodeModel struct {
	Vars  map[string]string     `json:"vars"`
	Lists map[string][]CodeItem `json:"lists"`
	Flags map[string]bool       `json:"flags"`
}

// Parse validates src against limits and vocab.
func Parse(src string, limits TemplateLimits, vocab Vocabulary) error {
	_, err := Compile(src, limits, vocab)
	return err
}

func codetmplIsName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, r := range name {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (index == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func codetmplContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func codetmplTokenize(src string) ([]codetmplToken, error) {
	var tokens []codetmplToken
	line := 1
	for len(src) > 0 {
		start := strings.Index(src, codetmplOpen)
		if start < 0 {
			tokens = append(tokens, codetmplToken{kind: codetmplKindText, text: src, line: line})
			break
		}
		if start > 0 {
			tokens = append(tokens, codetmplToken{kind: codetmplKindText, text: src[:start], line: line})
			line += strings.Count(src[:start], "\n")
		}
		rest := src[start+len(codetmplOpen):]
		if rest != "" && strings.ContainsRune("{&>=", rune(rest[0])) {
			return nil, codetmplFail(CodeForbiddenTag, line, "%q tags are not supported", codetmplOpen+rest[:1])
		}
		end := strings.Index(rest, codetmplClose)
		if end < 0 {
			return nil, codetmplFail(CodeSyntax, line, "unterminated tag")
		}
		content := rest[:end]
		if strings.Contains(content, codetmplOpen) {
			return nil, codetmplFail(CodeSyntax, line, "nested %q inside a tag", codetmplOpen)
		}
		token := codetmplToken{line: line}
		switch {
		case strings.HasPrefix(content, "!"):
			token.kind = codetmplKindCommentTag
		case strings.HasPrefix(content, "#"):
			token.kind, token.text = codetmplKindSection, content[1:]
		case strings.HasPrefix(content, "^"):
			token.kind, token.text = codetmplKindInverted, content[1:]
		case strings.HasPrefix(content, "/"):
			token.kind, token.text = codetmplKindCloseTag, content[1:]
		default:
			token.kind = codetmplKindVar
			name, filter, hasFilter := strings.Cut(content, "|")
			token.text = name
			if hasFilter {
				if !codetmplIsName(filter) {
					return nil, codetmplFail(CodeSyntax, line, "invalid filter %q", filter)
				}
				token.filter = filter
			}
		}
		if token.kind != codetmplKindCommentTag && !codetmplIsName(token.text) {
			return nil, codetmplFail(CodeSyntax, line, "invalid tag %q (names are letters and digits, without spaces)", codetmplOpen+content+codetmplClose)
		}
		line += strings.Count(content, "\n")
		tokens = append(tokens, token)
		src = rest[end+len(codetmplClose):]
	}
	return codetmplStripStandalone(tokens), nil
}

func codetmplBlank(text string) bool {
	return strings.Trim(text, " \t") == ""
}

// codetmplStripStandalone removes lines that hold only a section, inverted,
// close or comment tag (plus spaces/tabs), deciding every tag against the
// original text first so adjacent standalone lines are all removed.
func codetmplStripStandalone(tokens []codetmplToken) []codetmplToken {
	standalone := make([]bool, len(tokens))
	for index, token := range tokens {
		if token.kind == codetmplKindText || token.kind == codetmplKindVar {
			continue
		}
		lineStart := index == 0
		if index > 0 && tokens[index-1].kind == codetmplKindText {
			text := tokens[index-1].text
			newline := strings.LastIndexByte(text, '\n')
			lineStart = codetmplBlank(text[newline+1:]) && (newline >= 0 || index == 1)
		}
		if !lineStart {
			continue
		}
		lineEnd := index == len(tokens)-1
		if index+1 < len(tokens) && tokens[index+1].kind == codetmplKindText {
			text := strings.TrimLeft(tokens[index+1].text, " \t")
			lineEnd = strings.HasPrefix(text, "\n") || strings.HasPrefix(text, "\r\n") || (text == "" && index+1 == len(tokens)-1)
		}
		standalone[index] = lineEnd
	}
	for index := range tokens {
		if !standalone[index] {
			continue
		}
		if index > 0 && tokens[index-1].kind == codetmplKindText {
			text := tokens[index-1].text
			tokens[index-1].text = text[:strings.LastIndexByte(text, '\n')+1]
		}
		if index+1 < len(tokens) && tokens[index+1].kind == codetmplKindText {
			text := strings.TrimLeft(tokens[index+1].text, " \t")
			text = strings.TrimPrefix(text, "\r")
			tokens[index+1].text = strings.TrimPrefix(text, "\n")
		}
	}
	out := tokens[:0]
	for _, token := range tokens {
		if token.kind == codetmplKindCommentTag || (token.kind == codetmplKindText && token.text == "") {
			continue
		}
		out = append(out, token)
	}
	return out
}

// Compile validates src against limits and vocab and returns the template.
func Compile(src string, limits TemplateLimits, vocab Vocabulary) (*CodeTemplate, error) {
	if !utf8.ValidString(src) {
		return nil, codetmplFail(CodeSyntax, 0, "template must be valid UTF-8")
	}
	tokens, err := codetmplTokenize(src)
	if err != nil {
		return nil, err
	}
	type frame struct {
		name  string
		line  int
		list  bool
		nodes []codetmplNode
		kind  int
	}
	stack := []frame{{}}
	count := 0
	inList := false
	for _, token := range tokens {
		count++
		if count > limits.MaxNodes {
			return nil, codetmplFail(CodeTooManyNodes, token.line, "template exceeds %d nodes", limits.MaxNodes)
		}
		top := &stack[len(stack)-1]
		switch token.kind {
		case codetmplKindText:
			top.nodes = append(top.nodes, codetmplNode{kind: codetmplKindText, text: token.text})
		case codetmplKindVar:
			switch {
			case codetmplContains(vocab.Variables, token.text):
			case codetmplContains(vocab.ItemVariables, token.text):
				if !inList {
					return nil, codetmplFail(CodeOutsideSection, token.line, "{{%s}} is only valid inside a list section", token.text)
				}
			default:
				return nil, codetmplFail(CodeUnknownVariable, token.line, "unknown variable %q", token.text)
			}
			if token.filter != "" && !codetmplContains(vocab.Filters, token.filter) {
				return nil, codetmplFail(CodeUnknownFilter, token.line, "unknown filter %q", token.filter)
			}
			top.nodes = append(top.nodes, codetmplNode{kind: codetmplKindVar, text: token.text, filter: token.filter})
		case codetmplKindSection, codetmplKindInverted:
			list := codetmplContains(vocab.ListSections, token.text)
			switch {
			case list:
				if inList {
					return nil, codetmplFail(CodeNestedList, token.line, "list section %q cannot be nested in another list section", token.text)
				}
			case codetmplContains(vocab.FlagSections, token.text):
			case codetmplContains(vocab.ItemFlagSections, token.text):
				if !inList {
					return nil, codetmplFail(CodeOutsideSection, token.line, "section %q is only valid inside a list section", token.text)
				}
			default:
				return nil, codetmplFail(CodeUnknownSection, token.line, "unknown section %q", token.text)
			}
			if len(stack) > limits.MaxDepth {
				return nil, codetmplFail(CodeDepthExceeded, token.line, "sections nest deeper than %d", limits.MaxDepth)
			}
			stack = append(stack, frame{name: token.text, line: token.line, list: list && token.kind == codetmplKindSection, kind: token.kind})
			if list && token.kind == codetmplKindSection {
				inList = true
			}
		case codetmplKindCloseTag:
			if len(stack) == 1 {
				return nil, codetmplFail(CodeMismatchedSection, token.line, "{{/%s}} closes no open section", token.text)
			}
			if top.name != token.text {
				return nil, codetmplFail(CodeMismatchedSection, token.line, "{{/%s}} does not close {{%s}}", token.text, top.name)
			}
			closed := *top
			stack = stack[:len(stack)-1]
			if closed.list {
				inList = false
			}
			parent := &stack[len(stack)-1]
			parent.nodes = append(parent.nodes, codetmplNode{kind: closed.kind, text: closed.name, list: codetmplContains(vocab.ListSections, closed.name), children: closed.nodes})
		}
	}
	if len(stack) > 1 {
		open := stack[len(stack)-1]
		return nil, codetmplFail(CodeUnclosedSection, open.line, "section %q is not closed", open.name)
	}
	return &CodeTemplate{nodes: stack[0].nodes}, nil
}

type codetmplRenderer struct {
	model    CodeModel
	escape   string
	maxBytes int
	out      strings.Builder
	item     *CodeItem
	first    bool
	last     bool
}

// Render renders the template with model; bare variables use escape (one of
// the contract escapes). Output beyond maxBytes (>0) fails with
// output_too_large.
func (t *CodeTemplate) Render(model CodeModel, escape string, maxBytes int) (string, error) {
	if escape != "none" && !codetmplIsFilter(escape) {
		return "", codetmplFail(CodeUnknownEscape, 0, "unknown escape %q", escape)
	}
	renderer := &codetmplRenderer{model: model, escape: escape, maxBytes: maxBytes}
	if err := renderer.nodes(t.nodes); err != nil {
		return "", err
	}
	return renderer.out.String(), nil
}

func codetmplIsFilter(name string) bool {
	switch name {
	case "raw", "json", "jsonInner", "shell", "powershell", "cstring", "xml", "url", "upper", "lower":
		return true
	}
	return false
}

func (r *codetmplRenderer) write(text string) error {
	if r.maxBytes > 0 && r.out.Len()+len(text) > r.maxBytes {
		return codetmplFail(CodeOutputTooLarge, 0, "output exceeds %d bytes", r.maxBytes)
	}
	r.out.WriteString(text)
	return nil
}

func (r *codetmplRenderer) nodes(nodes []codetmplNode) error {
	for _, node := range nodes {
		var err error
		switch node.kind {
		case codetmplKindText:
			err = r.write(node.text)
		case codetmplKindVar:
			value := r.model.Vars[node.text]
			if r.item != nil && node.text == "name" {
				value = r.item.Name
			} else if r.item != nil && node.text == "value" {
				value = r.item.Value
			}
			filter := node.filter
			if filter == "" {
				filter = r.escape
			}
			err = r.write(ApplyFilter(filter, value))
		case codetmplKindSection, codetmplKindInverted:
			err = r.section(node)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *codetmplRenderer) section(node codetmplNode) error {
	inverted := node.kind == codetmplKindInverted
	if node.list {
		items := r.model.Lists[node.text]
		if inverted {
			if len(items) == 0 {
				return r.nodes(node.children)
			}
			return nil
		}
		for index := range items {
			item := items[index]
			r.item, r.first, r.last = &item, index == 0, index == len(items)-1
			err := r.nodes(node.children)
			r.item = nil
			if err != nil {
				return err
			}
		}
		return nil
	}
	var truthy bool
	switch node.text {
	case "first":
		truthy = r.first
	case "last":
		truthy = r.last
	default:
		truthy = r.model.Flags[node.text]
	}
	if truthy != inverted {
		return r.nodes(node.children)
	}
	return nil
}

const codetmplHex = "0123456789ABCDEF"

// ApplyFilter applies a filter or escape to value. "none" and "raw" return it
// unchanged; unknown names also return it unchanged (Compile rejects them).
func ApplyFilter(filter, value string) string {
	switch filter {
	case "json":
		return `"` + codetmplJSONInner(value) + `"`
	case "jsonInner":
		return codetmplJSONInner(value)
	case "shell":
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	case "powershell":
		var builder strings.Builder
		builder.WriteByte('\'')
		for _, r := range value {
			if r == '\'' || r == '‘' || r == '’' || r == '‚' || r == '‛' {
				builder.WriteRune(r)
			}
			builder.WriteRune(r)
		}
		builder.WriteByte('\'')
		return builder.String()
	case "cstring":
		var builder strings.Builder
		builder.WriteByte('"')
		for _, r := range value {
			switch r {
			case '\\':
				builder.WriteString(`\\`)
			case '"':
				builder.WriteString(`\"`)
			case '\n':
				builder.WriteString(`\n`)
			case '\r':
				builder.WriteString(`\r`)
			case '\t':
				builder.WriteString(`\t`)
			default:
				if r < 0x20 || r == 0x7f {
					builder.WriteString(fmt.Sprintf(`\%03o`, r))
				} else {
					builder.WriteRune(r)
				}
			}
		}
		builder.WriteByte('"')
		return builder.String()
	case "xml":
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
	case "url":
		var builder strings.Builder
		for _, b := range []byte(value) {
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.IndexByte("-_.!~*'()", b) >= 0 {
				builder.WriteByte(b)
			} else {
				builder.WriteByte('%')
				builder.WriteByte(codetmplHex[b>>4])
				builder.WriteByte(codetmplHex[b&15])
			}
		}
		return builder.String()
	case "upper", "lower":
		bytes := []byte(value)
		for index, b := range bytes {
			if filter == "upper" && b >= 'a' && b <= 'z' {
				bytes[index] = b - 32
			} else if filter == "lower" && b >= 'A' && b <= 'Z' {
				bytes[index] = b + 32
			}
		}
		return string(bytes)
	}
	return value
}

// codetmplJSONInner escapes value as the body of a JSON string, identically
// to the TypeScript mirror (no HTML escaping; U+2028/U+2029 escaped).
func codetmplJSONInner(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case ' ', ' ':
			builder.WriteString(fmt.Sprintf(`\u%04x`, r))
		default:
			if r < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}
