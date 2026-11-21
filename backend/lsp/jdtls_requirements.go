package lsp

import (
	"strconv"
	"strings"
	"unicode"
)

// JavaSE requirements use a small positive LDAP-filter subset. Anything outside
// it is unknown rather than a guessed lower bound (notably NOT/upper bounds).
// OSGi JavaSE versions denote compatible execution environments, so version=N
// and version>=N both establish a minimum JVM major, not an exact runtime pin.
const maxJavaEEFilterBytes = 16 * 1024
const maxJavaEEFilterNodes = 256
const maxJavaEEFilterDepth = 32

type javaEEFilter struct {
	kind                       byte
	attribute, operator, value string
	children                   []*javaEEFilter
}

type javaEEParser struct {
	text          string
	offset, nodes int
}

func (p *javaEEParser) space() {
	for p.offset < len(p.text) && unicode.IsSpace(rune(p.text[p.offset])) {
		p.offset++
	}
}
func (p *javaEEParser) parse(depth int) (*javaEEFilter, bool) {
	p.space()
	p.nodes++
	if depth > maxJavaEEFilterDepth || p.nodes > maxJavaEEFilterNodes || p.offset >= len(p.text) || p.text[p.offset] != '(' {
		return nil, false
	}
	p.offset++
	p.space()
	if p.offset >= len(p.text) {
		return nil, false
	}
	node := &javaEEFilter{kind: p.text[p.offset]}
	if node.kind == '&' || node.kind == '|' || node.kind == '!' {
		p.offset++
		for {
			p.space()
			if p.offset >= len(p.text) {
				return nil, false
			}
			if p.text[p.offset] == ')' {
				p.offset++
				break
			}
			child, ok := p.parse(depth + 1)
			if !ok {
				return nil, false
			}
			node.children = append(node.children, child)
		}
		if len(node.children) == 0 || (node.kind == '!' && len(node.children) != 1) {
			return nil, false
		}
		return node, true
	}
	node.kind = '='
	start := p.offset
	for p.offset < len(p.text) && p.text[p.offset] != ')' {
		if p.text[p.offset] == '(' || p.text[p.offset] == '\\' {
			return nil, false
		}
		p.offset++
	}
	if p.offset >= len(p.text) {
		return nil, false
	}
	atom := p.text[start:p.offset]
	p.offset++
	at := strings.IndexByte(atom, '=')
	if at < 1 {
		return nil, false
	}
	node.attribute = strings.TrimSpace(atom[:at])
	node.operator = "="
	if strings.HasSuffix(node.attribute, ">") || strings.HasSuffix(node.attribute, "<") || strings.HasSuffix(node.attribute, "~") {
		node.operator = node.attribute[len(node.attribute)-1:] + "="
		node.attribute = strings.TrimSpace(node.attribute[:len(node.attribute)-1])
	}
	node.value = strings.TrimSpace(atom[at+1:])
	if node.attribute == "" || node.value == "" {
		return nil, false
	}
	return node, true
}

// splitManifestTokens preserves separators inside quoted directive values.
func splitManifestTokens(text string, separator byte) ([]string, bool) {
	var parts []string
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(text); i++ {
		if escaped {
			escaped = false
			continue
		}
		if quoted && text[i] == '\\' {
			escaped = true
			continue
		}
		if text[i] == '"' {
			quoted = !quoted
			continue
		}
		if text[i] == separator && !quoted {
			parts = append(parts, text[start:i])
			start = i + 1
			if len(parts) > maxJavaEEFilterNodes {
				return nil, false
			}
		}
	}
	if quoted || escaped {
		return nil, false
	}
	return append(parts, text[start:]), true
}

func javaEESelectors(node *javaEEFilter) (java, other bool) {
	if node.kind == '=' && node.attribute == "osgi.ee" {
		return node.operator == "=" && node.value == "JavaSE", node.operator != "=" || node.value != "JavaSE"
	}
	for _, child := range node.children {
		j, o := javaEESelectors(child)
		java = java || j
		other = other || o
	}
	return
}

