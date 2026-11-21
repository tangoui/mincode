package sqlsafety

import (
	"strings"
	"time"
)

// Risk is how much confirmation a statement or check needs before it runs.
type Risk string

const (
	RiskNone Risk = "none"
	RiskLow  Risk = "low"
	RiskHigh Risk = "high"
)

// Estimate is how many rows a statement is expected to touch. Source is
// "count" (exact COUNT(*)), "explain" (planner estimate) or "none".
type Estimate struct {
	Rows   int64  `json:"rows"`
	Exact  bool   `json:"exact"`
	Source string `json:"source"`
}

// StatementCheck is the assessment of one statement inside a Check.
type StatementCheck struct {
	Index    int       `json:"index"`
	Text     string    `json:"text"` // truncated to 2000 characters
	Kind     Kind      `json:"kind"`
	Table    *TableRef `json:"table,omitempty"`
	Estimate Estimate  `json:"estimate"`
	Risk     Risk      `json:"risk"`
	Reasons  []Reason  `json:"reasons,omitempty"`
	Undoable bool      `json:"undoable"`
}

// Check is the pre-execution assessment of a query. CheckID is the one-time
// token the execution must present.
type Check struct {
	CheckID       string           `json:"checkId"`
	SessionID     string           `json:"sessionId"`
	Database      string           `json:"database"`
	Statements    []StatementCheck `json:"statements"`
	Risk          Risk             `json:"risk"`
	ConfirmTarget string           `json:"confirmTarget"`
	Undoable      bool             `json:"undoable"`
	ExpiresAt     time.Time        `json:"expiresAt"`

	queryHash string // the hash Tokens.Issue bound this check to; copies keep it
}

// Assess rates one classified statement. Reads are none; procedures,
// whole-table data changes (no WHERE or an always-true one), data changes
// without a row estimate or over s.HighRiskRows, inserts with a known count
// over s.HighRiskRows, and DROP TABLE / TRUNCATE are high; everything else
// is low. The returned reasons start with the
// statement's own reasons and never alias st.Reasons.
func Assess(st Statement, est Estimate, s Settings) (Risk, []Reason) {
	if st.Kind == KindRead {
		return RiskNone, nil
	}
	reasons := append([]Reason(nil), st.Reasons...)
	add := func(r Reason) {
		for _, have := range reasons {
			if have == r {
				return
			}
		}
		reasons = append(reasons, r)
	}
	risk := RiskLow
	switch st.Kind {
	case KindProcedure:
		add(ReasonProcedure)
		risk = RiskHigh
	case KindStructure:
		add(ReasonStructure)
		if st.Destructive {
			risk = RiskHigh
		}
	case KindData:
		switch {
		case est.Source == "none" || est.Source == "":
			add(ReasonNoEstimate)
			risk = RiskHigh
		case est.Rows > s.HighRiskRows:
			add(ReasonOverThreshold)
			risk = RiskHigh
		}
		if est.Source != "none" && est.Source != "" && !est.Exact {
			add(ReasonEstimated)
		}
	case KindInsert:
		// Inserts destroy nothing: only a known count over the threshold is high.
		if est.Source != "none" && est.Source != "" && est.Rows > s.HighRiskRows {
			add(ReasonOverThreshold)
			risk = RiskHigh
		}
	}
	for _, r := range reasons {
		if r == ReasonNoWhere || r == ReasonAlwaysTrue {
			risk = RiskHigh
		}
	}
	return risk, reasons
}

// ConfirmLiteral is the word to type when a high-risk statement names no
// table or procedure.
const ConfirmLiteral = "CONFIRM"

// ConfirmTarget is the name the user must type to run a high-risk check:
// the table (or, for a procedure call, the procedure) of the first
// high-risk statement, ConfirmLiteral when that statement names neither, and
// "" when no statement is high risk.
func ConfirmTarget(statements []StatementCheck) string {
	for _, sc := range statements {
		if sc.Risk != RiskHigh {
			continue
		}
		if sc.Table != nil && sc.Table.Name != "" {
			return sc.Table.Name
		}
		if sc.Kind == KindProcedure {
			if name := procedureName(sc.Text); name != "" {
				return name
			}
		}
		return ConfirmLiteral
	}
	return ""
}

// procedureName returns the unqualified routine name after CALL / EXEC /
// EXECUTE (skipping a T-SQL "@rc =" return target), or "".
func procedureName(text string) string {
	// SQLite lexing accepts all three identifier quotes: "x", `x`, [x].
	toks := lex(FamilySQLite, text)
	if len(toks) < 2 || !toks[0].isWord("CALL", "EXEC", "EXECUTE") {
		return ""
	}
	i := 1
	if strings.HasPrefix(toks[i].text, "@") && i+1 < len(toks) && toks[i+1].isPunct("=") {
		i += 2
	}
	name := ""
	for i < len(toks) && toks[i].isName() && !strings.HasPrefix(toks[i].text, "@") {
		name = toks[i].name()
		if i+2 < len(toks) && toks[i+1].isPunct(".") {
			i += 2
			continue
		}
		break
	}
	return name
}

// ConfirmTargetMatches reports whether typed names target. Quote characters
// (` " [ ]) and surrounding space are ignored, case does not matter, and
// either side may carry a schema prefix the other omits; when both carry
// one, the schemas must match too.
func ConfirmTargetMatches(target, typed string) bool {
	t, y := normalizeConfirmName(target), normalizeConfirmName(typed)
	if t == "" || y == "" {
		return false
	}
	if t == y {
		return true
	}
	tSchema, tName := splitConfirmName(t)
	ySchema, yName := splitConfirmName(y)
	return tName == yName && (tSchema == "" || ySchema == "")
}

var confirmQuotes = strings.NewReplacer("`", "", `"`, "", "[", "", "]", "")

func normalizeConfirmName(s string) string {
	return strings.ToLower(strings.TrimSpace(confirmQuotes.Replace(s)))
}

func splitConfirmName(s string) (schema, name string) {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return "", s
}
