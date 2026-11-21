package sqlsafety

import "testing"

// A line comment ends at CR as well as LF: PostgreSQL and others treat a bare
// CR as a line break, so statements after it run. Hiding them in the comment
// would let writes pass as reads (no confirmation, no snapshot).
func TestLineCommentEndsAtCarriageReturn(t *testing.T) {
	cases := []struct {
		family Family
		sql    string
	}{
		{FamilyPostgres, "/**/ SELECT 1 --x\r; COMMIT; DELETE FROM t"},
		{FamilyMySQL, "SELECT 1 -- x\r; DELETE FROM t"},
		{FamilyMySQL, "SELECT 1 # x\r; DELETE FROM t"},
		{FamilySQLServer, "SELECT 1 --x\r\n; DELETE FROM t"},
	}
	for _, tc := range cases {
		segments := Split(tc.family, tc.sql)
		if len(segments) < 2 {
			t.Errorf("%q: split into %d segment(s) %q; the DELETE after the comment must be its own statement", tc.sql, len(segments), segments)
			continue
		}
		last := segments[len(segments)-1]
		if IsReadOnly(tc.family, last) {
			t.Errorf("%q: last segment %q classified read-only", tc.sql, last)
		}
	}
	// A plain comment still swallows its own text.
	if got := Split(FamilyPostgres, "SELECT 1 -- DELETE FROM t"); len(got) != 1 || !IsReadOnly(FamilyPostgres, got[0]) {
		t.Errorf("comment text must stay a comment: %q", got)
	}
}
