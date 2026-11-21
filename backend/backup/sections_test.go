package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSectionForKeyMatchesTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sections.json"))
	if err != nil {
		t.Fatalf("read table: %v", err)
	}
	var table map[string]string
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse table: %v", err)
	}
	if len(table) < 14 {
		t.Fatalf("table has %d keys, want at least 14", len(table))
	}
	for key, want := range table {
		if got := SectionForKey(key); got != want {
			t.Errorf("SectionForKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCoreSectionsOrder(t *testing.T) {
	want := []string{SectionSettings, SectionSSH, SectionDatabase, SectionWorkspaces, SectionAI}
	if len(CoreSections) != len(want) {
		t.Fatalf("CoreSections = %v, want %v", CoreSections, want)
	}
	for i := range want {
		if CoreSections[i] != want[i] {
			t.Fatalf("CoreSections = %v, want %v", CoreSections, want)
		}
	}
}

func TestPluginSectionID(t *testing.T) {
	if got := PluginSectionID("http-tools"); got != "plugin:http-tools" {
		t.Fatalf("PluginSectionID = %q", got)
	}
	if got := PluginSectionID(""); got != "" {
		t.Fatalf("PluginSectionID(\"\") = %q, want empty", got)
	}
}

func TestIsPluginSection(t *testing.T) {
	cases := []struct {
		id     string
		want   string
		wantOK bool
	}{
		{"plugin:http-tools", "http-tools", true},
		{"settings", "", false},
		{"plugin:", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := IsPluginSection(c.id)
		if got != c.want || ok != c.wantOK {
			t.Errorf("IsPluginSection(%q) = (%q, %v), want (%q, %v)", c.id, got, ok, c.want, c.wantOK)
		}
	}
}
