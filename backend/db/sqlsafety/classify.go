package sqlsafety

import "strings"

// Kind is the coarse category of a statement.
type Kind string

const (
	KindRead      Kind = "read"
	KindData      Kind = "data"
	KindInsert    Kind = "insert"
	KindStructure Kind = "structure"
	KindProcedure Kind = "procedure"
	KindScript    Kind = "script"
	KindUnknown   Kind = "unknown"
	// KindRows is never classified: it is the kind of the one record a
	// grid change group (updates, inserts, deletes by key) is saved as.
	KindRows Kind = "rows"
)

// TableRef names a table, optionally schema-qualified. Quotes are removed.
type TableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// Statement is the classification of one SQL statement.
//
// Table, Alias, Where and SetColumns are filled for a single-table UPDATE/DELETE
// that can be rewritten into a snapshot SELECT (Rewritable), and Table also
// for single-table DROP TABLE / TRUNCATE (Destructive) and for a plain
// single-table INSERT ... VALUES / INSERT ... SELECT (with InsertRows or
// InsertQuery).
type Statement struct {
	Index       int       `json:"index"`
	Text        string    `json:"text"`
	Kind        Kind      `json:"kind"`
	Table       *TableRef `json:"table,omitempty"`
	Alias       string    `json:"alias,omitempty"` // target alias of a rewritable UPDATE/DELETE
	Where       string    `json:"where"`
	SetColumns  []string  `json:"setColumns,omitempty"`
	Rewritable  bool      `json:"rewritable"`
	Destructive bool      `json:"destructive"`
	InsertRows  int64     `json:"insertRows,omitempty"`  // VALUES tuples of a plain INSERT (DEFAULT VALUES: 1)
	InsertQuery string    `json:"insertQuery,omitempty"` // the SELECT/WITH query of INSERT ... SELECT
	Reasons     []Reason  `json:"reasons,omitempty"`
}

// Reason explains why a statement or check is risky or not undoable.
type Reason string

const (
	ReasonOverThreshold       Reason = "overThreshold"
	ReasonNoWhere             Reason = "noWhere"
	ReasonAlwaysTrue          Reason = "alwaysTrueWhere"
	ReasonProcedure           Reason = "procedure"
	ReasonNoEstimate          Reason = "noEstimate"
	ReasonUnparsed            Reason = "unparsed"
	ReasonNoPrimaryKey        Reason = "noPrimaryKey"
	ReasonCascade             Reason = "cascade"
	ReasonTrigger             Reason = "trigger"
	ReasonOverSnapshotLimit   Reason = "overSnapshotLimit"
	ReasonNoTransaction       Reason = "noTransaction"
	ReasonNotUndoableStore    Reason = "notUndoableStore"
	ReasonStructure           Reason = "structure"
	ReasonExplicitTransaction Reason = "explicitTransaction"
	ReasonEstimated           Reason = "estimated"
	// ReasonRowChangedTwice: a grid change group changes one row in more
	// than one way (other than delete then insert again), which undo cannot reverse.
	ReasonRowChangedTwice Reason = "rowChangedTwice"
	// ReasonTableNotFound: the statement parsed, but its target table is
	// not in the database / schema the statement runs in (unqualified names
	// resolve in the connection's current database or search path).
	ReasonTableNotFound Reason = "tableNotFound"
)

// IsReadOnly reports whether stmt is on the read-only whitelist.
func IsReadOnly(f Family, stmt string) bool {
	return Classify(f, stmt).Kind == KindRead
}

