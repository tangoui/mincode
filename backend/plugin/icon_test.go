package plugin

import "testing"

func TestResolvePluginViewIcon(t *testing.T) {
	for _, tt := range []struct{ name, page, plugin, want string }{
		{"page first", "document", "connection", "document"},
		{"plugin fallback", "", "connection", "connection"},
		{"default fallback", "", "", "box"},
		{"trim whitespace", "  ", " connection ", "connection"},
		{"unknown page", "unknown", "connection", "connection"},
		{"unknown icons", "unknown", "unknown", "box"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePluginViewIcon(tt.page, tt.plugin); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManifestPluginIcon(t *testing.T) {
	for _, icon := range []string{"", "connection", " document "} {
		root := writeTestManifest(t, func(m *Manifest) { m.Icon = icon })
		if _, err := readManifest(root); err != nil {
			t.Fatalf("valid icon %q: %v", icon, err)
		}
	}
	root := writeTestManifest(t, func(m *Manifest) { m.Icon = "unknown" })
	if _, err := readManifest(root); err == nil {
		t.Fatal("unsupported plugin icon should be rejected")
	}
}
