package db

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func exportTestResult() *QueryResult {
	return &QueryResult{
		Columns: []string{"id", "name", "note"},
		Rows: [][]any{
			{int64(1), "Ada", "line1\nline2 | pipe"},
			{int64(2), "O'Neil", "tab\tsep"},
		},
	}
}

func TestExportMarkdownEscapesCells(t *testing.T) {
	out := exportMarkdown(exportTestResult())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected header + separator + 2 rows, got %d lines", len(lines))
	}
	if lines[0] != "| id | name | note |" {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	if lines[1] != "| --- | --- | --- |" {
		t.Fatalf("unexpected separator: %q", lines[1])
	}
	if !strings.Contains(lines[2], "line1 line2 \\| pipe") {
		t.Fatalf("newline and pipe must be escaped: %q", lines[2])
	}
}

func TestExportTXTStripsControlChars(t *testing.T) {
	out := exportTXT(exportTestResult())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines", len(lines))
	}
	if lines[0] != "id\tname\tnote" {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	if strings.Contains(lines[1], "\n\n") || strings.Count(lines[1], "\t") != 2 {
		t.Fatalf("cell control characters must be replaced: %q", lines[1])
	}
	if !strings.Contains(lines[2], "tab sep") {
		t.Fatalf("tab in cell must be replaced with space: %q", lines[2])
	}
}

func TestExportInsertSQLRowsMode(t *testing.T) {
	out, err := exportInsertSQL(exportTestResult(), "mysql", "shop", "users", "rows")
	if err != nil {
		t.Fatalf("exportInsertSQL: %v", err)
	}
	want := "INSERT INTO `shop`.`users` (`id`, `name`, `note`)\nVALUES (1, 'Ada', 'line1\nline2 | pipe');\n" +
		"INSERT INTO `shop`.`users` (`id`, `name`, `note`)\nVALUES (2, 'O''Neil', 'tab\tsep');\n"
	if out != want {
		t.Fatalf("rows mode mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestExportInsertSQLBatchMode(t *testing.T) {
	out, err := exportInsertSQL(exportTestResult(), "postgres", "public", "users", "batch")
	if err != nil {
		t.Fatalf("exportInsertSQL: %v", err)
	}
	want := "INSERT INTO \"public\".\"users\" (\"id\", \"name\", \"note\")\nVALUES\n" +
		"  (1, 'Ada', 'line1\nline2 | pipe'),\n" +
		"  (2, 'O''Neil', 'tab\tsep');\n"
	if out != want {
		t.Fatalf("batch mode mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestExportInsertSQLBatchSplitsBySize(t *testing.T) {
	r := &QueryResult{Columns: []string{"id"}}
	for i := 0; i < exportSQLBatchSize+1; i++ {
		r.Rows = append(r.Rows, []any{int64(i)})
	}
	out, err := exportInsertSQL(r, "sqlite", "main", "ids", "batch")
	if err != nil {
		t.Fatalf("exportInsertSQL: %v", err)
	}
	if got := strings.Count(out, "INSERT INTO"); got != 2 {
		t.Fatalf("expected 2 batched statements, got %d", got)
	}
}

func TestSQLExportLiteral(t *testing.T) {
	moment := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		dialect string
		value   any
		want    string
	}{
		{"mysql", nil, "NULL"},
		{"mysql", "a\\b", "'a\\\\b'"},
		{"postgres", "a\\b", "'a\\b'"},
		{"sqlserver", true, "1"},
		{"postgres", false, "FALSE"},
		{"postgres", []byte{0xab, 0xcd}, "decode('ABCD', 'hex')"},
		{"sqlserver", []byte{0xab}, "0xAB"},
		{"mysql", []byte{0xab}, "X'AB'"},
		{"mysql", moment, "'2026-08-15 01:02:03'"},
		{"mysql", int64(42), "42"},
		{"mysql", 1.5, "1.5"},
	}
	for _, tc := range cases {
		if got := sqlExportLiteral(tc.dialect, tc.value); got != tc.want {
			t.Errorf("sqlExportLiteral(%q, %v) = %q, want %q", tc.dialect, tc.value, got, tc.want)
		}
	}
}

func TestExportXLSXRoundTrip(t *testing.T) {
	encoded, err := exportXLSX(&QueryResult{
		Columns: []string{"id", "name", "created_at", "blob"},
		Rows: [][]any{
			{int64(1), "Ada", time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC), []byte{0x0f}},
			{int64(2), nil, true, 1.5},
		},
	})
	if err != nil {
		t.Fatalf("exportXLSX: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "id" || rows[0][3] != "blob" {
		t.Fatalf("unexpected header: %v", rows[0])
	}
	if rows[1][0] != "1" || rows[1][1] != "Ada" {
		t.Fatalf("unexpected first row: %v", rows[1])
	}
	if rows[1][2] != "2026-08-15 01:02:03" {
		t.Fatalf("time must render as text, got %q", rows[1][2])
	}
	if rows[1][3] != "0x0F" {
		t.Fatalf("binary must render as hex text, got %q", rows[1][3])
	}
	if len(rows[2]) < 2 || rows[2][1] != "" {
		t.Fatalf("nil must render as empty cell, got %v", rows[2])
	}
}

func TestTruncateXLSXCell(t *testing.T) {
	long := strings.Repeat("好", xlsxCellMaxRunes+10)
	if got := len([]rune(truncateXLSXCell(long))); got != xlsxCellMaxRunes {
		t.Fatalf("expected truncation to %d runes, got %d", xlsxCellMaxRunes, got)
	}
}