// Classify categorizes a single statement. For FamilyNone only Kind (and
// Destructive/Table for DROP TABLE / TRUNCATE) is determined; nothing is
// rewritable. For other families, a data statement that is not Rewritable
// always carries ReasonUnparsed.
func Classify(f Family, stmt string) Statement {
	text := strings.TrimSpace(stmt)
	toks, execComment := lexExpanded(f, text)
	for len(toks) > 0 && toks[len(toks)-1].isPunct(";") {
		toks = toks[:len(toks)-1]
	}
	st := Statement{Text: text, Kind: KindUnknown}
	if len(toks) == 0 {
		return st
	}
	innerSemicolon := false
	for _, t := range toks {
		if t.isPunct(";") && t.depth == 0 {
			innerSemicolon = true
		}
	}
	if innerSemicolon && len(Split(f, text)) > 1 {
		return st // several statements: callers must Split first
	}
	c := classifier{family: f, text: text}
	c.classify(&st, toks)
	// Defence in depth: text that may hide a second statement is never
	// read-only and never rewritten. A semicolon left inside one segment
	// belongs to a procedural block (or an unclosed one); an executable
	// comment is SQL the server runs but the WHERE text cannot carry.
	if st.Kind == KindRead && (innerSemicolon || execComment || embedsStatement(toks)) {
		st.Kind = KindUnknown
	}
	if innerSemicolon || execComment {
		st.Rewritable = false
		if st.Kind == KindInsert {
			st.Table, st.InsertRows, st.InsertQuery = nil, 0, ""
		}
	}
	if f != FamilyNone && st.Kind == KindData && !st.Rewritable {
		st.Table, st.Alias, st.Where, st.SetColumns = nil, "", "", nil
		st.addReason(ReasonUnparsed)
	}
	return st
}

func (st *Statement) addReason(r Reason) {
	for _, have := range st.Reasons {
		if have == r {
			return
		}
	}
	st.Reasons = append(st.Reasons, r)
}

type classifier struct {
	family Family
	text   string
}

func (c *classifier) classify(st *Statement, toks []token) {
	head := toks[0]
	if head.kind != tokWord {
		return
	}
	at := func(i int) token {
		if i < len(toks) {
			return toks[i]
		}
		return token{}
	}
	switch head.up {
	case "SELECT":
		st.Kind = KindRead
		if findTop(toks, "INTO") >= 0 {
			st.Kind = KindData
		}
	case "SHOW", "VALUES", "TABLE":
		st.Kind = KindRead
	case "EXPLAIN", "DESCRIBE", "DESC":
		c.classifyExplain(st, toks)
	case "PRAGMA":
		if readOnlyPragma(toks) {
			st.Kind = KindRead
		}
	case "WITH":
		c.classifyWith(st, toks)
	case "UPDATE":
		st.Kind = KindData
		c.parseUpdate(st, toks)
	case "DELETE":
		st.Kind = KindData
		c.parseDelete(st, toks)
	case "MERGE", "REPLACE", "UPSERT":
		st.Kind = KindData
	case "INSERT":
		st.Kind = KindInsert
		for i := 1; i+1 < len(toks); i++ {
			if toks[i].depth == 0 && toks[i].isWord("ON") && toks[i+1].isWord("DUPLICATE", "CONFLICT") {
				st.Kind = KindData
			}
		}
		// SQLite INSERT OR REPLACE deletes the rows it conflicts with, and a
		// data-modifying CTE in the query changes another table: neither is
		// a plain insert whose new keys describe the whole change.
		if at(1).isWord("OR") && at(2).isWord("REPLACE") || modifiesInQuery(toks) {
			st.Kind = KindData
		}
		if st.Kind == KindInsert && c.family != FamilyNone {
			c.parseInsert(st, toks)
		}
	case "COPY":
		if findTop(toks, "FROM") >= 0 {
			st.Kind = KindData
		}
	case "LOAD":
		if at(1).isWord("DATA", "XML") {
			st.Kind = KindData
		}
	case "DROP":
		st.Kind = KindStructure
		i := 1
		if at(i).isWord("TEMPORARY") {
			i++
		}
		if at(i).isWord("TABLE") {
			c.markDestructive(st, toks, i+1)
		}
	case "TRUNCATE":
		st.Kind = KindStructure
		i := 1
		if at(i).isWord("TABLE") {
			i++
		}
		c.markDestructive(st, toks, i)
	case "ALTER":
		st.Kind = KindStructure
		if at(1).isWord("TABLE") {
			if _, i, ok := parseTableRef(toks, 2); ok {
				if at(i).isWord("ON") && at(i+1).isWord("CLUSTER") {
					i += 3
				}
				if at(i).isWord("DELETE", "UPDATE") {
					st.Kind = KindData
				}
			}
		}
	case "CREATE", "GRANT", "REVOKE", "RENAME", "COMMENT":
		st.Kind = KindStructure
	case "CALL", "EXEC", "EXECUTE", "DO", "DECLARE":
		st.Kind = KindProcedure
	case "BEGIN":
		st.Kind = KindProcedure
		if c.family != FamilyOracle && c.family != FamilyDM &&
			(len(toks) == 1 || at(1).isWord("TRAN", "TRANSACTION", "WORK", "DEFERRED", "IMMEDIATE", "EXCLUSIVE", "ISOLATION", "READ", "DISTRIBUTED")) {
			st.Kind = KindScript
		}
	case "START":
		if at(1).isWord("TRANSACTION") {
			st.Kind = KindScript
		}
	case "COMMIT", "ROLLBACK":
		st.Kind = KindScript
	}
}

