package plugin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pkg/sftp"

	"mincode/app/backend/backup"
	backendsftp "mincode/app/backend/sftp"
	"mincode/app/plugins/api"
)

const sshFilesTestPlugin = "data-backup"

type fakeSSHFiles struct {
	home        string // the remote user's home (SFTP working directory)
	opened      int
	connections []backendsftp.SavedConnection
}

// useSSHFilesFakes isolates plugin data and staging and replaces the saved
// SSH connections with fakes; SFTP runs in-process against a temp directory.
func useSSHFilesFakes(t *testing.T) (*fakeSSHFiles, *capturedUIEvents) {
	t.Helper()
	events := useBackupHost(t)
	fake := &fakeSSHFiles{home: t.TempDir(), connections: []backendsftp.SavedConnection{
		{ID: "srv", Name: "Backup server", Host: "backup.example", Port: 2222, Username: "alice", Eligible: true},
		{ID: "otp", Name: "MFA box", Host: "mfa.example", Port: 22, Username: "bob", Reason: backendsftp.ReasonInteractive},
	}}
	previousList, previousGet, previousOpen := sshFilesConnections, sshFilesConnection, sshFilesOpen
	sshFilesConnections = func() ([]backendsftp.SavedConnection, error) { return fake.connections, nil }
	sshFilesConnection = func(id string) (backendsftp.SavedConnection, error) {
		for _, connection := range fake.connections {
			if connection.ID == id {
				return connection, nil
			}
		}
		return backendsftp.SavedConnection{}, &backendsftp.SavedConnectionError{Code: backendsftp.SavedNotFound}
	}
	sshFilesOpen = func(ctx context.Context, id string) (*sftp.Client, func(), error) {
		connection, err := sshFilesConnection(id)
		if err != nil {
			return nil, nil, err
		}
		if !connection.Eligible {
			return nil, nil, &backendsftp.SavedConnectionError{Code: backendsftp.SavedNotEligible, Reason: connection.Reason}
		}
		fake.opened++
		return pipeSFTPClient(t, fake.home)
	}
	t.Cleanup(func() {
		sshFilesConnections, sshFilesConnection, sshFilesOpen = previousList, previousGet, previousOpen
		sshFilesGrantsMu.Lock()
		sshFilesRun = map[string]map[string]sshFilesRunGrant{}
		sshFilesGrantsMu.Unlock()
	})
	return fake, events
}

func pipeSFTPClient(t *testing.T, workDir string) (*sftp.Client, func(), error) {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{serverRead, serverWrite}, sftp.WithServerWorkingDirectory(workDir))
	if err != nil {
		return nil, nil, err
	}
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientRead, clientWrite)
	if err != nil {
		return nil, nil, err
	}
	return client, func() {
		// Close the pipes first: client.Close waits for its reader to stop.
		_ = clientRead.Close()
		_ = serverRead.Close()
		_ = client.Close()
		_ = server.Close()
	}, nil
}

func sshFilesRuntime() *processRuntime {
	manager, _, _ := pluginDataTestManager()
	return backupRuntime(sshFilesTestPlugin, manager)
}

func sshFilesErrorCode(err *rpcError) string {
	if err == nil {
		return ""
	}
	if data, ok := err.Data.(map[string]string); ok {
		return data["code"]
	}
	return ""
}

func authorizeSSHFiles(t *testing.T, events *capturedUIEvents, caller *processRuntime, directory, choice string) *rpcError {
	t.Helper()
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != sshFilesPromptKind || event.SSHFiles == nil || len(event.Items) != 2 {
			t.Errorf("prompt %+v", event)
			return promptReply{Cancelled: true}
		}
		if choice == "" {
			return promptReply{Cancelled: true}
		}
		return promptReply{IDs: []string{choice}}
	})
	raw, _ := json.Marshal(map[string]string{"connectionId": "srv", "directory": directory})
	_, err := backupCall(t, caller, "host.ssh.files.authorize", string(raw))
	return err
}

