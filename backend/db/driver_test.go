package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestYashanClientLibraryName(t *testing.T) {
	if got := yashanClientLibraryName("windows"); got != "yascli.dll" {
		t.Fatalf("Windows library = %q", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := yashanClientLibraryName(goos); got != "libyascli.so" {
			t.Fatalf("%s library = %q", goos, got)
		}
	}
}

func TestFindYashanLibUsesConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	libName := yashanClientLibraryName(runtime.GOOS)
	libPath := filepath.Join(dir, libName)
	if err := os.WriteFile(libPath, []byte("test"), 0o600); err != nil {
		t.Fatalf("create test client library: %v", err)
	}
	t.Setenv("YASHANDB_LIB_DIR", dir)

	if got := findYashanLib(libName); got != libPath {
		t.Fatalf("findYashanLib() = %q, want %q", got, libPath)
	}
}

func TestJSONToSQLValueSerializesStructuredValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "object",
			value: map[string]any{"provider": "amazon", "regions": []any{"US", "CA"}},
			want:  `{"provider":"amazon","regions":["US","CA"]}`,
		},
		{
			name:  "array",
			value: []any{"amazon", "ebay"},
			want:  `["amazon","ebay"]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := jsonToSQLValue(test.value).(string)
			if !ok || got != test.want {
				t.Fatalf("jsonToSQLValue() = %#v, want %q", got, test.want)
			}
		})
	}
}
