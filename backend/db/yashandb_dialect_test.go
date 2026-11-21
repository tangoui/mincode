//go:build !lite

package db

import "testing"

// The yashandb dialect helpers stay in every non-Lite build: plugin-driver
// sessions of dialect yashandb reuse them in the lean profile.
func TestYashanDialectTableOperations(t *testing.T) {
	query, err := selectAllSQL("yashandb", "APP", "USERS", 50, 100)
	if err != nil {
		t.Fatalf("selectAllSQL: %v", err)
	}
	wantQuery := `SELECT * FROM "APP"."USERS" OFFSET 100 ROWS FETCH NEXT 50 ROWS ONLY`
	if query != wantQuery {
		t.Fatalf("selectAllSQL() = %q, want %q", query, wantQuery)
	}

	qualified, err := qualifiedTable("yashandb", "APP", "USERS")
	if err != nil {
		t.Fatalf("qualifiedTable: %v", err)
	}
	if qualified != `"APP"."USERS"` {
		t.Fatalf("qualifiedTable() = %q", qualified)
	}
	if got := placeholder("yashandb", 2); got != "?" {
		t.Fatalf("placeholder() = %q", got)
	}
	for _, action := range []string{"clear_table", "drop_table"} {
		if !supportsDestructiveOperation("yashandb", action) {
			t.Fatalf("expected %s to be supported", action)
		}
	}
	if supportsDestructiveOperation("yashandb", "drop_database") {
		t.Fatal("drop_database must remain disabled until YashanDB schema deletion is verified")
	}
}
