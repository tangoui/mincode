package sqlsafety

import (
	"slices"
	"testing"
)

func TestAssess(t *testing.T) {
	s := DefaultSettings()
	update := Classify(FamilyMySQL, "UPDATE t SET a = 1 WHERE id < 5000")
	counted := func(n int64) Estimate { return Estimate{Rows: n, Exact: true, Source: "count"} }
	cases := []struct {
		name    string
		st      Statement
		est     Estimate
		risk    Risk
		reasons []Reason
	}{
		{name: "read", st: Classify(FamilyMySQL, "SELECT * FROM t"), est: Estimate{Source: "none"}, risk: RiskNone},
		{name: "at threshold", st: update, est: counted(1000), risk: RiskLow},
		{name: "over threshold", st: update, est: counted(1001), risk: RiskHigh, reasons: []Reason{ReasonOverThreshold}},
		{name: "no estimate", st: update, est: Estimate{Source: "none"}, risk: RiskHigh, reasons: []Reason{ReasonNoEstimate}},
		{name: "procedure", st: Classify(FamilyMySQL, "CALL purge_all()"), est: Estimate{Source: "none"}, risk: RiskHigh, reasons: []Reason{ReasonProcedure}},
		{name: "no where", st: Classify(FamilyMySQL, "DELETE FROM t"), est: counted(3), risk: RiskHigh, reasons: []Reason{ReasonNoWhere}},
		{name: "always true", st: Classify(FamilyMySQL, "DELETE FROM t WHERE 1=1"), est: counted(3), risk: RiskHigh, reasons: []Reason{ReasonAlwaysTrue}},
		{name: "drop table", st: Classify(FamilyMySQL, "DROP TABLE t"), est: Estimate{Source: "none"}, risk: RiskHigh, reasons: []Reason{ReasonStructure}},
		{name: "truncate", st: Classify(FamilyMySQL, "TRUNCATE t"), est: Estimate{Source: "none"}, risk: RiskHigh, reasons: []Reason{ReasonStructure}},
		{name: "other structure", st: Classify(FamilyMySQL, "CREATE INDEX i ON t (a)"), est: Estimate{Source: "none"}, risk: RiskLow, reasons: []Reason{ReasonStructure}},
		{name: "insert", st: Classify(FamilyMySQL, "INSERT INTO t VALUES (1)"), est: Estimate{Source: "none"}, risk: RiskLow},
		{name: "insert over threshold", st: Classify(FamilyMySQL, "INSERT INTO t SELECT * FROM u"), est: counted(1001), risk: RiskHigh, reasons: []Reason{ReasonOverThreshold}},
		{name: "insert at threshold", st: Classify(FamilyMySQL, "INSERT INTO t SELECT * FROM u"), est: counted(1000), risk: RiskLow},
		{name: "unknown", st: Classify(FamilyMySQL, "FLUSH TABLES"), est: Estimate{Source: "none"}, risk: RiskLow},
		{name: "explain estimate", st: update, est: Estimate{Rows: 20, Source: "explain"}, risk: RiskLow, reasons: []Reason{ReasonEstimated}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			risk, reasons := Assess(tc.st, tc.est, s)
			if risk != tc.risk {
				t.Fatalf("risk = %q, want %q (reasons %v)", risk, tc.risk, reasons)
			}
			for _, want := range tc.reasons {
				if !slices.Contains(reasons, want) {
					t.Fatalf("reasons %v missing %q", reasons, want)
				}
			}
			if tc.risk == RiskNone && len(reasons) != 0 {
				t.Fatalf("read statement has reasons %v", reasons)
			}
		})
	}
}

func TestAssessDoesNotAliasStatementReasons(t *testing.T) {
	st := Classify(FamilyMySQL, "DELETE FROM t")
	st.Reasons = append(make([]Reason, 0, 4), st.Reasons...)
	Assess(st, Estimate{Source: "none"}, DefaultSettings())
	if got := st.Reasons[:cap(st.Reasons)][1]; got != "" {
		t.Fatalf("Assess wrote into the statement's reason slice: %q", got)
	}
}

func TestConfirmTarget(t *testing.T) {
	cases := []struct {
		name  string
		stmts []StatementCheck
		want  string
	}{
		{name: "first high table", want: "orders", stmts: []StatementCheck{
			{Risk: RiskLow, Table: &TableRef{Name: "low_t"}},
			{Risk: RiskHigh, Table: &TableRef{Schema: "dbo", Name: "orders"}},
			{Risk: RiskHigh, Table: &TableRef{Name: "later"}},
		}},
		{name: "procedure name", want: "purge_all", stmts: []StatementCheck{
			{Risk: RiskHigh, Kind: KindProcedure, Text: "CALL `purge_all`(1)"},
		}},
		{name: "qualified procedure", want: "purge", stmts: []StatementCheck{
			{Risk: RiskHigh, Kind: KindProcedure, Text: "exec [dbo].[purge] @days = 1"},
		}},
		{name: "high without table", want: ConfirmLiteral, stmts: []StatementCheck{
			{Risk: RiskHigh, Kind: KindData, Text: "UPDATE a JOIN b ON a.id = b.id SET a.x = 1"},
		}},
		{name: "high without name", want: ConfirmLiteral, stmts: []StatementCheck{
			{Risk: RiskHigh, Kind: KindStructure, Text: "DROP TABLE a, b"},
			{Risk: RiskHigh, Table: &TableRef{Name: "later"}},
		}},
		{name: "procedure without name", want: ConfirmLiteral, stmts: []StatementCheck{
			{Risk: RiskHigh, Kind: KindProcedure, Text: "EXEC (@sql)"},
		}},
		{name: "no high", want: "", stmts: []StatementCheck{{Risk: RiskLow, Table: &TableRef{Name: "t"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfirmTarget(tc.stmts); got != tc.want {
				t.Fatalf("ConfirmTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfirmTargetMatches(t *testing.T) {
	cases := []struct {
		target, typed string
		want          bool
	}{
		{"Order", "`order`", true},
		{"dbo.Order", "[Order]", true},
		{"Order", "Orders", false},
		{"Order", ` public."ORDER" `, true},
		{"dbo.Order", "[dbo].[Order]", true},
		{"dbo.Order", "sales.Order", false},
		{"Order", "", false},
		{"", "", false},
		{ConfirmLiteral, "confirm", true},
		{ConfirmLiteral, " CONFIRM ", true},
		{ConfirmLiteral, "CONFIRMED", false},
	}
	for _, tc := range cases {
		if got := ConfirmTargetMatches(tc.target, tc.typed); got != tc.want {
			t.Errorf("ConfirmTargetMatches(%q, %q) = %v, want %v", tc.target, tc.typed, got, tc.want)
		}
	}
}
