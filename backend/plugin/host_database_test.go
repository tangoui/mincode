package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/backend/db"
)

type fakeDatabaseSchema struct {
	described [][]string
}

func useDatabaseSchemaFakes(t *testing.T) *fakeDatabaseSchema {
	t.Helper()
	useTemporaryPluginData(t)
	fake := &fakeDatabaseSchema{}
	previousConnections, previousTables, previousDescribe := databaseSchemaConnections, databaseSchemaTables, databaseSchemaDescribe
	databaseSchemaConnections = func() ([]db.SchemaConnection, error) {
		return []db.SchemaConnection{{ID: "c1", Name: "Orders", Kind: "mysql", Database: "shop"}, {ID: "c2", Name: "Local", Kind: "sqlite"}}, nil
	}
	databaseSchemaTables = func(_ context.Context, connectionID, database string) (*db.SchemaTableListing, error) {
		if connectionID != "c1" && connectionID != "c2" {
			return nil, db.ErrSchemaConnectionNotFound
		}
		return &db.SchemaTableListing{Databases: []string{"shop"}, Database: "shop", Tables: []db.SchemaTableSummary{{Name: "orders", Kind: "table"}}}, nil
	}
	databaseSchemaDescribe = func(_ context.Context, _, _ string, tables []string) ([]db.SchemaTableDescription, error) {
		fake.described = append(fake.described, tables)
		out := make([]db.SchemaTableDescription, 0, len(tables))
		for _, table := range tables {
			out = append(out, db.SchemaTableDescription{Name: table})
		}
		return out, nil
	}
	t.Cleanup(func() {
		databaseSchemaConnections, databaseSchemaTables, databaseSchemaDescribe = previousConnections, previousTables, previousDescribe
	})
	return fake
}

func TestHostDatabaseSchemaAsksPerConnectionAndRemembers(t *testing.T) {
	events := captureHostUI(t)
	fake := useDatabaseSchemaFakes(t)
	caller := uiRuntime("schema-studio")

	result, err := uiCall(hostDatabaseConnections, caller, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"granted":false`) || strings.Contains(string(raw), "password") {
		t.Fatalf("connections %s", raw)
	}

	// Declined: nothing is read.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != databaseSchemaPromptKind || event.Database == nil || event.Database.ConnectionID != "c1" || event.Database.Kind != "mysql" || len(event.Items) != 2 {
			t.Errorf("approval %+v %+v", event, event.Database)
		}
		return promptReply{Cancelled: true}
	})
	if _, err := uiCall(hostDatabaseTables, caller, `{"connectionId":"c1"}`); err == nil || err.Code != -32003 {
		t.Fatalf("declined: %v", err)
	}

	// Allowed (not always): lasts for this plugin run only.
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{IDs: []string{"once"}} })
	if _, err := uiCall(hostDatabaseTables, caller, `{"connectionId":"c1"}`); err != nil {
		t.Fatal(err)
	}
	if databaseSchemaGranted("schema-studio", "c1") {
		t.Fatal("once was remembered")
	}
	if _, err := uiCall(hostDatabaseTables, caller, `{"connectionId":"c1"}`); err != nil {
		t.Fatalf("same run asked again: %v", err)
	}
	if databaseSchemaRunGranted(uiRuntime("other"), "c1") {
		t.Fatal("run grant leaked to another plugin")
	}
	caller.generation++ // the plugin restarted

	// Always: remembered, later calls skip the prompt.
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{IDs: []string{"always"}} })
	if _, err := uiCall(hostDatabaseDescribe, caller, `{"connectionId":"c1","database":"shop","tables":["orders","orders","items"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := uiCall(hostDatabaseDescribe, caller, `{"connectionId":"c1","database":"shop","tables":["orders"]}`); err != nil {
		t.Fatal(err)
	}
	if len(fake.described) != 2 || strings.Join(fake.described[0], ",") != "orders,items" {
		t.Fatalf("described %v", fake.described)
	}
	result, _ = uiCall(hostDatabaseConnections, caller, `{}`)
	raw, _ = json.Marshal(result)
	if strings.Count(string(raw), `"granted":true`) != 1 {
		t.Fatalf("grant not reported %s", raw)
	}
	// Grants are per plugin.
	if databaseSchemaGranted("other", "c1") {
		t.Fatal("grant leaked to another plugin")
	}

	listed, _ := databaseSchemaGrantsGetHandler("", `{"id":"schema-studio"}`)
	if !strings.Contains(listed, `{"id":"c1","name":"Orders","kind":"mysql"}`) {
		t.Fatalf("grants %s", listed)
	}
	// Revoke from the desktop UI.
	out, revokeErr := databaseSchemaGrantsRevokeHandler("", `{"id":"schema-studio","connectionId":"c1"}`)
	if revokeErr != nil || !strings.Contains(out, `"connections":[]`) || databaseSchemaGranted("schema-studio", "c1") {
		t.Fatalf("revoke %s %v", out, revokeErr)
	}
	grantDatabaseSchemaRun(caller, "c1")
	_, _ = databaseSchemaGrantsRevokeHandler("", `{"id":"schema-studio"}`)
	if databaseSchemaRunGranted(caller, "c1") {
		t.Fatal("revoke kept the run grant")
	}
}

func TestHostDatabaseSchemaValidatesParams(t *testing.T) {
	captureHostUI(t)
	useDatabaseSchemaFakes(t)
	caller := uiRuntime("schema-studio")
	if err := grantDatabaseSchema("schema-studio", "c1"); err != nil {
		t.Fatal(err)
	}
	many := make([]string, db.SchemaDescribeMaxTables+1)
	for index := range many {
		many[index] = "t" + strings.Repeat("x", index%5)
	}
	tooMany, _ := json.Marshal(map[string]any{"connectionId": "c1", "database": "shop", "tables": many})
	for name, call := range map[string]struct {
		handler func(*processRuntime, json.RawMessage) (any, *rpcError)
		params  string
		code    int
	}{
		"missing connection": {hostDatabaseTables, `{}`, -32602},
		"extra field":        {hostDatabaseTables, `{"connectionId":"c1","sql":"drop table x"}`, -32602},
		"control chars":      {hostDatabaseTables, `{"connectionId":"c1","database":"a\u0000b"}`, -32602},
		"bad timeout":        {hostDatabaseTables, `{"connectionId":"c1","timeoutMs":1}`, -32602},
		"unknown connection": {hostDatabaseTables, `{"connectionId":"nope"}`, -32002},
		"no tables":          {hostDatabaseDescribe, `{"connectionId":"c1","database":"shop","tables":[]}`, -32602},
		"no database":        {hostDatabaseDescribe, `{"connectionId":"c1","tables":["orders"]}`, -32602},
		"too many tables":    {hostDatabaseDescribe, string(tooMany), -32602},
	} {
		if _, err := uiCall(call.handler, caller, call.params); err == nil || err.Code != call.code {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestDatabaseSchemaApprovalRepliesAreValidated(t *testing.T) {
	broker := &promptBroker{pending: map[string]*pendingPrompt{"p": {pluginID: "a", kind: databaseSchemaPromptKind, items: map[string]bool{"once": true, "always": true}, reply: make(chan promptReply, 1)}}, byPlugin: map[string]string{}}
	for _, bad := range []promptReply{{IDs: []string{"forever"}}, {IDs: []string{"once", "once"}}, {}} {
		if broker.answer("p", bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := broker.answer("p", promptReply{IDs: []string{"always"}}); err != nil {
		t.Fatal(err)
	}
}