func TestSSHFilesMethodsNeedTheBackupCapability(t *testing.T) {
	useSSHFilesFakes(t)
	without := uiRuntime(sshFilesTestPlugin)
	without.capabilities = map[api.Capability]struct{}{api.CapabilityStorage: {}}
	if _, err := backupCall(t, without, "host.ssh.connections", `{}`); err == nil || err.Code != -32001 {
		t.Fatalf("without capability: %v", err)
	}
	for _, registration := range registeredHostMethods() {
		if strings.HasPrefix(registration.name, "host.ssh.") && registration.capability != api.CapabilityBackup {
			t.Errorf("%s capability %q", registration.name, registration.capability)
		}
	}
}

func TestSSHFilesConnectionsShowNamesOnly(t *testing.T) {
	useSSHFilesFakes(t)
	result, err := backupCall(t, sshFilesRuntime(), "host.ssh.connections", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"backup.example", "2222", "alice", "mfa.example", "bob"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("connections leak %q: %s", secret, raw)
		}
	}
	var got []map[string]any
	_ = json.Unmarshal(raw, &got)
	if len(got) != 2 || got[0]["id"] != "srv" || got[0]["name"] != "Backup server" || got[0]["eligible"] != true || got[0]["reason"] != nil ||
		got[1]["eligible"] != false || got[1]["reason"] != backendsftp.ReasonInteractive {
		t.Fatalf("connections %s", raw)
	}
}

func TestSSHFilesRequireAuthorization(t *testing.T) {
	useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	for method, params := range map[string]string{
		"host.ssh.files.list":    `{"connectionId":"srv"}`,
		"host.ssh.files.get":     `{"connectionId":"srv","name":"mincode-backup-a.bin"}`,
		"host.ssh.files.delete":  `{"connectionId":"srv","name":"mincode-backup-a.bin"}`,
		"host.ssh.files.getText": `{"connectionId":"srv","name":"mincode-backup-a.json"}`,
		"host.ssh.files.putText": `{"connectionId":"srv","name":"mincode-backup-a.json","text":"{}"}`,
		"host.ssh.files.put":     `{"connectionId":"srv","name":"mincode-backup-a.bin","token":"t"}`,
	} {
		if _, err := backupCall(t, caller, method, params); sshFilesErrorCode(err) != "notAuthorized" {
			t.Errorf("%s: %v", method, err)
		}
	}
}