// classifyExplain treats plain EXPLAIN/DESCRIBE as read-only. With ANALYZE
// the explained statement runs, so the result takes the inner kind (or
// unknown when the inner statement is a read).
func (c *classifier) classifyExplain(st *Statement, toks []token) {
	st.Kind = KindRead
	analyze := false
	i := 1
	if i < len(toks) && toks[i].isWord("ANALYZE", "ANALYSE") {
		analyze = true
	} else if i < len(toks) && toks[i].isPunct("(") {
		for j := i + 1; j < len(toks) && toks[j].depth > 0; j++ {
			if toks[j].isWord("ANALYZE", "ANALYSE") {
				analyze = true
			}
		}
	}
	if !analyze {
		return
	}
	st.Kind = KindUnknown
	for j := i; j < len(toks); j++ {
		if toks[j].depth == 0 && toks[j].isWord("SELECT", "WITH", "VALUES", "TABLE", "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "CREATE", "EXECUTE") {
			var inner Statement
			c.classify(&inner, toks[j:])
			if inner.Kind != KindRead {
				st.Kind = inner.Kind
			}
			return
		}
	}
}

// classifyWith classifies a WITH statement by its main body, escalating
// when a CTE itself modifies data. Results are never rewritable.
func (c *classifier) classifyWith(st *Statement, toks []token) {
	mainAt := -1
	for i := 1; i < len(toks); i++ {
		if toks[i].depth == 0 && toks[i].isWord("SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "VALUES", "TABLE", "REPLACE") {
			mainAt = i
			break
		}
	}
	if mainAt < 0 {
		return
	}
	cte := KindRead
	for i := 1; i < mainAt; i++ {
		t := toks[i]
		switch {
		case t.isWord("DELETE", "MERGE"),
			t.isWord("UPDATE") && !toks[i-1].isWord("FOR", "KEY"):
			cte = KindData
		case t.isWord("INSERT") && cte == KindRead:
			cte = KindInsert
		}
	}
	var inner Statement
	c.classify(&inner, toks[mainAt:])
	st.Kind = inner.Kind
	if rank(cte) > rank(st.Kind) {
		st.Kind = cte
	}
	for _, r := range inner.Reasons {
		if r != ReasonUnparsed {
			st.addReason(r)
		}
	}
}

func rank(k Kind) int {
	switch k {
	case KindRead:
		return 1
	case KindInsert:
		return 2
	case KindData:
		return 3
	}
	return 0
}

// destructiveOptions may follow the table of DROP TABLE / TRUNCATE without
// changing which rows it removes (CASCADE / RESTRICT / RESTART IDENTITY /
// CONTINUE IDENTITY / PURGE / DROP|REUSE STORAGE / CASCADE CONSTRAINTS).
var destructiveOptions = []string{"CASCADE", "RESTRICT", "RESTART", "CONTINUE", "IDENTITY", "PURGE", "DROP", "REUSE", "STORAGE", "CONSTRAINTS"}

