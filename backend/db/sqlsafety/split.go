package sqlsafety

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Family groups database dialects that share quoting, comment and
// statement syntax for the purposes of the SQL safety net.
type Family string

const (
	FamilyMySQL     Family = "mysql"
	FamilyPostgres  Family = "postgres"
	FamilySQLServer Family = "sqlserver"
	FamilyOracle    Family = "oracle"
	FamilySQLite    Family = "sqlite"
	FamilyDM        Family = "dm"
	FamilyANSI      Family = "ansi"
	FamilyNone      Family = "none"
)

// FamilyFor maps a MinCode dialect name (built-in driver or a plugin's
// sqlDialect) to its family. Unknown dialects map to FamilyNone.
func FamilyFor(dialect string) Family {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "mysql":
		return FamilyMySQL
	case "postgres":
		return FamilyPostgres
	case "sqlserver":
		return FamilySQLServer
	case "oracle", "yashandb":
		return FamilyOracle
	case "dm":
		return FamilyDM
	case "sqlite":
		return FamilySQLite
	case "duckdb", "firebird", "generic":
		return FamilyANSI
	default:
		return FamilyNone
	}
}

type tokKind int

const (
	tokWord   tokKind = iota // unquoted identifier or keyword
	tokIdent                 // quoted identifier: "x", `x`, [x]
	tokString                // string literal, including $tag$...$tag$
	tokNumber                // numeric literal
	tokPunct                 // operator or punctuation
	tokOther                 // opaque text such as MySQL /*! ... */
)

type token struct {
	kind  tokKind
	text  string // raw source text
	up    string // upper-cased text for words, "" otherwise
	start int
	end   int
	depth int // parenthesis depth; "(" and ")" carry the outer depth
}

// name returns the identifier value of a word or quoted identifier.
func (t token) name() string {
	if t.kind != tokIdent || len(t.text) < 2 {
		return t.text
	}
	open, close := t.text[0], t.text[len(t.text)-1]
	inner := t.text[1 : len(t.text)-1]
	if open == '[' {
		return strings.ReplaceAll(inner, "]]", "]")
	}
	return strings.ReplaceAll(inner, string([]byte{close, close}), string(close))
}

func (t token) isWord(words ...string) bool {
	if t.kind != tokWord {
		return false
	}
	for _, w := range words {
		if t.up == w {
			return true
		}
	}
	return false
}

func (t token) isPunct(p string) bool { return t.kind == tokPunct && t.text == p }

func (t token) isName() bool { return t.kind == tokWord || t.kind == tokIdent }

var twoCharOps = []string{"<=", ">=", "<>", "!=", ":=", "==", "::", "||", "->", "=>"}

// lex tokenizes sql, dropping whitespace and comments. MySQL executable
// comments (/*! ... */) stay one opaque tokOther token.
func lex(f Family, s string) []token {
	toks, _ := lexMode(f, s, false)
	return toks
}

// lexExpanded is lex, except that the body of each MySQL executable
// comment is tokenized as SQL, the way the server runs it. It also
// reports whether any executable comment was present.
func lexExpanded(f Family, s string) ([]token, bool) {
	return lexMode(f, s, true)
}