func TestSSHFilesAuthorizeValidatesAndAsks(t *testing.T) {
	fake, events := useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	for _, directory := range []string{"", "backups", "/a/../b", "~/../x", "/a\x00b", "~bob/x", "/a\nb", "/", "//", "/.", "~", "~/", "~//", "~/."} {
		raw, _ := json.Marshal(map[string]string{"connectionId": "srv", "directory": directory})
		if _, err := backupCall(t, caller, "host.ssh.files.authorize", string(raw)); err == nil || err.Code != -32602 {
			t.Errorf("directory %q: %v", directory, err)
		}
	}
	if _, err := backupCall(t, caller, "host.ssh.files.authorize", `{"connectionId":"otp","directory":"~/b"}`); sshFilesErrorCode(err) != "notEligible" {
		t.Fatalf("ineligible: %v", err)
	}
	if _, err := backupCall(t, caller, "host.ssh.files.authorize", `{"connectionId":"nope","directory":"~/b"}`); sshFilesErrorCode(err) != "notFound" {
		t.Fatalf("missing: %v", err)
	}

	// The prompt shows the connection, the host and the directory.
	go answerNext(t, events, func(event promptEvent) promptReply {
		details := event.SSHFiles
		if event.Kind != sshFilesPromptKind || details == nil || details.ConnectionName != "Backup server" || details.Host != "backup.example" ||
			details.Port != 2222 || details.Username != "alice" || details.Directory != "~/backups/mincode" {
			t.Errorf("prompt %+v %+v", event, details)
		}
		return promptReply{Cancelled: true}
	})
	if _, err := backupCall(t, caller, "host.ssh.files.authorize", `{"connectionId":"srv","directory":"~/backups//mincode/"}`); sshFilesErrorCode(err) != "declined" {
		t.Fatalf("declined: %v", err)
	}
	if fake.opened != 0 {
		t.Fatal("connected before the user allowed it")
	}

	// once: lasts for this run, creates the directory, not persisted.
	if err := authorizeSSHFiles(t, events, caller, "~/backups/mincode", "once"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(fake.home, "backups", "mincode")); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	if grants := readSSHFilesGrants(sshFilesTestPlugin); len(grants) != 0 {
		t.Fatalf("once persisted %v", grants)
	}
	if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); err != nil {
		t.Fatalf("list after once: %v", err)
	}
	// Same directory again: no prompt.
	if _, err := backupCall(t, caller, "host.ssh.files.authorize", `{"connectionId":"srv","directory":"~/backups/mincode"}`); err != nil {
		t.Fatalf("re-authorize same directory: %v", err)
	}

	// always: persisted per plugin.
	absolute := filepath.ToSlash(filepath.Join(fake.home, "abs"))
	if err := authorizeSSHFiles(t, events, caller, absolute, "always"); err != nil {
		t.Fatal(err)
	}
	if grants := readSSHFilesGrants(sshFilesTestPlugin); grants["srv"].Directory != absolute || grants["srv"].Host != "backup.example" || grants["srv"].Port != 2222 || grants["srv"].Username != "alice" {
		t.Fatalf("grants %v", grants)
	}
	if raw, found, _ := pluginKVGetRaw(hostPluginID, "sshfiles/"+sshFilesTestPlugin); !found || !strings.Contains(string(raw), `"srv"`) {
		t.Fatalf("kv %s", raw)
	}
	// The grant is per plugin.
	other := backupRuntime("other-plugin", nil)
	if _, err := backupCall(t, other, "host.ssh.files.list", `{"connectionId":"srv"}`); sshFilesErrorCode(err) != "notAuthorized" {
		t.Fatalf("other plugin: %v", err)
	}
}

