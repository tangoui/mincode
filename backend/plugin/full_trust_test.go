package plugin

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

const fullTrustDemoID = "trust.demo"

// fullTrustDemoManifest is the in-memory manifest of the full trust fixture.
// Like testManagerWithPlugin it bypasses readManifest, so capabilities stay
// exactly as written (validateCapabilities would add ui and commands).
func fullTrustDemoManifest() Manifest {
	return Manifest{
		APIVersion: 2, ID: fullTrustDemoID, Name: "Trust Demo", Version: "1.0.0", Category: "development",
		Capabilities: []api.Capability{api.CapabilityStorage, api.CapabilityPlugins, api.CapabilityStorage},
		Runtime:      ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"},
		Contributes: ManifestContributions{
			Actions: []ManifestUIAction{
				{ID: "run", Risk: "write", UICallable: true, Permissions: []api.Capability{}},
				{ID: "wipe", Risk: "dangerous", UICallable: true, Permissions: []api.Capability{}},
			},
			Commands: []ManifestCommand{{ID: "run", Title: "Run", Action: "run"}},
		},
	}
}

type fullTrustFixture struct {
	manager *pluginManager
	root    string
}

func writeFullTrustManifest(t *testing.T, root string, manifest Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fullTrustManager installs trust.demo (enabled, not started) in a temporary
// plugin data directory and unlocks the app.
func fullTrustManager(t *testing.T) *fullTrustFixture {
	t.Helper()
	useTemporaryPluginData(t)
	unlockAppForTest(t)
	root := filepath.Join(t.TempDir(), fullTrustDemoID)
	for name, content := range map[string]string{"bin/plugin": "#!/bin/sh\necho demo\n", "ui/index.html": "<!doctype html><title>demo</title>\n"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := fullTrustDemoManifest()
	writeFullTrustManifest(t, root, manifest)
	fixture := &fullTrustFixture{manager: &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}, root: root}
	if err := fixture.manager.registerLocal(root, manifest); err != nil {
		t.Fatal(err)
	}
	fixture.setEnabled(t, true)
	return fixture
}

func (fixture *fullTrustFixture) record(t *testing.T) *pluginRecord {
	t.Helper()
	record, err := fixture.manager.record(fullTrustDemoID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func (fixture *fullTrustFixture) setEnabled(t *testing.T, enabled bool) {
	t.Helper()
	record := fixture.record(t)
	record.mu.Lock()
	record.enabled = enabled
	record.mu.Unlock()
}

// updateManifest swaps the in-memory manifest for a mutated copy and rewrites
// plugin.json to match, as a reinstall or reload would.
func (fixture *fullTrustFixture) updateManifest(t *testing.T, mutate func(*Manifest)) {
	t.Helper()
	record := fixture.record(t)
	record.mu.Lock()
	manifest := *record.manifest
	record.mu.Unlock()
	manifest.Capabilities = slices.Clone(manifest.Capabilities)
	manifest.Contributes.Actions = slices.Clone(manifest.Contributes.Actions)
	manifest.Contributes.Commands = slices.Clone(manifest.Contributes.Commands)
	mutate(&manifest)
	writeFullTrustManifest(t, fixture.root, manifest)
	record.mu.Lock()
	record.manifest = &manifest
	record.mu.Unlock()
}

// grant stores the current candidate record, as plugin_full_trust_set does.
func (fixture *fullTrustFixture) grant(t *testing.T) FullTrustSubject {
	t.Helper()
	return grantFullTrustFor(t, fixture.manager, fullTrustDemoID)
}

// rewriteByte flips one byte of a package file without changing its size and
// moves its mtime, so the (path, size, mtime) hash cache cannot hide it.
func rewriteByte(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0x01
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func fullTrustRecordFound(t *testing.T, id string) bool {
	t.Helper()
	_, found, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFullTrustKey(t *testing.T) {
	if got := fullTrustKey(fullTrustDemoID); got != "full-trust/trust.demo" {
		t.Fatalf("key = %q", got)
	}
	if got := fullTrustAuditKeyPrefix + fullTrustDemoID; got != "full-trust-audit/trust.demo" {
		t.Fatalf("audit key = %q", got)
	}
}

func TestFullTrustFingerprintStable(t *testing.T) {
	fixture := fullTrustManager(t)
	first, err := fixture.manager.fullTrustSubject(fullTrustDemoID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.manager.fullTrustSubject(fullTrustDemoID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Eligible || first.Reason != "" {
		t.Fatalf("subject = %+v", first)
	}
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints %q / %q", first.Fingerprint, second.Fingerprint)
	}
	if !reflect.DeepEqual(first.Capabilities, []string{"plugins", "storage"}) {
		t.Fatalf("capabilities = %v", first.Capabilities)
	}
	if first.PluginID != fullTrustDemoID || first.Name != "Trust Demo" || first.Version != "1.0.0" {
		t.Fatalf("subject = %+v", first)
	}
	record := first.record
	if record.Version != fullTrustRecordVersion || record.PluginVersion != "1.0.0" || record.Fingerprint != first.Fingerprint ||
		len(record.PermissionsDigest) != 64 || len(record.PackageDigest) != 64 || !reflect.DeepEqual(record.Capabilities, first.Capabilities) {
		t.Fatalf("record = %+v", record)
	}
	// The record's wire shape is fixed by the design (global constraints).
	var wire map[string]any
	if err := json.Unmarshal([]byte(jsonOf(t, record)), &wire); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for key := range wire {
		keys = append(keys, key)
	}
	if want := 7; len(keys) != want {
		t.Fatalf("record keys = %v", keys)
	}
}

func TestFullTrustLapsesOnChange(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		change func(t *testing.T, fixture *fullTrustFixture)
		revert func(t *testing.T, fixture *fullTrustFixture)
	}{
		{"version", "version_changed",
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Version = "1.0.1" })
			},
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Version = "1.0.0" })
			}},
		{"capability", "permissions_changed",
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Capabilities = append(m.Capabilities, api.CapabilityShell) })
			},
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Capabilities = fullTrustDemoManifest().Capabilities })
			}},
		{"action risk", "permissions_changed",
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Contributes.Actions[0].Risk = "dangerous" })
			},
			func(t *testing.T, f *fullTrustFixture) {
				f.updateManifest(t, func(m *Manifest) { m.Contributes.Actions[0].Risk = "write" })
			}},
		{"ui byte", "package_changed",
			func(t *testing.T, f *fullTrustFixture) { rewriteByte(t, filepath.Join(f.root, "ui", "index.html")) },
			func(t *testing.T, f *fullTrustFixture) { rewriteByte(t, filepath.Join(f.root, "ui", "index.html")) }},
		{"entry byte", "package_changed",
			func(t *testing.T, f *fullTrustFixture) { rewriteByte(t, filepath.Join(f.root, "bin", "plugin")) },
			func(t *testing.T, f *fullTrustFixture) { rewriteByte(t, filepath.Join(f.root, "bin", "plugin")) }},
		{"disabled", "disabled",
			func(t *testing.T, f *fullTrustFixture) { f.setEnabled(t, false) },
			func(t *testing.T, f *fullTrustFixture) { f.setEnabled(t, true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fullTrustManager(t)
			events := captureHostUI(t)
			fixture.grant(t)
			status, err := fixture.manager.fullTrustStatus(fullTrustDemoID)
			if err != nil || !status.Trusted || status.GrantedAt != 1762128000000 || status.Lapsed != "" {
				t.Fatalf("before change: %+v %v", status, err)
			}

			tc.change(t, fixture)
			status, err = fixture.manager.fullTrustStatus(fullTrustDemoID)
			if err != nil {
				t.Fatal(err)
			}
			if status.Trusted || status.Lapsed != tc.reason {
				t.Fatalf("after change: %+v", status)
			}
			if fullTrustRecordFound(t, fullTrustDemoID) {
				t.Fatal("lapsed record still stored")
			}
			entries, err := readFullTrustAudit(fullTrustDemoID)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) == 0 {
				t.Fatal("no audit entry")
			}
			last := entries[len(entries)-1]
			if last.Kind != "lapsed" || last.Reason != tc.reason || last.Count != 1 {
				t.Fatalf("audit = %+v", last)
			}
			event := jsonOf(t, events.last(t, fullTrustChangedEvent))
			if want := fmt.Sprintf(`{"pluginId":"trust.demo","reason":%q,"trusted":false}`, tc.reason); event != want {
				t.Fatalf("event = %s, want %s", event, want)
			}

			tc.revert(t, fixture)
			status, err = fixture.manager.fullTrustStatus(fullTrustDemoID)
			if err != nil || status.Trusted || status.Lapsed != "" {
				t.Fatalf("after revert: %+v %v", status, err)
			}
			if subject, err := fixture.manager.fullTrustSubject(fullTrustDemoID); err != nil || !subject.Eligible {
				t.Fatalf("reverted subject: %+v %v", subject, err)
			}
		})
	}
}

