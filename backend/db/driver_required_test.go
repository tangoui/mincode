package db

import (
	"strings"
	"testing"
)

func TestDriverRequiredMessageDoesNotMentionPlugins(t *testing.T) {
	for _, driver := range []string{"DuckDB", "Oracle", "YashanDB", "ClickHouse"} {
		t.Run(driver, func(t *testing.T) {
			message := coreDriverUnavailable(driver).Error()
			if !strings.HasPrefix(message, "driver_required:") || !strings.Contains(message, driver) {
				t.Fatalf("missing driver_required code or driver name: %q", message)
			}
			if strings.Contains(message, "插件") || strings.Contains(strings.ToLower(message), "plugin") {
				t.Fatalf("error mentions plugins: %q", message)
			}
		})
	}
}