func stageForTest(t *testing.T, owner string, data []byte) string {
	t.Helper()
	token, _, _, err := backupStaging().Create(owner, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestSSHFilesPutListGetDeleteRoundTrip(t *testing.T) {
	fake, events := useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	if err := authorizeSSHFiles(t, events, caller, "~/b", "always"); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(fake.home, "b")
	// Noise the listing must skip.
	_ = os.WriteFile(filepath.Join(remote, ".hidden"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(remote, "mincode-backup-bad name"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(remote, "notes.txt"), []byte("x"), 0o600) // not a backup file
	_ = os.Mkdir(filepath.Join(remote, "sub"), 0o700)

	body := []byte(strings.Repeat("backup-bytes ", 4096))
	token := stageForTest(t, sshFilesTestPlugin, body)
	if _, err := backupCall(t, caller, "host.ssh.files.put", `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak","token":"`+token+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "mincode-backup-2026-10-29.mcbak.part")); !os.IsNotExist(err) {
		t.Fatalf("part file left: %v", err)
	}
	// Overwrite works too.
	if _, err := backupCall(t, caller, "host.ssh.files.put", `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak","token":"`+token+`"}`); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err := backupCall(t, caller, "host.ssh.files.putText", `{"connectionId":"srv","name":"mincode-backup-index.json","text":"{\"n\":1}"}`); err != nil {
		t.Fatal(err)
	}

	result, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var files []map[string]any
	_ = json.Unmarshal(raw, &files)
	names := []string{}
	for _, file := range files {
		names = append(names, file["name"].(string))
		if file["name"] == "mincode-backup-2026-10-29.mcbak" && file["size"] != float64(len(body)) {
			t.Errorf("size %v", file["size"])
		}
		if _, ok := file["modTime"].(float64); !ok {
			t.Errorf("modTime %v", file["modTime"])
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"mincode-backup-2026-10-29.mcbak", "mincode-backup-index.json"}) {
		t.Fatalf("list %s", raw)
	}
	if result, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv","name":"mincode-backup-index.json"}`); err != nil {
		t.Fatal(err)
	} else if raw, _ := json.Marshal(result); !strings.Contains(string(raw), "mincode-backup-index.json") || strings.Contains(string(raw), "mcbak") {
		t.Fatalf("filtered list %s", raw)
	}

	got, err := backupCall(t, caller, "host.ssh.files.get", `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak"}`)
	if err != nil {
		t.Fatal(err)
	}
	downloaded := backupResult(t, got)
	if downloaded["size"] != float64(len(body)) {
		t.Fatalf("get %v", downloaded)
	}
	if staged := readStaged(t, caller, downloaded["token"].(string)); string(staged) != string(body) {
		t.Fatal("downloaded bytes differ")
	}
	text, err := backupCall(t, caller, "host.ssh.files.getText", `{"connectionId":"srv","name":"mincode-backup-index.json"}`)
	if err != nil || backupResult(t, text)["text"] != `{"n":1}` {
		t.Fatalf("getText %v %v", text, err)
	}

	if _, err := backupCall(t, caller, "host.ssh.files.delete", `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "mincode-backup-2026-10-29.mcbak")); !os.IsNotExist(err) {
		t.Fatalf("not deleted: %v", err)
	}
	for method, params := range map[string]string{
		"host.ssh.files.delete":  `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak"}`,
		"host.ssh.files.get":     `{"connectionId":"srv","name":"mincode-backup-2026-10-29.mcbak"}`,
		"host.ssh.files.getText": `{"connectionId":"srv","name":"mincode-backup-missing.json"}`,
	} {
		if _, err := backupCall(t, caller, method, params); sshFilesErrorCode(err) != "notFound" {
			t.Errorf("%s missing: %v", method, err)
		}
	}
	// A token of another plugin is not found.
	foreign := stageForTest(t, "someone-else", []byte("x"))
	if _, err := backupCall(t, caller, "host.ssh.files.put", `{"connectionId":"srv","name":"mincode-backup-x.bin","token":"`+foreign+`"}`); sshFilesErrorCode(err) != "notFound" {
		t.Fatalf("foreign token: %v", err)
	}
}

func TestSSHFilesRejectBadNamesAndLargeText(t *testing.T) {
	_, events := useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	if err := authorizeSSHFiles(t, events, caller, "~/b", "once"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a/b", "..", "../x", ".hidden", "", strings.Repeat("a", 129), "x.part", "a b", "ü.bin", `a\b`, "index.json", "mincode-backup-", "mincode-backup-x.part", "Mincode-backup-x"} {
		raw, _ := json.Marshal(map[string]string{"connectionId": "srv", "name": name})
		for _, method := range []string{"host.ssh.files.get", "host.ssh.files.delete", "host.ssh.files.getText"} {
			if _, err := backupCall(t, caller, method, string(raw)); sshFilesErrorCode(err) != "invalidName" {
				t.Errorf("%s %q: %v", method, name, err)
			}
		}
	}
	tooBig, _ := json.Marshal(map[string]string{"connectionId": "srv", "name": "mincode-backup-big.json", "text": strings.Repeat("x", sshFilesTextMaxBytes+1)})
	if _, err := backupCall(t, caller, "host.ssh.files.putText", string(tooBig)); sshFilesErrorCode(err) != "tooLarge" {
		t.Fatalf("putText too large: %v", err)
	}
}

func TestSSHFilesGetRefusesOversizedFiles(t *testing.T) {
	fake, events := useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	if err := authorizeSSHFiles(t, events, caller, "~/b", "once"); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(fake.home, "b", "mincode-backup-huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(backup.MaxBackupBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := backupCall(t, caller, "host.ssh.files.get", `{"connectionId":"srv","name":"mincode-backup-huge.bin"}`); sshFilesErrorCode(err) != "tooLarge" {
		t.Fatalf("huge get: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fake.home, "b", "mincode-backup-big.json"), []byte(strings.Repeat("x", sshFilesTextMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backupCall(t, caller, "host.ssh.files.getText", `{"connectionId":"srv","name":"mincode-backup-big.json"}`); sshFilesErrorCode(err) != "tooLarge" {
		t.Fatalf("big getText: %v", err)
	}
}

func TestSSHFilesListMissingDirectoryIsEmpty(t *testing.T) {
	fake, events := useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	if err := authorizeSSHFiles(t, events, caller, "~/gone", "once"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fake.home, "gone")); err != nil {
		t.Fatal(err)
	}
	result, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(result); string(raw) != "[]" {
		t.Fatalf("list %s", raw)
	}
}

func TestSSHFilesBlockedDuringRestoreAndRevokedOnRemoval(t *testing.T) {
	_, events := useSSHFilesFakes(t)
	manager, _, _ := pluginDataTestManager()
	caller := backupRuntime(sshFilesTestPlugin, manager)
	if err := authorizeSSHFiles(t, events, caller, "~/b", "always"); err != nil {
		t.Fatal(err)
	}
	backupRestoringMu.Lock()
	backupRestoring[sshFilesTestPlugin] = true
	backupRestoringMu.Unlock()
	if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); sshFilesErrorCode(err) != "restoreInProgress" {
		t.Errorf("during restore: %v", err)
	}
	backupRestoringMu.Lock()
	delete(backupRestoring, sshFilesTestPlugin)
	backupRestoringMu.Unlock()

	for index, reason := range []removalReason{removalReasonUninstall, removalReasonClearData} {
		if index > 0 {
			if err := authorizeSSHFiles(t, events, caller, "~/b", "always"); err != nil {
				t.Fatal(err)
			}
		}
		manager.runRecordRemovalHooks(sshFilesTestPlugin, reason)
		if grants := readSSHFilesGrants(sshFilesTestPlugin); len(grants) != 0 {
			t.Fatalf("%v kept grants %v", reason, grants)
		}
		if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); sshFilesErrorCode(err) != "notAuthorized" {
			t.Fatalf("%v: list after removal: %v", reason, err)
		}
	}
}