func TestFullTrustEligibility(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		reason string
		change func(t *testing.T, fixture *fullTrustFixture)
	}{
		{"development", fullTrustDemoID, "development", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			record.source = pluginSourceDevelopment
			record.mu.Unlock()
		}},
		{"builtin", fullTrustDemoID, "builtin", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			record.source = pluginSourceBuiltin
			record.mu.Unlock()
		}},
		{"declarative", fullTrustDemoID, "declarative", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			manifest := *record.manifest
			manifest.Runtime = ProcessRuntimeManifest{Type: "declarative"}
			record.manifest = &manifest
			record.mu.Unlock()
		}},
		{"no operations", fullTrustDemoID, "no_operations", func(t *testing.T, f *fullTrustFixture) {
			record := f.record(t)
			record.mu.Lock()
			manifest := *record.manifest
			manifest.Contributes.Actions, manifest.Contributes.Commands = nil, nil
			record.manifest = &manifest
			record.mu.Unlock()
		}},
		{"disabled", fullTrustDemoID, "disabled", func(t *testing.T, f *fullTrustFixture) { f.setEnabled(t, false) }},
		{"symlink", fullTrustDemoID, "package_unreadable", func(t *testing.T, f *fullTrustFixture) {
			if err := os.Symlink(filepath.Join(f.root, "bin", "plugin"), filepath.Join(f.root, "ui", "link")); err != nil {
				t.Skipf("symlink: %v", err)
			}
		}},
		{"too many files", fullTrustDemoID, "package_unreadable", func(t *testing.T, f *fullTrustFixture) {
			dir := filepath.Join(f.root, "lib")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// plugin.json, bin/plugin and ui/index.html plus 1998 = 2001 files.
			for i := range fullTrustPackageMaxFiles - 2 {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), []byte{byte(i)}, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"not installed", "missing.plugin", "not_installed", func(*testing.T, *fullTrustFixture) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fullTrustManager(t)
			tc.change(t, fixture)
			subject, err := fixture.manager.fullTrustSubject(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if subject.Eligible || subject.Reason != tc.reason || subject.Fingerprint != "" || subject.PluginID != tc.id {
				t.Fatalf("subject = %+v, want reason %s", subject, tc.reason)
			}
		})
	}
}