// javaEEApplicability only proves selector facts; versions and unsupported
// predicates are unknown. A foreign selector beneath NOT/OR is not enough
// to prove that the complete clause cannot match JavaSE.
func javaEEApplicability(node *javaEEFilter) (matches, known bool) {
	if node.kind == '=' && node.attribute == "osgi.ee" && node.operator == "=" && !strings.ContainsAny(node.value, "*\\") {
		return node.value == "JavaSE", true
	}
	if node.kind != '&' && node.kind != '|' {
		return false, false
	}
	allKnown := true
	for _, child := range node.children {
		match, understood := javaEEApplicability(child)
		if understood && node.kind == '&' && !match {
			return false, true
		}
		if understood && node.kind == '|' && match {
			return true, true
		}
		allKnown = allKnown && understood
	}
	if allKnown {
		return node.kind == '&', true
	}
	return false, false
}

func javaEEVersion(value string) (int, bool) {
	parts := strings.Split(value, ".")
	if len(parts) > 4 {
		return 0, false
	}
	for _, part := range parts {
		if part == "" {
			return 0, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
	}
	major := javaVersionNumber(value)
	if major <= 0 || major > 10000 {
		return 0, false
	}
	start := 1
	if parts[0] == "1" && len(parts) > 1 {
		start = 2
	}
	// Patch/minor requirements cannot safely be proved by the major-only probe.
	for _, part := range parts[start:] {
		n, err := strconv.Atoi(part)
		if err != nil || n != 0 {
			return 0, false
		}
	}
	return major, true
}

func javaEEMinimum(node *javaEEFilter) (int, bool) {
	switch node.kind {
	case '&', '|':
		minimum := 0
		for i, child := range node.children {
			major, known := javaEEMinimum(child)
			if !known {
				return 0, false
			}
			if node.kind == '&' {
				minimum = max(minimum, major)
			} else if i == 0 || major < minimum {
				minimum = major
			}
		}
		return minimum, true
	case '=':
		if node.attribute == "osgi.ee" && node.operator == "=" && node.value == "JavaSE" {
			return 0, true
		}
		if node.attribute == "version" && (node.operator == "=" || node.operator == ">=") {
			return javaEEVersion(node.value)
		}
	}
	return 0, false
}

// javaEECapabilityMinimum returns a floor and whether all relevant mandatory
// constraints were understood. Other capability namespaces/optional clauses do
// not impose a Java floor. Unknown expressions are never silently treated as 21.
func javaEECapabilityMinimum(header string) (int, bool) {
	if len(header) > maxManifestBytes {
		return 0, false
	}
	clauses, ok := splitManifestTokens(header, ',')
	if !ok {
		return 0, false
	}
	required := 0
	for _, clause := range clauses {
		terms, ok := splitManifestTokens(clause, ';')
		if !ok {
			return 0, false
		}
		if strings.TrimSpace(terms[0]) != "osgi.ee" {
			continue
		}
		filter := ""
		optional := false
		for _, term := range terms[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(term), ":=")
			if !found {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if strings.HasPrefix(value, "\"") {
				unquoted, err := strconv.Unquote(value)
				if err != nil {
					return 0, false
				}
				value = unquoted
			}
			if key == "resolution" && value == "optional" {
				optional = true
			}
			if key == "filter" {
				if filter != "" {
					return 0, false
				}
				filter = value
			}
		}
		if optional {
			continue
		}
		if filter == "" || len(filter) > maxJavaEEFilterBytes {
			return 0, false
		}
		parser := javaEEParser{text: filter}
		node, ok := parser.parse(0)
		parser.space()
		if !ok || parser.offset != len(filter) {
			return 0, false
		}
		java, other := javaEESelectors(node)
		if !java && other {
			if matches, known := javaEEApplicability(node); known && !matches {
				continue
			}
		}
		if !java || other {
			return 0, false
		}
		major, known := javaEEMinimum(node)
		if !known {
			return 0, false
		}
		required = max(required, major)
	}
	return required, true
}