func TestSSHFilesGrantBoundToHostPortAndUser(t *testing.T) {
	for _, choice := range []string{"once", "always"} {
		t.Run(choice, func(t *testing.T) {
			fake, events := useSSHFilesFakes(t)
			caller := sshFilesRuntime()
			if err := authorizeSSHFiles(t, events, caller, "~/b", choice); err != nil {
				t.Fatal(err)
			}
			if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); err != nil {
				t.Fatalf("list: %v", err)
			}
			for _, edit := range []func(*backendsftp.SavedConnection){
				func(c *backendsftp.SavedConnection) { c.Host = "evil.example" },
				func(c *backendsftp.SavedConnection) { c.Port = 22 },
				func(c *backendsftp.SavedConnection) { c.Username = "root" },
			} {
				saved := fake.connections[0]
				edit(&fake.connections[0])
				if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); sshFilesErrorCode(err) != "notAuthorized" {
					t.Errorf("edited connection still authorized: %v", err)
				}
				result, _ := backupCall(t, caller, "host.ssh.connections", `{}`)
				if raw, _ := json.Marshal(result); strings.Contains(string(raw), `"directory"`) {
					t.Errorf("edited connection shows a directory: %s", raw)
				}
				fake.connections[0] = saved
			}
			// The edited connection prompts again.
			fake.connections[0].Host = "new.example"
			if err := authorizeSSHFiles(t, events, caller, "~/b", choice); err != nil {
				t.Fatal(err)
			}
			if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); err != nil {
				t.Fatalf("list after re-authorizing: %v", err)
			}
		})
	}
}

func TestSSHFilesOldGrantsAreNotAuthorized(t *testing.T) {
	useSSHFilesFakes(t)
	caller := sshFilesRuntime()
	if err := pluginKVSetRaw(hostPluginID, sshFilesGrantsKVKey(sshFilesTestPlugin), json.RawMessage(`{"srv":"~/b"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := backupCall(t, caller, "host.ssh.files.list", `{"connectionId":"srv"}`); sshFilesErrorCode(err) != "notAuthorized" {
		t.Fatalf("old grant: %v", err)
	}
}