// markDestructive flags DROP TABLE / TRUNCATE and records the table when
// exactly one is named. Anything else after the table (a PARTITION list,
// WITH (PARTITIONS ...)) adds ReasonUnparsed: the statement may remove only
// part of the table, so a whole-table snapshot does not describe it.
func (c *classifier) markDestructive(st *Statement, toks []token, i int) {
	st.Destructive = true
	if i+1 < len(toks) && toks[i].isWord("IF") && toks[i+1].isWord("EXISTS") {
		i += 2
	}
	if i < len(toks) && toks[i].isWord("ONLY") {
		i++
	}
	ref, next, ok := parseTableRef(toks, i)
	if next < len(toks) && toks[next].isPunct("*") {
		next++
	}
	if !ok || (next < len(toks) && toks[next].isPunct(",")) {
		return
	}
	st.Table = &ref
	for _, t := range toks[next:] {
		if !t.isWord(destructiveOptions...) {
			st.addReason(ReasonUnparsed)
			return
		}
	}
}

// clauseWords end an UPDATE/DELETE target or SET list; any of them other
// than WHERE and RETURNING makes the statement unparsed.
var clauseWords = map[string]bool{
	"SET": true, "WHERE": true, "RETURNING": true, "FROM": true, "USING": true,
	"JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true, "CROSS": true,
	"NATURAL": true, "STRAIGHT_JOIN": true, "ORDER": true, "LIMIT": true, "OUTPUT": true,
	"OPTION": true, "WITH": true, "PARTITION": true, "USE": true, "FORCE": true, "IGNORE": true,
}

// whereStops end a WHERE condition and make the statement unparsed.
var whereStops = map[string]bool{"ORDER": true, "LIMIT": true, "OFFSET": true, "FETCH": true, "OPTION": true, "OUTPUT": true}

func (c *classifier) parseUpdate(st *Statement, toks []token) {
	i := 1
	for i < len(toks) && toks[i].isWord("LOW_PRIORITY", "IGNORE") {
		i++
	}
	if i < len(toks) && toks[i].isWord("ONLY") {
		// ONLY excludes inheriting/partition tables; a snapshot of the
		// parent cannot express that, so it is not rewritten.
		c.finishUnparsed(st, toks)
		return
	}
	tg, i, ok := c.parseTarget(toks, i)
	if !ok || i >= len(toks) || !toks[i].isWord("SET") {
		c.finishUnparsed(st, toks)
		return
	}
	i++
	setStart := i
	for i < len(toks) && !(toks[i].depth == 0 && toks[i].kind == tokWord && clauseWords[toks[i].up]) {
		i++
	}
	cols, ok := setColumns(toks[setStart:i])
	if !ok || hidesStatement(toks, setStart, i) {
		c.finishUnparsed(st, toks)
		return
	}
	st.SetColumns = cols
	c.parseTail(st, toks, i, tg)
}

func (c *classifier) parseDelete(st *Statement, toks []token) {
	i := 1
	for i < len(toks) && toks[i].isWord("LOW_PRIORITY", "QUICK", "IGNORE") {
		i++
	}
	if i < len(toks) && toks[i].isWord("FROM") {
		i++
	}
	if i < len(toks) && toks[i].isWord("ONLY") {
		c.finishUnparsed(st, toks) // see parseUpdate
		return
	}
	tg, i, ok := c.parseTarget(toks, i)
	if !ok {
		c.finishUnparsed(st, toks)
		return
	}
	c.parseTail(st, toks, i, tg)
}

// parseTarget parses "table [*] [[AS] alias]" starting at toks[i].
func (c *classifier) parseTarget(toks []token, i int) (target, int, bool) {
	ref, i, ok := parseTableRef(toks, i)
	tg := target{ref: ref}
	if !ok {
		return tg, i, false
	}
	if i < len(toks) && toks[i].isPunct("*") {
		i++
	}
	if i < len(toks) && toks[i].isWord("AS") {
		if i+1 >= len(toks) || !toks[i+1].isName() {
			return tg, i, false
		}
		tg.alias = toks[i+1].name()
		i += 2
	} else if i < len(toks) && toks[i].isName() && !(toks[i].kind == tokWord && (clauseWords[toks[i].up] || statementStarters[toks[i].up])) {
		tg.alias = toks[i].name()
		i++
	}
	return tg, i, true
}

// target is the table and optional alias of an UPDATE/DELETE.
type target struct {
	ref   TableRef
	alias string
}

