package api

// Category is a stable, language-independent plugin catalog key.
type Category string

const (
	CategoryDatabase     Category = "database"
	CategorySpreadsheet  Category = "spreadsheet"
	CategoryWeb          Category = "web"
	CategoryAPI          Category = "api"
	CategoryNetwork      Category = "network"
	CategoryDevelopment  Category = "development"
	CategoryProductivity Category = "productivity"
	CategoryOther        Category = "other"
)

// Categories returns a fresh slice in catalog display order.
func Categories() []Category {
	return []Category{CategoryDatabase, CategorySpreadsheet, CategoryWeb, CategoryAPI, CategoryNetwork, CategoryDevelopment, CategoryProductivity, CategoryOther}
}

func IsCategory(category Category) bool {
	for _, known := range Categories() {
		if category == known {
			return true
		}
	}
	return false
}
