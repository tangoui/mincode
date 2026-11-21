package db

import "testing"

func TestNormalizeCloudReadOnlyQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    string
		wantErr bool
	}{
		{name: "select", query: "SELECT id FROM users", want: "SELECT id FROM users"},
		{name: "existing limit", query: "select id from users limit 10", want: "select id from users limit 10"},
		{name: "string keyword", query: "select 'delete from users' as text", want: "select 'delete from users' as text"},
		{name: "comment keyword", query: "select 1 /* delete from users */", want: "select 1 /* delete from users */"},
		{name: "delete", query: "delete from users", wantErr: true},
		{name: "cte delete", query: "with removed as (delete from users returning id) select * from removed", wantErr: true},
		{name: "for update", query: "select * from users for update", wantErr: true},
		{name: "multiple", query: "select 1; select 2", wantErr: true},
		{name: "unterminated", query: "select 'x", wantErr: true},
		{name: "comment ended by bare CR", query: "/**/ SELECT 1 --x\r; COMMIT; DELETE FROM t", wantErr: true},
		{name: "comment ended by CRLF", query: "select 1 --x\r\n; delete from t", wantErr: true},
		{name: "comment text stays a comment", query: "select 1 -- delete from t", want: "select 1 -- delete from t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeCloudReadOnlyQuery(tt.query)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