func lexMode(f Family, src string, expand bool) ([]token, bool) {
	var toks []token
	depth := 0
	sawExec := false
	emit := func(k tokKind, start, end int) {
		t := token{kind: k, text: src[start:end], start: start, end: end, depth: depth}
		if k == tokWord {
			t.up = strings.ToUpper(t.text)
		}
		if k == tokPunct {
			switch t.text {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
				t.depth = depth
			}
		}
		toks = append(toks, t)
	}
	// scan lexes s[i:]; s is always a prefix of src so offsets match.
	var scan func(s string, i int)
	scan = func(s string, i int) {
		n := len(s)
		for i < n {
			c := s[i]
			switch {
			case isSpace(c):
				i++
			case c == '-' && i+1 < n && s[i+1] == '-' && (f != FamilyMySQL || i+2 >= n || isSpace(s[i+2])):
				i = lineEnd(s, i)
			case c == '#' && f == FamilyMySQL:
				i = lineEnd(s, i)
			case c == '/' && i+1 < n && s[i+1] == '*':
				end := blockCommentEnd(s, i, f == FamilyPostgres)
				if f == FamilyMySQL && i+2 < n && s[i+2] == '!' {
					sawExec = true
					if !expand {
						emit(tokOther, i, end)
					} else {
						body := i + 3
						for body < end && isDigit(s[body]) {
							body++
						}
						bodyEnd := end
						if bodyEnd-2 >= body && s[bodyEnd-2:bodyEnd] == "*/" {
							bodyEnd -= 2
						}
						scan(s[:bodyEnd], body)
					}
				}
				i = end
			case c == '\'':
				backslash := f == FamilyMySQL ||
					(f == FamilyPostgres && len(toks) > 0 && toks[len(toks)-1].end == i && toks[len(toks)-1].isWord("E"))
				end := quotedEnd(s, i, '\'', backslash)
				emit(tokString, i, end)
				i = end
			case c == '"':
				if f == FamilyMySQL {
					end := quotedEnd(s, i, '"', true)
					emit(tokString, i, end)
					i = end
				} else {
					end := quotedEnd(s, i, '"', false)
					emit(tokIdent, i, end)
					i = end
				}
			case c == '`' && (f == FamilyMySQL || f == FamilySQLite):
				end := quotedEnd(s, i, '`', false)
				emit(tokIdent, i, end)
				i = end
			case c == '[' && (f == FamilySQLServer || f == FamilySQLite):
				end := quotedEnd(s, i, ']', false)
				emit(tokIdent, i, end)
				i = end
			case c == '$' && f == FamilyPostgres && dollarTag(s, i) != "":
				tag := dollarTag(s, i)
				end := n
				if k := strings.Index(s[i+len(tag):], tag); k >= 0 {
					end = i + len(tag) + k + len(tag)
				}
				emit(tokString, i, end)
				i = end
			case isDigit(c) || (c == '.' && i+1 < n && isDigit(s[i+1])):
				end := numberEnd(s, i)
				emit(tokNumber, i, end)
				i = end
			case isWordStart(s, i, f):
				end := wordEnd(s, i)
				emit(tokWord, i, end)
				i = end
			default:
				width := 1
				if c >= utf8.RuneSelf {
					_, width = utf8.DecodeRuneInString(s[i:])
				} else if i+1 < n {
					for _, op := range twoCharOps {
						if s[i:i+2] == op {
							width = 2
							break
						}
					}
				}
				emit(tokPunct, i, i+width)
				i += width
			}
		}
	}
	scan(src, 0)
	return toks, sawExec
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// lineEnd returns the index just past the line break that ends a line comment
// starting at s[i]. A bare CR counts as a line break too: PostgreSQL and other
// servers end the comment there and run what follows, so treating it as part
// of the comment would hide those statements from classification.
func lineEnd(s string, i int) int {
	if k := strings.IndexAny(s[i:], "\r\n"); k >= 0 {
		return i + k + 1
	}
	return len(s)
}

func blockCommentEnd(s string, i int, nested bool) int {
	level := 0
	for j := i; j+1 < len(s); j++ {
		switch {
		case s[j] == '/' && s[j+1] == '*' && (nested || level == 0):
			level++
			j++
		case s[j] == '*' && s[j+1] == '/':
			level--
			j++
			if level == 0 {
				return j + 1
			}
		}
	}
	return len(s)
}

// quotedEnd returns the index just past the closing quote of the literal
// starting at s[i]. A doubled closing quote is an escaped quote.
func quotedEnd(s string, i int, closer byte, backslash bool) int {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == closer:
			if j+1 < len(s) && s[j+1] == closer {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// dollarTag returns "$tag$" when a Postgres dollar quote starts at s[i].
func dollarTag(s string, i int) string {
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '$':
			return s[i : j+1]
		case c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case isDigit(c) && j > i+1:
		default:
			return ""
		}
	}
	return ""
}

func numberEnd(s string, i int) int {
	j := i
	for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
		j++
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if k < len(s) && isDigit(s[k]) {
			j = k
			for j < len(s) && isDigit(s[j]) {
				j++
			}
		}
	}
	return j
}

func isWordStart(s string, i int, f Family) bool {
	c := s[i]
	if c == '_' || c == '@' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
		return true
	}
	if c == '#' && f != FamilyMySQL {
		return true
	}
	if c >= utf8.RuneSelf {
		r, _ := utf8.DecodeRuneInString(s[i:])
		return unicode.IsLetter(r)
	}
	return false
}

func wordEnd(s string, i int) int {
	j := i + 1
	for j < len(s) {
		c := s[j]
		if c == '_' || c == '$' || c == '@' || c == '#' || isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			j++
			continue
		}
		if c >= utf8.RuneSelf {
			r, w := utf8.DecodeRuneInString(s[j:])
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				j += w
				continue
			}
		}
		break
	}
	return j
}