// parseTail handles "[WHERE cond] [RETURNING ...]" at toks[i] and
// finalizes a successfully parsed UPDATE/DELETE.
func (c *classifier) parseTail(st *Statement, toks []token, i int, tg target) {
	var cond []token
	if i < len(toks) && toks[i].isWord("WHERE") {
		j := i + 1
		for j < len(toks) && !(toks[j].depth == 0 && toks[j].isWord("RETURNING")) {
			if (toks[j].depth == 0 && toks[j].kind == tokWord && whereStops[toks[j].up]) || hidesStatement(toks, j, j+1) {
				c.finishUnparsed(st, toks)
				return
			}
			j++
		}
		cond = toks[i+1 : j]
		if len(cond) == 0 || cond[0].isWord("CURRENT") || refersTo(cond, tg.ref.Name) {
			c.finishUnparsed(st, toks)
			return
		}
		i = j
	}
	if i < len(toks) && !toks[i].isWord("RETURNING") {
		c.finishUnparsed(st, toks)
		return
	}
	if c.family == FamilyNone {
		st.SetColumns = nil
		return
	}
	ref := tg.ref
	st.Table = &ref
	st.Alias = tg.alias
	st.Rewritable = true
	if len(cond) == 0 {
		st.addReason(ReasonNoWhere)
		return
	}
	st.Where = c.text[cond[0].start:cond[len(cond)-1].end]
	if alwaysTrue(cond) {
		st.addReason(ReasonAlwaysTrue)
	}
}

// parseInsert fills Table and InsertRows or InsertQuery for
// "INSERT [INTO] table [(cols)] VALUES (...), ... [RETURNING ...]",
// "... DEFAULT VALUES" and "... SELECT|WITH ... [RETURNING ...]". Any other
// shape (INSERT ALL, INSERT ... SET, OUTPUT, PARTITION, ...) leaves them empty.
func (c *classifier) parseInsert(st *Statement, toks []token) {
	i := 1
	for i < len(toks) && toks[i].isWord("LOW_PRIORITY", "DELAYED", "HIGH_PRIORITY", "IGNORE") {
		i++
	}
	if i >= len(toks) || !toks[i].isWord("INTO") {
		return
	}
	ref, i, ok := parseTableRef(toks, i+1)
	if !ok {
		return
	}
	if i < len(toks) && toks[i].isPunct("(") {
		for i++; i < len(toks) && !(toks[i].depth == 0 && toks[i].isPunct(")")); i++ {
		}
		i++
	}
	end := len(toks)
	for j := i; j < len(toks); j++ {
		if toks[j].depth == 0 && toks[j].isWord("RETURNING") {
			end = j
			break
		}
	}
	if i >= end {
		return
	}
	switch {
	case toks[i].isWord("DEFAULT") && i+2 == end && toks[i+1].isWord("VALUES"):
		st.InsertRows = 1
	case toks[i].isWord("VALUES"):
		n, ok := countTuples(toks[i+1 : end])
		if !ok {
			return
		}
		st.InsertRows = n
	case toks[i].isWord("SELECT", "WITH"):
		query := toks[i:end]
		if embedsStatement(query) {
			return
		}
		st.InsertQuery = c.text[query[0].start:query[len(query)-1].end]
	default:
		return
	}
	st.Table = &ref
}

// modifiesInQuery reports a nested INSERT / UPDATE / DELETE / MERGE after
// the leading INSERT, such as a Postgres data-modifying CTE. Row-locking
// "FOR [NO KEY] UPDATE" does not count.
func modifiesInQuery(toks []token) bool {
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t.isWord("INSERT", "DELETE", "MERGE") || t.isWord("UPDATE") && !toks[i-1].isWord("FOR", "KEY") {
			return true
		}
	}
	return false
}

// countTuples counts "(...), (...)" (MySQL also "ROW(...)") at depth 0.
func countTuples(toks []token) (int64, bool) {
	var n int64
	i := 0
	for {
		if i < len(toks) && toks[i].isWord("ROW") {
			i++
		}
		if i >= len(toks) || toks[i].depth != 0 || !toks[i].isPunct("(") {
			return 0, false
		}
		for i++; i < len(toks) && !(toks[i].depth == 0 && toks[i].isPunct(")")); i++ {
		}
		if i >= len(toks) {
			return 0, false
		}
		n++
		i++
		if i == len(toks) {
			return n, true
		}
		if !toks[i].isPunct(",") {
			return 0, false
		}
		i++
	}
}

