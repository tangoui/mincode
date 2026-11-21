//go:build lite

package db

import (
	"reflect"
	"strings"
	"testing"
)

func TestLiteSupportedDatabaseTypes(t *testing.T) {
	want := []string{"sqlite", "mysql", "postgres", "redis", "mongodb"}
	if !reflect.DeepEqual(supportedDatabaseTypes, want) {
		t.Fatalf("supportedDatabaseTypes = %v, want %v", supportedDatabaseTypes, want)
	}
}

func TestLiteRejectsFullDatabaseType(t *testing.T) {
	_, err := buildProfileSession(&DbConnectionConfig{DbType: "oracle"})
	if err == nil || !strings.Contains(err.Error(), "Full") {
		t.Fatalf("buildProfileSession(oracle) error = %v, want Full version hint", err)
	}
}
