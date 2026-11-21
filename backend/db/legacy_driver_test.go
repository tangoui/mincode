package db

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDatabaseDriverRequiresEditingConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	for _, binding := range []string{
		`{"pluginId":"acme.db","driverId":"sqlite"}`,
		`{}`,
	} {
		var saved backupSavedConnection
		config := map[string]any{
			"dbType": "sqlite", "filePath": path, "driver": json.RawMessage(binding),
			"id": "legacy", "connectionId": "legacy",
		}
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if session, err := openBackupSession(context.Background(), &saved); err == nil || !strings.Contains(err.Error(), "内置驱动") {
			if session != nil {
				session.close()
			}
			t.Fatalf("backup must reject legacy binding %s and explain migration: %v", binding, err)
		}
		if result, err := dbConnectHandler("", `{"config":`+string(raw)+`}`); err == nil || !strings.Contains(err.Error(), "内置驱动") {
			t.Fatalf("connect must reject legacy binding %s and explain migration: %s, %v", binding, result, err)
		}
	}

	result, err := dbConnectHandler("", `{"config":{"dbType":"sqlite","filePath":":memory:","driver":null}}`)
	if err != nil {
		t.Fatalf("connect with cleared driver binding: %v", err)
	}
	t.Cleanup(func() {
		if _, err := dbDisconnectHandler("", `{"sessionId":`+result+`}`); err != nil {
			t.Error(err)
		}
	})
}