// finishUnparsed records the no-WHERE reason for an UPDATE/DELETE that
// could not be parsed; Classify adds ReasonUnparsed.
func (c *classifier) finishUnparsed(st *Statement, toks []token) {
	st.SetColumns = nil
	if c.family != FamilyNone && findTop(toks, "WHERE") < 0 {
		st.addReason(ReasonNoWhere)
	}
}

// parseTableRef parses "name" or "schema.name" at toks[i].
func parseTableRef(toks []token, i int) (TableRef, int, bool) {
	var parts []string
	for {
		if i >= len(toks) || !toks[i].isName() {
			return TableRef{}, i, false
		}
		parts = append(parts, toks[i].name())
		i++
		if i+1 < len(toks) && toks[i].isPunct(".") {
			i++
			continue
		}
		break
	}
	switch len(parts) {
	case 1:
		return TableRef{Name: parts[0]}, i, true
	case 2:
		return TableRef{Schema: parts[0], Name: parts[1]}, i, true
	}
	return TableRef{}, i, false
}

// setColumns extracts assigned column names from "a = x, t.b = y".
func setColumns(toks []token) ([]string, bool) {
	var cols []string
	for len(toks) > 0 {
		end := 0
		for end < len(toks) && !(toks[end].depth == 0 && toks[end].isPunct(",")) {
			end++
		}
		item := toks[:end]
		eq := -1
		for k, t := range item {
			if t.depth == 0 && t.isPunct("=") {
				eq = k
				break
			}
		}
		if eq <= 0 || eq%2 == 0 {
			return nil, false
		}
		for k := 0; k < eq; k++ {
			if (k%2 == 0 && !item[k].isName()) || (k%2 == 1 && !item[k].isPunct(".")) {
				return nil, false
			}
		}
		cols = append(cols, item[eq-1].name())
		if end == len(toks) {
			break
		}
		toks = toks[end+1:]
	}
	return cols, len(cols) > 0
}

// refersTo reports whether cond names the target table other than as a
// column qualifier ("t.col"), e.g. in a self-referencing subquery.
func refersTo(cond []token, table string) bool {
	for k, t := range cond {
		if !t.isName() || !strings.EqualFold(t.name(), table) {
			continue
		}
		if k+1 < len(cond) && cond[k+1].isPunct(".") {
			continue
		}
		return true
	}
	return false
}

func findTop(toks []token, word string) int {
	for i, t := range toks {
		if t.depth == 0 && t.isWord(word) {
			return i
		}
	}
	return -1
}

// alwaysTrue reports whether cond is a constant tautology such as 1=1,
// TRUE, 1, 'a'='a', or an OR containing one.
func alwaysTrue(cond []token) bool {
	cond = stripParens(cond)
	if len(cond) == 0 {
		return false
	}
	base := cond[0].depth
	if parts := splitTop(cond, base, "OR"); len(parts) > 1 {
		for _, p := range parts {
			if alwaysTrue(p) {
				return true
			}
		}
		return false
	}
	if parts := splitTop(cond, base, "AND"); len(parts) > 1 {
		for _, p := range parts {
			if !alwaysTrue(p) {
				return false
			}
		}
		return true
	}
	switch len(cond) {
	case 1:
		t := cond[0]
		return t.isWord("TRUE") || (t.kind == tokNumber && strings.Trim(t.text, "0.") != "")
	case 3:
		l, op, r := cond[0], cond[1], cond[2]
		literal := l.kind == tokNumber || l.kind == tokString
		return literal && l.kind == r.kind && l.text == r.text && (op.isPunct("=") || op.isPunct("=="))
	}
	return false
}

