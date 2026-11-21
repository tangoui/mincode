package appstore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	backendstorage "mincode/app/backend/storage"
)

func TestStoreHandlersAndBatchContract(t *testing.T) {
	setTestStore(t)
	if _, err := storeSetHandler("", `{"key":"one","value":{"enabled":true}}`); err != nil {
		t.Fatal(err)
	}
	got, err := storeGetHandler("", `{"key":"one"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"enabled":true}` {
		t.Fatalf("store_get = %s", got)
	}
	if _, err := storeApplyBatchHandler("", `{"operations":[{"type":"set","key":"two","value":"secret"},{"type":"delete","key":"one"}],"sets":{"three":3},"deleteKeys":["missing"]}`); err != nil {
		t.Fatal(err)
	}
	allJSON, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(allJSON), &all); err != nil {
		t.Fatal(err)
	}
	want := map[string]json.RawMessage{"two": json.RawMessage(`"secret"`), "three": json.RawMessage(`3`)}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("store_load_all = %#v, want %#v", all, want)
	}
	if _, err := storeApplyBatchHandler("", `{"deletes":["two","three"]}`); err != nil {
		t.Fatal(err)
	}
	missing, err := storeGetHandler("", `{"key":"two"}`)
	if err != nil {
		t.Fatal(err)
	}
	if missing != "null" {
		t.Fatalf("missing store_get = %s, want null", missing)
	}
}