// Split breaks sql into statements at top-level semicolons, honoring
// quotes, comments, dollar quotes and procedural BEGIN ... END bodies.
// Each statement keeps its original text (including leading comments),
// trimmed; statements with no SQL tokens are dropped.
func Split(f Family, sql string) []string {
	toks := lex(f, sql)
	var out []string
	segStart := 0
	first := 0 // index of the current statement's first token
	var blk blockState
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if i == first {
			blk = newBlockState(f, toks[i:])
		}
		if t.isPunct(";") && t.depth == 0 && blk.closed() {
			if i > first {
				out = append(out, strings.TrimSpace(sql[segStart:t.start]))
			}
			segStart, first = t.end, i+1
			continue
		}
		i += blk.step(toks, i)
	}
	if first < len(toks) {
		out = append(out, strings.TrimSpace(sql[segStart:]))
	}
	return out
}

// blockState tracks BEGIN/CASE ... END nesting inside procedural code so
// that semicolons inside a block do not end the statement.
type blockState struct {
	family   Family
	tracking bool // count BEGIN/CASE/END for this statement
	needBody bool // Oracle/DM: semicolons do not end the statement until the body opens
	asOpens  bool // Oracle/DM package or type body: the first top-level AS/IS opens the body
	depth    int
}

func (b *blockState) closed() bool { return b.depth == 0 && !b.needBody }

func newBlockState(f Family, toks []token) blockState {
	b := blockState{family: f}
	head := toks[0]
	plsql := f == FamilyOracle || f == FamilyDM
	if plsql || f == FamilySQLServer {
		b.tracking = true
		b.needBody = plsql && head.isWord("DECLARE")
	}
	if head.isWord("CREATE") {
		switch obj, body := routineObject(toks); obj {
		case "PROCEDURE", "FUNCTION", "TRIGGER", "EVENT":
			b.tracking = true
			b.needBody = plsql
		case "PACKAGE":
			b.tracking = true
			b.needBody, b.asOpens = plsql, plsql
		case "TYPE":
			if body {
				b.tracking = true
				b.needBody, b.asOpens = plsql, plsql
			}
		}
	}
	return b
}

// routineObject finds the object type named by a CREATE statement and
// whether it is followed by BODY.
func routineObject(toks []token) (string, bool) {
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t.isPunct(";") && t.depth == 0 {
			break
		}
		if t.isWord("PROCEDURE", "FUNCTION", "TRIGGER", "EVENT", "PACKAGE", "TYPE",
			"TABLE", "VIEW", "INDEX", "SEQUENCE", "SCHEMA", "DATABASE", "USER", "ROLE", "SYNONYM") {
			return t.up, i+1 < len(toks) && toks[i+1].isWord("BODY")
		}
	}
	return "", false
}

// endsControlBlock reports whether END next closes a procedural control
// statement (END IF, END LOOP, ...) that never opened a level. Only
// dialects with such statements are listed; SQL Server has none, so a
// SQL Server END always closes BEGIN or CASE (e.g. CASE ... END FOR XML).
func (b *blockState) endsControlBlock(next token) bool {
	switch b.family {
	case FamilyOracle, FamilyDM:
		return next.isWord("IF", "LOOP")
	case FamilyMySQL:
		return next.isWord("IF", "LOOP", "WHILE", "REPEAT")
	}
	return false
}

// step updates the block state for toks[i] and returns how many following
// tokens it consumed.
func (b *blockState) step(toks []token, i int) int {
	if !b.tracking {
		return 0
	}
	t := toks[i]
	if t.kind != tokWord {
		return 0
	}
	next := token{}
	if i+1 < len(toks) {
		next = toks[i+1]
	}
	switch t.up {
	case "BEGIN":
		if b.family == FamilySQLServer && next.isWord("TRAN", "TRANSACTION", "DISTRIBUTED") {
			return 0
		}
		b.depth++
		b.needBody = false
	case "CASE":
		b.depth++
	case "END":
		if b.endsControlBlock(next) {
			return 0
		}
		if b.depth > 0 {
			b.depth--
		}
		if next.isWord("CASE") {
			return 1 // END CASE closes the CASE level; do not reopen it
		}
	case "AS", "IS":
		if b.asOpens && t.depth == 0 {
			b.asOpens = false
			b.needBody = false
			b.depth++
		}
	}
	return 0
}
