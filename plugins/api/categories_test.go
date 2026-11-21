package api

import (
	"reflect"
	"testing"
)

func TestCategoriesContract(t *testing.T) {
	want := []Category{"database", "spreadsheet", "web", "api", "network", "development", "productivity", "other"}
	if !reflect.DeepEqual(Categories(), want) {
		t.Fatal("category contract changed")
	}
	for _, category := range want {
		if !IsCategory(category) {
			t.Fatal(category)
		}
	}
	for _, category := range []Category{"", "Database", "unknown"} {
		if IsCategory(category) {
			t.Fatal(category)
		}
	}
	categories := Categories()
	categories[0] = "changed"
	if !reflect.DeepEqual(Categories(), want) {
		t.Fatal("caller mutated category vocabulary")
	}
}
