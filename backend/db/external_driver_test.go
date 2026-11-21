package db

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDriverBoundConfigNeverBuildsBuiltinSession(t *testing.T) {
	var cfg DbConnectionConfig
	if err := json.Unmarshal([]byte(`{"dbType":"plugin","host":"127.0.0.1","port":1,"ssl":false,"driver":{"pluginId":"p","driverId":"d"},"connectionId":"c1","options":{"compression":"lz4"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Driver == nil || cfg.Driver.PluginID != "p" || cfg.Driver.DriverID != "d" || cfg.ConnectionID != "c1" || cfg.Options["compression"] != "lz4" {
		t.Fatalf("decoded config = %+v", cfg)
	}
	// Even with a built-in dbType the driver reference wins and fails closed.
	cfg.DbType = "sqlite"
	if _, err := buildSession(&cfg); !errors.Is(err, errExternalDriverUnavailable) {
		t.Fatalf("buildSession error = %v", err)
	}
	if _, err := ConnectCloudReadOnly(CloudReadOnlyConnection{Config: cfg}); !errors.Is(err, errExternalDriverUnavailable) {
		t.Fatalf("cloud read-only error = %v", err)
	}
	if registeredExternalDriverProvider() != nil {
		t.Fatal("no provider is registered before T2")
	}
	if closed := CloseExternalSessions("p", "test"); len(closed) != 0 {
		t.Fatalf("closed = %v", closed)
	}
	encoded, _ := json.Marshal(DbConnectionConfig{DbType: "mysql"})
	var members map[string]any
	_ = json.Unmarshal(encoded, &members)
	for _, key := range []string{"driver", "connectionId", "options"} {
		if _, ok := members[key]; ok {
			t.Fatalf("built-in config serializes %s", key)
		}
	}
}
