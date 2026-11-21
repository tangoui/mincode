package plugin

import "testing"

func TestBuiltinCatalogExcludesHelloExample(t *testing.T) {
	for _, builtin := range builtinHostPlugins() {
		if builtin.Meta().ID == "hello" {
			t.Fatal("hello development example must not be registered in the desktop app")
		}
	}
}
