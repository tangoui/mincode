package externalopen

import "testing"

func TestCatalogClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Catalog() {
		if seen[v.ID] || v.ID == "" || v.Value == "" {
			t.Fatal(v)
		}
		seen[v.ID] = true
		if v.Kind != "file" && v.Kind != "scheme" {
			t.Fatal(v)
		}
	}
	for _, id := range []string{"http", "https", "file", "mincode", "exe", "sh", "*"} {
		if _, ok := Lookup(id); ok {
			t.Fatal("reserved type", id)
		}
	}
}
func TestExternalURI(t *testing.T) {
	for _, raw := range []string{"magnet:?xt=urn:btih:abc", "mailto:test@example.com", "ssh://example.com", "ed2k://|file|x|1|abc|/"} {
		if _, err := URI(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"https://example.com", "file:///tmp/a", "mincode://test/x", "magnet:", "magnet:?x=1\n", "ssh://a b", "ed2k://|file|x%0A|1|abc|/", "magnet:?x=%0A", "ssh://x/%E2%80%AE"} {
		if _, err := URI(raw); err == nil {
			t.Fatal(raw)
		}
	}
	if tpe, ok := FileType("/tmp/SEED.TORRENT"); !ok || tpe.ID != "torrent" {
		t.Fatal(tpe)
	}
}

func TestCatalogIndependent(t *testing.T) {
	a := Catalog()
	a[0].ID = "corrupt"
	if _, ok := Lookup("magnet"); !ok {
		t.Fatal("mutated catalog")
	}
}