func TestFullTrustPackageDigestLimits(t *testing.T) {
	fixture := fullTrustManager(t)
	digest, err := fullTrustPackageDigest(fixture.root)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest %q %v", digest, err)
	}
	// Exactly the file limit is still readable.
	dir := filepath.Join(fixture.root, "lib")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range fullTrustPackageMaxFiles - 3 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fullTrustPackageDigest(fixture.root); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	// A new empty file changes the digest (paths are part of it).
	if err := os.WriteFile(filepath.Join(fixture.root, "extra"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fullTrustPackageDigest(fixture.root); err == nil || !strings.HasPrefix(err.Error(), "package_unreadable") {
		t.Fatalf("over limit: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.root, "extra")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := fullTrustPackageDigest(fixture.root); err != nil || changed == digest {
		t.Fatalf("new file kept digest: %v", err)
	}
	// Byte budget: a sparse file over the total limit is refused.
	big, err := os.Create(filepath.Join(fixture.root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(fullTrustPackageMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	if _, err := fullTrustPackageDigest(fixture.root); err == nil || !strings.HasPrefix(err.Error(), "package_unreadable") {
		t.Fatalf("over byte limit: %v", err)
	}
}

func TestFullTrustStatusWithoutRecord(t *testing.T) {
	fixture := fullTrustManager(t)
	events := captureHostUI(t)
	status, err := fixture.manager.fullTrustStatus(fullTrustDemoID)
	if err != nil || status.Trusted || status.Lapsed != "" || status.PluginID != fullTrustDemoID || status.LegacyDangerous {
		t.Fatalf("status = %+v %v", status, err)
	}
	if status.Subject.Name != "Trust Demo" || status.Subject.Version != "1.0.0" {
		t.Fatalf("subject = %+v", status.Subject)
	}
	if entries, err := readFullTrustAudit(fullTrustDemoID); err != nil || len(entries) != 0 {
		t.Fatalf("audit = %+v %v", entries, err)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.events) != 0 {
		t.Fatalf("events = %+v", events.events)
	}
}

func TestFullTrustStatusReportsLegacyTrust(t *testing.T) {
	fixture := fullTrustManager(t)
	if err := fixture.manager.setDangerousActionsTrusted(fullTrustDemoID, true); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.manager.fullTrustStatus(fullTrustDemoID)
	if err != nil || !status.LegacyDangerous || status.Trusted {
		t.Fatalf("status = %+v %v", status, err)
	}
}

func TestFullTrustActiveNeedsUnlock(t *testing.T) {
	fixture := fullTrustManager(t)
	setLocked := unlockAppForTest(t)
	if fixture.manager.fullTrustActive(fullTrustDemoID) {
		t.Fatal("active without a record")
	}
	fixture.grant(t)
	if !fixture.manager.fullTrustActive(fullTrustDemoID) {
		t.Fatal("not active while unlocked")
	}
	setLocked(true)
	if fixture.manager.fullTrustActive(fullTrustDemoID) {
		t.Fatal("active while locked")
	}
	if !fullTrustRecordFound(t, fullTrustDemoID) {
		t.Fatal("locking removed the record")
	}
	setLocked(false)
	if !fixture.manager.fullTrustActive(fullTrustDemoID) {
		t.Fatal("not active after unlock")
	}
}

func TestFullTrustSkip(t *testing.T) {
	fixture := fullTrustManager(t)
	setLocked := unlockAppForTest(t)
	entry := FullTrustAuditEntry{Kind: "granted", Surface: "serviceCall", Risk: "write", Subject: "backup.target", Counterpart: "official.data-backup"}
	if fixture.manager.fullTrustSkip(fullTrustDemoID, entry) {
		t.Fatal("skipped without trust")
	}
	fixture.grant(t)
	setLocked(true)
	if fixture.manager.fullTrustSkip(fullTrustDemoID, entry) {
		t.Fatal("skipped while locked")
	}
	if entries, _ := readFullTrustAudit(fullTrustDemoID); len(entries) != 0 {
		t.Fatalf("audit while not skipping: %+v", entries)
	}
	setLocked(false)
	if !fixture.manager.fullTrustSkip(fullTrustDemoID, entry) {
		t.Fatal("not skipped under trust")
	}
	entries, err := readFullTrustAudit(fullTrustDemoID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %+v %v", entries, err)
	}
	got := entries[0]
	if got.Kind != "skipped" || got.Surface != "serviceCall" || got.Risk != "write" || got.Subject != "backup.target" || got.Counterpart != "official.data-backup" || got.Count != 1 {
		t.Fatalf("audit entry = %+v", got)
	}
}

func TestRevokeFullTrust(t *testing.T) {
	fixture := fullTrustManager(t)
	events := captureHostUI(t)
	fixture.grant(t)
	fixture.manager.revokeFullTrust(fullTrustDemoID, "revoked")
	if fullTrustRecordFound(t, fullTrustDemoID) {
		t.Fatal("record kept")
	}
	entries, _ := readFullTrustAudit(fullTrustDemoID)
	if len(entries) != 1 || entries[0].Kind != "revoked" {
		t.Fatalf("audit = %+v", entries)
	}
	if event := jsonOf(t, events.last(t, fullTrustChangedEvent)); event != `{"pluginId":"trust.demo","reason":"revoked","trusted":false}` {
		t.Fatalf("event = %s", event)
	}

	fixture.grant(t)
	fixture.manager.revokeFullTrust(fullTrustDemoID, "disabled")
	entries, _ = readFullTrustAudit(fullTrustDemoID)
	if last := entries[len(entries)-1]; len(entries) != 2 || last.Kind != "lapsed" || last.Reason != "disabled" {
		t.Fatalf("audit = %+v", entries)
	}
	if event := jsonOf(t, events.last(t, fullTrustChangedEvent)); event != `{"pluginId":"trust.demo","reason":"disabled","trusted":false}` {
		t.Fatalf("event = %s", event)
	}

	// A lifecycle revoke without a record (never trusted) leaves no trace.
	fixture.manager.revokeFullTrust(fullTrustDemoID, "disabled")
	if entries, _ := readFullTrustAudit(fullTrustDemoID); len(entries) != 2 {
		t.Fatalf("audit grew without a record: %+v", entries)
	}
}

// TestFullTrustNeverLeavesTheMachine fails when the settings export, the
// encrypted backup or the plugin data archive would carry the host-side trust
// record or its audit log.
func TestFullTrustNeverLeavesTheMachine(t *testing.T) {
	fixture := fullTrustManager(t)
	useTestSecretCipher(t)
	subject := fixture.grant(t)
	if err := appendFullTrustAudit(fullTrustDemoID, FullTrustAuditEntry{Kind: "granted"}); err != nil {
		t.Fatal(err)
	}
	mustSetPluginKV(t, fullTrustDemoID, "notes", `{"count":1}`)
	mustSetSecret(t, fullTrustDemoID, "token", "plain-token")
	// A plugin ID ending in ".configuration" makes the trust keys look like
	// configuration keys to a suffix match.
	const tricky = "acme.configuration"
	mustSetPluginKV(t, hostPluginID, fullTrustKey(tricky), jsonOf(t, subject.record))
	if err := appendFullTrustAudit(tricky, FullTrustAuditEntry{Kind: "granted"}); err != nil {
		t.Fatal(err)
	}
	leaks := func(where, data string) {
		t.Helper()
		for _, marker := range []string{"full-trust", subject.Fingerprint, `"kind":"granted"`} {
			if strings.Contains(data, marker) {
				t.Fatalf("%s carries %q:\n%s", where, marker, data)
			}
		}
	}

	ids, err := pluginIDsWithConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	leaks("configured plugin IDs", strings.Join(ids, ","))
	ids, err = pluginIDsWithData()
	if err != nil {
		t.Fatal(err)
	}
	leaks("plugin IDs with data", strings.Join(ids, ","))

	for _, includeSecrets := range []bool{false, true} {
		entries, _, err := fixture.manager.collectPluginBackupEntries(includeSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Fatal("backup carried no plugin data at all")
		}
		for _, entry := range entries {
			leaks("backup entry "+entry.Path, entry.Path+"\n"+string(entry.Data))
		}
	}

	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`), "values": json.RawMessage(`{}`)}
	if _, err := writeConfigExportWithPlugins(target, config); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), `"notes"`) {
		t.Fatalf("settings export lost plugin data: %s", exported)
	}
	leaks("settings export", string(exported))

	archivePath := filepath.Join(t.TempDir(), "data.zip")
	if _, err := fixture.manager.ExportPluginDataArchive(fullTrustDemoID, archivePath); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		leaks("data archive "+file.Name, file.Name+"\n"+string(data))
	}
}