func stripParens(toks []token) []token {
	for len(toks) >= 2 && toks[0].isPunct("(") && toks[len(toks)-1].isPunct(")") && toks[0].depth == toks[len(toks)-1].depth {
		inner := toks[1 : len(toks)-1]
		balanced := true
		for _, t := range inner {
			if t.depth <= toks[0].depth {
				balanced = false
				break
			}
		}
		if !balanced {
			break
		}
		toks = inner
	}
	return toks
}

func splitTop(toks []token, depth int, word string) [][]token {
	var parts [][]token
	start := 0
	for i, t := range toks {
		if t.depth == depth && t.isWord(word) {
			parts = append(parts, toks[start:i])
			start = i + 1
		}
	}
	return append(parts, toks[start:])
}

// statementStarters are keywords that begin a statement. SQL Server runs
// "SELECT 1 DELETE FROM t" as two statements without a semicolon, so any
// of these at the top level of a query, SET list or WHERE means the text
// may hide another statement. Columns that share these names only cost an
// extra confirmation.
var statementStarters = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		INSERT UPDATE DELETE MERGE TRUNCATE ALTER CREATE DROP GRANT REVOKE DENY
		EXEC EXECUTE CALL BEGIN DECLARE SET USE IF WHILE GOTO RETURN BREAK CONTINUE
		COMMIT ROLLBACK SAVE OPEN CLOSE FETCH DEALLOCATE RAISERROR THROW PRINT WAITFOR
		SHUTDOWN KILL DBCC BACKUP RESTORE CHECKPOINT RECONFIGURE BULK
		DISABLE ENABLE READTEXT WRITETEXT UPDATETEXT
		RECEIVE SEND MOVE GET ADD SETUSER REVERT END`) {
		statementStarters[w] = true
	}
}

// startsStatement reports whether toks[i] is a top-level statement keyword,
// excluding the in-query uses SELECT ... FOR [NO KEY] UPDATE,
// OFFSET ... FETCH FIRST|NEXT n ROWS and the IF(...) function; END only
// starts END CONVERSATION. A pass-through rowset function (OPENQUERY and
// friends) counts at any depth, since the linked server may run DML.
func startsStatement(toks []token, i int) bool {
	t := toks[i]
	if t.isWord("OPENQUERY", "OPENROWSET", "OPENDATASOURCE") {
		return true
	}
	if t.depth != 0 || t.kind != tokWord || !statementStarters[t.up] {
		return false
	}
	at := func(k int) token {
		if k >= 0 && k < len(toks) {
			return toks[k]
		}
		return token{}
	}
	switch t.up {
	case "UPDATE":
		return !at(i-1).isWord("FOR", "KEY")
	case "FETCH":
		return !(at(i+1).isWord("FIRST", "NEXT") && !at(i+2).isWord("FROM"))
	case "IF":
		return !at(i + 1).isPunct("(")
	case "END":
		return at(i + 1).isWord("CONVERSATION")
	}
	return true
}

// embedsStatement reports whether a SELECT/WITH/VALUES/TABLE query has a
// statement keyword at its top level.
func embedsStatement(toks []token) bool {
	if !toks[0].isWord("SELECT", "WITH", "VALUES", "TABLE") {
		return false
	}
	for i := range toks {
		if startsStatement(toks, i) {
			return true
		}
	}
	return false
}

// hidesStatement reports whether toks[lo:hi], part of an UPDATE SET list
// or WHERE condition, has a top-level statement keyword (including SELECT
// or WITH, which there can only start a new statement).
func hidesStatement(toks []token, lo, hi int) bool {
	for i := lo; i < hi; i++ {
		if startsStatement(toks, i) || (toks[i].depth == 0 && toks[i].isWord("SELECT", "WITH")) {
			return true
		}
	}
	return false
}

// SQLite pragmas that only report state. queryPragmas are read-only only
// without an argument (with one they set the value); argPragmas take an
// object name or limit as their argument.
var (
	queryPragmas = map[string]bool{
		"database_list": true, "collation_list": true, "compile_options": true, "function_list": true,
		"module_list": true, "pragma_list": true, "table_list": true, "foreign_key_check": true,
		"integrity_check": true, "quick_check": true, "schema_version": true, "user_version": true,
		"application_id": true, "journal_mode": true, "foreign_keys": true, "page_size": true,
		"page_count": true, "freelist_count": true, "encoding": true, "data_version": true,
		"auto_vacuum": true, "cache_size": true, "synchronous": true, "busy_timeout": true,
	}
	argPragmas = map[string]bool{
		"table_info": true, "table_xinfo": true, "index_list": true, "index_info": true,
		"index_xinfo": true, "foreign_key_list": true, "foreign_key_check": true,
		"integrity_check": true, "quick_check": true, "table_list": true,
	}
)

// readOnlyPragma accepts "PRAGMA [schema.]name" for queryPragmas and
// "PRAGMA [schema.]name(arg)" for argPragmas, nothing else.
func readOnlyPragma(toks []token) bool {
	i := 1
	if i+2 < len(toks) && toks[i].isName() && toks[i+1].isPunct(".") {
		i += 2
	}
	if i >= len(toks) || toks[i].kind != tokWord {
		return false
	}
	name := strings.ToLower(toks[i].text)
	rest := toks[i+1:]
	switch len(rest) {
	case 0:
		return queryPragmas[name]
	case 3:
		arg := rest[1].kind
		return argPragmas[name] && rest[0].isPunct("(") && rest[2].isPunct(")") &&
			(arg == tokWord || arg == tokIdent || arg == tokString || arg == tokNumber)
	}
	return false
}

// sqlStatementWords are statement keywords of SQL (any dialect): the
// classified ones plus verbs the classifier leaves unknown (SET, VACUUM,
// OPTIMIZE …). USE and FETCH are left out: query languages without SQL
// (nGQL) start reads with them.
var sqlStatementWords = map[string]bool{
	"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true, "SHOW": true, "EXPLAIN": true,
	"DESCRIBE": true, "DESC": true, "PRAGMA": true, "UPDATE": true, "DELETE": true, "MERGE": true,
	"REPLACE": true, "UPSERT": true, "INSERT": true, "COPY": true, "LOAD": true, "UNLOAD": true,
	"DROP": true, "CREATE": true, "ALTER": true, "TRUNCATE": true, "RENAME": true, "COMMENT": true,
	"GRANT": true, "REVOKE": true, "SET": true, "RESET": true, "VACUUM": true, "ANALYZE": true,
	"ANALYSE": true, "REFRESH": true, "OPTIMIZE": true, "FLASHBACK": true, "PURGE": true,
	"CALL": true, "EXEC": true, "EXECUTE": true, "DO": true, "DECLARE": true, "BEGIN": true,
	"START": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
	"LOCK": true, "UNLOCK": true, "PREPARE": true, "DEALLOCATE": true, "REINDEX": true,
	"CLUSTER": true, "CHECKPOINT": true, "ATTACH": true, "DETACH": true, "LISTEN": true,
	"NOTIFY": true, "UNLISTEN": true, "DISCARD": true, "IMPORT": true, "EXPORT": true,
	"MSCK": true, "INVALIDATE": true, "COMPUTE": true, "FLUSH": true, "KILL": true,
	"CACHE": true, "UNCACHE": true, "AUDIT": true, "NOAUDIT": true, "REORG": true,
	"RUNSTATS": true, "REPAIR": true, "CHECK": true, "HANDLER": true, "INSTALL": true,
	"UNINSTALL": true, "SHUTDOWN": true, "SECURITY": true, "LABEL": true, "ASSOCIATE": true,
	"DISASSOCIATE": true, "ADMINISTER": true, "ADD": true, "BACKUP": true, "RESTORE": true,
	"DFS": true, "REPL": true,
}

// LooksLikeSQL reports text whose first keyword (after opening
// parentheses) starts a SQL statement, that starts with "{" (a JDBC escape
// such as {call p()}), or that carries a MySQL executable comment: SQL even
// where the classifier could not clear it as a read.
func LooksLikeSQL(f Family, stmt string) bool {
	toks, execComment := lexExpanded(f, strings.TrimSpace(stmt))
	if execComment {
		return true
	}
	for _, t := range toks {
		if t.isPunct("(") {
			continue
		}
		return t.isPunct("{") || t.kind == tokWord && sqlStatementWords[t.up]
	}
	return false
}
