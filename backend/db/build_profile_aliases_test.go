//go:build !lite

package db

import (
	"strings"
	"testing"
)

// Wire-compatible aliases reuse the MySQL / PostgreSQL drivers: they must be
// listed and routed to a session builder.
func TestBuildProfileRoutesCompatibleAliases(t *testing.T) {
	available := make(map[string]struct{}, len(supportedDatabaseTypes))
	for _, dbType := range supportedDatabaseTypes {
		available[dbType] = struct{}{}
	}
	for _, dbType := range []string{"dolt", "greatsql", "opentenbase"} {
		if _, ok := available[dbType]; !ok {
			t.Errorf("supportedDatabaseTypes is missing %q", dbType)
		}
		// Port 1 on loopback refuses the connection: the error must come from
		// the driver, not from the unsupported-type fallback.
		_, err := buildProfileSession(&DbConnectionConfig{DbType: dbType, Host: "127.0.0.1", Port: 1})
		if err == nil {
			t.Errorf("%s: expected a connection error", dbType)
		} else if strings.Contains(err.Error(), "暂不支持") {
			t.Errorf("%s is not routed to a driver: %v", dbType, err)
		}
	}
}
