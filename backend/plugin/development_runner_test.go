package plugin

import (
	"strings"
	"testing"
)

func TestPluginDevRunnerOutputIsBoundedAndDoesNotExposeSecrets(t *testing.T) {
	output := &pluginDevRunnerOutput{}
	input := strings.Repeat("x", 20000) + "MinCode development bridge is unavailable or incompatible"
	if n, err := output.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatal("output writer must consume all bytes")
	}
	if len(output.tail) > 8192 {
		t.Fatal("output exceeded bound")
	}
	if output.reason() == "" {
		t.Fatal("missing actionable bridge failure")
	}
	output.Write([]byte(strings.Repeat("private-password", 1000)))
	if strings.Contains(output.reason(), "private-password") {
		t.Fatal("raw output must not be exposed")
	}
}
