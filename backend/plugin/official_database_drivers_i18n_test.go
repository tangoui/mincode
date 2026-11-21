package plugin

import (
	"path/filepath"
	"testing"
)

// The official database driver plugins localize every text the connection
// dialog shows: the driver description, each option hint and the label of
// each enum value (descriptionLocalized / enumLabelsLocalized).
func TestOfficialDatabaseDriversLocalized(t *testing.T) {
	for _, id := range []string{"db2-driver", "phoenix-driver", "xugudb-driver"} {
		t.Run(id, func(t *testing.T) {
			manifest := readContributionManifestForTest(t, filepath.Join("..", "..", "official-plugins", id))
			phaseGRequireNoIssues(t, id, manifest)
			drivers := phaseGEntries[DatabaseDriverContribution](manifest, databaseDriverPointKey)
			if len(drivers) != 1 {
				t.Fatalf("drivers = %+v", drivers)
			}
			driver := drivers[0]
			if driver.TitleLocalized["zh"] == "" || driver.DescriptionLocalized["zh"] == "" {
				t.Fatalf("driver texts not localized: %+v", driver)
			}
			if len(driver.Connection.Options) == 0 {
				t.Fatal("no options")
			}
			for key, option := range driver.Connection.Options {
				if option.TitleLocalized["zh"] == "" || option.DescriptionLocalized["zh"] == "" {
					t.Errorf("option %s not localized: %+v", key, option)
				}
				if option.Enum != nil && len(option.EnumLabelsLocalized["zh"]) != len(option.Enum) {
					t.Errorf("option %s enum labels = %v for %v", key, option.EnumLabelsLocalized, option.Enum)
				}
			}
		})
	}
}