func TestApplyBatchDeletesAllNormalizedAIWorkspaces(t *testing.T) {
	db := setTestStore(t)
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE ai_workspaces (scope_id TEXT PRIMARY KEY, active_conversation_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE ai_conversations (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL, payload_hash BLOB NOT NULL, PRIMARY KEY(scope_id, conversation_id), FOREIGN KEY(scope_id) REFERENCES ai_workspaces(scope_id) ON DELETE CASCADE)`,
		`CREATE TABLE ai_attachments (hash TEXT PRIMARY KEY, kind TEXT NOT NULL, mime_type TEXT NOT NULL, encoding TEXT NOT NULL, data BLOB NOT NULL)`,
		`CREATE TABLE ai_attachment_refs (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, attachment_hash TEXT NOT NULL, PRIMARY KEY(scope_id, conversation_id, attachment_hash), FOREIGN KEY(scope_id, conversation_id) REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE, FOREIGN KEY(attachment_hash) REFERENCES ai_attachments(hash) ON DELETE CASCADE)`,
		`INSERT INTO ai_workspaces(scope_id) VALUES('workspace:one'), ('workspace:two')`,
		`INSERT INTO ai_conversations(scope_id, conversation_id, position, payload, payload_hash) VALUES('workspace:one', 'conversation-1', 0, '{}', 'hash-1'), ('workspace:two', 'conversation-2', 0, '{}', 'hash-2')`,
		`INSERT INTO ai_attachments(hash, kind, mime_type, encoding, data) VALUES('attachment-1', 'image', 'image/png', 'base64', 'abc'), ('attachment-2', 'image', 'image/png', 'base64', 'def')`,
		`INSERT INTO ai_attachment_refs(scope_id, conversation_id, attachment_hash) VALUES('workspace:one', 'conversation-1', 'attachment-1'), ('workspace:two', 'conversation-2', 'attachment-2')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"app_ai_conversations_v2", "app_ai_conversations_v2__workspace__one", "app_ai_session_v1__workspace__one"} {
		if _, err := db.Exec(`INSERT INTO app_kv(key,value) VALUES(?, '{}')`, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO app_kv(key,value) VALUES('app_settings', '{}'), ('appXaiXconversationsXv2__keep', '{}')`); err != nil {
		t.Fatal(err)
	}
	if err := applyBatch([]storeBatchOperation{{Type: "delete_all_ai", Key: "*"}}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"ai_workspaces", "ai_conversations", "ai_attachment_refs", "ai_attachments"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows", table, count)
		}
	}
	var retiredCount, settingsCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_kv WHERE key = 'app_ai_conversations_v2' OR substr(key, 1, length('app_ai_conversations_v2__')) = 'app_ai_conversations_v2__' OR key = 'app_ai_session_v1' OR substr(key, 1, length('app_ai_session_v1__')) = 'app_ai_session_v1__'`).Scan(&retiredCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_kv WHERE key IN ('app_settings', 'appXaiXconversationsXv2__keep')`).Scan(&settingsCount); err != nil {
		t.Fatal(err)
	}
	if retiredCount != 0 || settingsCount != 2 {
		t.Fatalf("retired/settings rows=%d/%d want 0/2", retiredCount, settingsCount)
	}

}

func TestApplyBatchValidatesAllInputBeforeOpeningDatabase(t *testing.T) {
	oversizedOperations := make([]storeBatchOperation, maxBatchOperations+1)
	largeValue := json.RawMessage(`"` + strings.Repeat("x", maxBatchValueBytes/2) + `"`)
	tests := []struct {
		name       string
		operations []storeBatchOperation
	}{
		{name: "too many operations", operations: oversizedOperations},
		{name: "empty key", operations: []storeBatchOperation{{Type: "delete"}}},
		{name: "duplicate key", operations: []storeBatchOperation{{Type: "set", Key: "same", Value: json.RawMessage("1")}, {Type: "set", Key: "same", Value: json.RawMessage("2")}}},
		{name: "set and delete same key", operations: []storeBatchOperation{{Type: "set", Key: "same", Value: json.RawMessage("1")}, {Type: "delete", Key: "same"}}},
		{name: "invalid json", operations: []storeBatchOperation{{Type: "set", Key: "bad", Value: json.RawMessage("{")}}},
		{name: "empty value", operations: []storeBatchOperation{{Type: "set", Key: "bad"}}},
		{name: "values too large", operations: []storeBatchOperation{{Type: "set", Key: "one", Value: largeValue}, {Type: "set", Key: "two", Value: largeValue}}},
	}
	originalOpenStoreDB := openStoreDB
	openStoreDB = func() (*sql.DB, error) {
		t.Fatal("database opened before batch validation completed")
		return nil, nil
	}
	t.Cleanup(func() { openStoreDB = originalOpenStoreDB })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := applyBatch(test.operations); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestApplyBatchAcceptsMaximumBoundaries(t *testing.T) {
	operations := make([]storeBatchOperation, maxBatchOperations)
	for index := range operations {
		operations[index] = storeBatchOperation{Type: "delete", Key: fmt.Sprintf("key-%d", index)}
	}
	if err := validateBatch(operations); err != nil {
		t.Fatalf("maximum operation count rejected: %v", err)
	}
	if err := validateBatch([]storeBatchOperation{{Type: "set", Key: "large", Value: json.RawMessage(`"` + strings.Repeat("x", maxBatchValueBytes-2) + `"`)}}); err != nil {
		t.Fatalf("maximum value bytes rejected: %v", err)
	}
}

func setTestStore(t *testing.T) *sql.DB {
	t.Helper()
	root := t.TempDir()
	originalOpenStoreDB := openStoreDB

	db, err := sql.Open("sqlite", root+"/test.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE app_kv (key TEXT PRIMARY KEY NOT NULL, value BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	originalOpenStoreReadDB := openStoreReadDB
	openStoreDB = func() (*sql.DB, error) { return db, nil }
	openStoreReadDB = openStoreDB
	t.Cleanup(func() {
		_ = db.Close()
		openStoreDB = originalOpenStoreDB
		openStoreReadDB = originalOpenStoreReadDB
		_ = backendstorage.Close()
	})
	return db
}

func TestApplyBatchDeletesWorkspaceStateAndNormalizedAIAtomically(t *testing.T) {
	db := setTestStore(t)
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE ai_workspaces (scope_id TEXT PRIMARY KEY, active_conversation_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE ai_conversations (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL, payload_hash BLOB NOT NULL, PRIMARY KEY(scope_id, conversation_id), FOREIGN KEY(scope_id) REFERENCES ai_workspaces(scope_id) ON DELETE CASCADE)`,
		`CREATE TABLE ai_attachments (hash TEXT PRIMARY KEY, kind TEXT NOT NULL, mime_type TEXT NOT NULL, encoding TEXT NOT NULL, data BLOB NOT NULL)`,
		`CREATE TABLE ai_attachment_refs (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, attachment_hash TEXT NOT NULL, PRIMARY KEY(scope_id, conversation_id, attachment_hash), FOREIGN KEY(scope_id, conversation_id) REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE, FOREIGN KEY(attachment_hash) REFERENCES ai_attachments(hash) ON DELETE CASCADE)`,
		`INSERT INTO app_kv(key, value) VALUES('workspace-state', '1')`,
		`INSERT INTO ai_workspaces(scope_id) VALUES('workspace:one')`,
		`INSERT INTO ai_conversations(scope_id, conversation_id, position, payload, payload_hash) VALUES('workspace:one', 'conversation-1', 0, '{}', 'hash')`,
		`INSERT INTO ai_attachments(hash, kind, mime_type, encoding, data) VALUES('attachment-1', 'image', 'image/png', 'base64', 'abc')`,
		`INSERT INTO ai_attachment_refs(scope_id, conversation_id, attachment_hash) VALUES('workspace:one', 'conversation-1', 'attachment-1')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyBatch([]storeBatchOperation{
		{Type: "delete", Key: "workspace-state"},
		{Type: "delete_ai_scope", Key: "workspace:one"},
	}); err != nil {
		t.Fatal(err)
	}
	for table, condition := range map[string]string{
		"app_kv":             "key = 'workspace-state'",
		"ai_workspaces":      "scope_id = 'workspace:one'",
		"ai_conversations":   "scope_id = 'workspace:one'",
		"ai_attachment_refs": "scope_id = 'workspace:one'",
		"ai_attachments":     "hash = 'attachment-1'",
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE " + condition).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows", table, count)
		}
	}
}

func TestStoreLoadAllSkipsBackendKeysAndMatchesEncodedMap(t *testing.T) {
	db := setTestStore(t)
	values := map[string]string{
		"app_settings":            `{"uiTheme":"dark","nested":[1,2]}`,
		"cloud-operation:req-1":   `{"status":"completed"}`,
		"cloud-terminal:sess":     `{"sessionId":"sess"}`,
		"desktop_window_state_v1": `{"width":1200}`,
		"cloud-terminals":         `true`,
		"unicode-键\"quoted\"":     `"值"`,
		"zeta":                    `null`,
	}
	for key, value := range values {
		if _, err := db.Exec("INSERT INTO app_kv(key, value) VALUES(?, ?)", key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]json.RawMessage{}
	for key, value := range values {
		if strings.HasPrefix(key, "cloud-operation:") || strings.HasPrefix(key, "cloud-terminal:") || key == "desktop_window_state_v1" {
			continue
		}
		kept[key] = json.RawMessage(value)
	}
	want, err := json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("store_load_all = %s\nwant %s", got, want)
	}
}

func TestStoreLoadAllEmptyStore(t *testing.T) {
	setTestStore(t)
	got, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "{}" {
		t.Fatalf("store_load_all on empty store = %s, want {}", got)
	}
}

func TestStoreGetManyReturnsRequestedKeysWithNullForMissing(t *testing.T) {
	setTestStore(t)
	if _, err := storeApplyBatchHandler("", `{"sets":{"one":{"enabled":true},"two":"text"}}`); err != nil {
		t.Fatal(err)
	}
	got, err := storeGetManyHandler("", `{"keys":["one","missing","two","one"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &values); err != nil {
		t.Fatalf("store_get_many returned invalid JSON %q: %v", got, err)
	}
	want := map[string]json.RawMessage{
		"one":     json.RawMessage(`{"enabled":true}`),
		"two":     json.RawMessage(`"text"`),
		"missing": json.RawMessage(`null`),
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("store_get_many = %s, want %#v", got, want)
	}

	empty, err := storeGetManyHandler("", `{"keys":[]}`)
	if err != nil || empty != "{}" {
		t.Fatalf("empty store_get_many = %q, %v", empty, err)
	}
}

func TestStoreGetManyValidatesKeys(t *testing.T) {
	setTestStore(t)
	if _, err := storeGetManyHandler("", `{"keys":[""]}`); err == nil {
		t.Fatal("empty key must be rejected")
	}
	keys := make([]string, maxGetManyKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	payload, _ := json.Marshal(map[string]any{"keys": keys})
	if _, err := storeGetManyHandler("", string(payload)); err == nil {
		t.Fatal("oversized key list must be rejected")
	}
}

func TestCustomTemplateKeyIsReserved(t *testing.T) {
	db := setTestStore(t)
	key := "app_file_templates_custom_v1"
	if _, err := db.Exec("INSERT INTO app_kv(key,value) VALUES(?,?)", key, `{"body":"BODY_SENTINEL"}`); err != nil {
		t.Fatal(err)
	}
	all, err := storeLoadAllHandler("", "")
	if err != nil || strings.Contains(all, "BODY_SENTINEL") {
		t.Fatalf("leaked snapshot %s %v", all, err)
	}
	for _, handler := range []func(string, string) (string, error){storeGetHandler, storeSetHandler, storeDelHandler} {
		if _, err := handler("", `{"key":"app_file_templates_custom_v1","value":{}}`); err == nil {
			t.Fatal("generic access accepted")
		}
	}
	if _, err := storeApplyBatchHandler("", `{"sets":{"safe":1,"app_file_templates_custom_v1":{}}}`); err == nil {
		t.Fatal("batch overwrite accepted")
	}
	raw, err := storeGetHandler("", `{"key":"safe"}`)
	if err != nil || raw != "null" {
		t.Fatal("partial batch", raw, err)
	}
}

func TestStoreRefusesHostOwnedAppLockKey(t *testing.T) {
	db := setTestStore(t)
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES('app_lock_v1', '{"enabled":true,"passcode":{"hash":"x"}}')`); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() (string, error){
		"store_get":      func() (string, error) { return storeGetHandler("", `{"key":"app_lock_v1"}`) },
		"store_get_many": func() (string, error) { return storeGetManyHandler("", `{"keys":["app_lock_v1"]}`) },
		"store_set":      func() (string, error) { return storeSetHandler("", `{"key":"app_lock_v1","value":{"enabled":false}}`) },
		"store_del":      func() (string, error) { return storeDelHandler("", `{"key":"app_lock_v1"}`) },
		"store_apply_batch": func() (string, error) {
			return storeApplyBatchHandler("", `{"sets":{"app_lock_v1":{"enabled":false}}}`)
		},
	}
	for name, run := range calls {
		if _, err := run(); err == nil || !strings.Contains(err.Error(), "app_lock_reserved_key") {
			t.Fatalf("%s must refuse app_lock_v1, got %v", name, err)
		}
	}
	all, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "app_lock_v1") {
		t.Fatalf("store_load_all must not expose the passcode verifier: %s", all)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM app_kv WHERE key = 'app_lock_v1'`).Scan(&value); err != nil || !strings.Contains(value, `"enabled":true`) {
		t.Fatalf("record must be untouched, got %q (%v)", value, err)
	}
}

func TestStoreKeyIsReserved(t *testing.T) {
	db := setTestStore(t)
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES('mcp_oauth_v1', '"enc:v2:SENTINEL"')`); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() (string, error){
		"store_get":      func() (string, error) { return storeGetHandler("", `{"key":"mcp_oauth_v1"}`) },
		"store_get_many": func() (string, error) { return storeGetManyHandler("", `{"keys":["mcp_oauth_v1"]}`) },
		"store_set":      func() (string, error) { return storeSetHandler("", `{"key":"mcp_oauth_v1","value":{}}`) },
		"store_del":      func() (string, error) { return storeDelHandler("", `{"key":"mcp_oauth_v1"}`) },
		"store_apply_batch": func() (string, error) {
			return storeApplyBatchHandler("", `{"sets":{"mcp_oauth_v1":{}}}`)
		},
	}
	for name, run := range calls {
		if _, err := run(); err == nil || !strings.Contains(err.Error(), "mcp_oauth_reserved_key") {
			t.Fatalf("%s must refuse mcp_oauth_v1, got %v", name, err)
		}
	}
	all, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "SENTINEL") || strings.Contains(all, "mcp_oauth_v1") {
		t.Fatalf("store_load_all must not expose the token store: %s", all)
	}
}

func TestStoreRefusesHostOwnedBackupPasswordKey(t *testing.T) {
	db := setTestStore(t)
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES('backup_password_v1', '"enc:v2:SENTINEL"')`); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() (string, error){
		"store_get":      func() (string, error) { return storeGetHandler("", `{"key":"backup_password_v1"}`) },
		"store_get_many": func() (string, error) { return storeGetManyHandler("", `{"keys":["backup_password_v1"]}`) },
		"store_set":      func() (string, error) { return storeSetHandler("", `{"key":"backup_password_v1","value":"x"}`) },
		"store_del":      func() (string, error) { return storeDelHandler("", `{"key":"backup_password_v1"}`) },
		"store_apply_batch": func() (string, error) {
			return storeApplyBatchHandler("", `{"sets":{"backup_password_v1":"x"}}`)
		},
		"store_apply_batch_delete": func() (string, error) {
			return storeApplyBatchHandler("", `{"deleteKeys":["backup_password_v1"]}`)
		},
	}
	for name, run := range calls {
		if _, err := run(); err == nil || !strings.Contains(err.Error(), "backup_password_reserved_key") {
			t.Fatalf("%s must refuse backup_password_v1, got %v", name, err)
		}
	}
	all, err := storeLoadAllHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "SENTINEL") || strings.Contains(all, "backup_password_v1") {
		t.Fatalf("store_load_all must not expose the remembered backup password: %s", all)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM app_kv WHERE key = 'backup_password_v1'`).Scan(&value); err != nil || value != `"enc:v2:SENTINEL"` {
		t.Fatalf("record must be untouched, got %q (%v)", value, err)
	}
}
