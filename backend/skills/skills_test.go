package skills

import "testing"

func TestParseFrontmatter(t *testing.T) {
	name, description := parseFrontmatter("---\nname: ego-browser\ndescription: Browser automation\n---\n")
	if name != "ego-browser" || description != "Browser automation" {
		t.Fatalf("unexpected metadata: %q %q", name, description)
	}
}

func TestParseFrontmatterMissingDescription(t *testing.T) {
	_, description := parseFrontmatter("---\nname: incomplete\n---\n")
	if description != "" {
		t.Fatalf("expected empty description, got %q", description)
	}
}
