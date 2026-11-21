package db

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeBackupExternal adds a native backup stream to fakeExternalConn.
type fakeBackupExternal struct {
	*fakeExternalConn
	stream []string
	err    error
}

func (f *fakeBackupExternal) Backup(ctx context.Context, _, _, _ string, chunk func([]byte) error) error {
	for _, s := range f.stream {
		if err := chunk([]byte(s)); err != nil {
			return err
		}
	}
	return f.err
}

const pluginMarker = "\n{\"mincodeBackupComplete\":1}\n"

func pluginBackupSession(conn ExternalConnection, version int) *dbSession {
	info := conn.Info()
	info.BackupVersion = version
	return &dbSession{kind: kindPlugin, ext: newExternalSession(conn, info)}
}

func TestPluginBackupCapabilityNegotiation(t *testing.T) {
	plain := newFakeExternalConn("tableRows", "metadata.ddl")
	if c := backupSessionCapability(pluginBackupSession(plain, 0), "plugin"); c.Supported || c.Reason == "" {
		t.Fatalf("a driver without backupVersion must be refused with a reason: %+v", c)
	}
	native := &fakeBackupExternal{fakeExternalConn: newFakeExternalConn()}
	if c := backupSessionCapability(pluginBackupSession(native, 1), "plugin"); !c.Supported || c.Reason != "" {
		t.Fatalf("a negotiated native stream must be supported: %+v", c)
	}
	// Announcing the version without implementing the stream is refused.
	if c := backupSessionCapability(pluginBackupSession(plain, 1), "plugin"); c.Supported {
		t.Fatal("backupVersion without a provider must not be supported")
	}
}

func TestPluginBackupArchive(t *testing.T) {
	native := &fakeBackupExternal{fakeExternalConn: newFakeExternalConn(), stream: []string{
		`{"kind":"metadata","definition":{"ddl":"CREATE TABLE t (b BLOB)"}}` + "\n",
		`{"kind":"row","values":[{"type":"binary-base64","value":"AP8="}]}`, "\n" + pluginMarker[1:],
	}}
	job := BackupJob{ID: "plugin-job", FileNamePrefix: "p", Format: "archive", Destination: t.TempDir(),
		Targets: []BackupTarget{{Database: "main", Tables: []string{"t"}}}}
	result, err := writeBackupArchive(context.Background(), job, pluginBackupSession(native, 1), "plugin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(result.File)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	found := false
	for _, f := range archive.File {
		if !strings.HasPrefix(f.Name, "objects/") {
			continue
		}
		r, _ := f.Open()
		raw, _ := io.ReadAll(r)
		r.Close()
		found = strings.Contains(string(raw), `"AP8="`) && strings.HasSuffix(string(raw), pluginMarker)
	}
	if !found {
		t.Fatal("plugin stream not stored verbatim with its completion marker")
	}
}

func TestPluginBackupIncompleteStreamIsNotPublished(t *testing.T) {
	for name, native := range map[string]*fakeBackupExternal{
		"no marker":    {fakeExternalConn: newFakeExternalConn(), stream: []string{`{"kind":"row","values":[]}` + "\n"}},
		"driver error": {fakeExternalConn: newFakeExternalConn(), stream: []string{`{"kind":"row"}`}, err: errors.New("driver_error: lost connection")},
	} {
		t.Run(name, func(t *testing.T) {
			job := BackupJob{ID: "cut", FileNamePrefix: "cut", Format: "archive", Destination: t.TempDir(),
				Targets: []BackupTarget{{Database: "main", Tables: []string{"t"}}}}
			if _, err := writeBackupArchive(context.Background(), job, pluginBackupSession(native, 1), "plugin", time.Now()); err == nil {
				t.Fatal("an incomplete plugin stream was accepted")
			}
			if entries, _ := os.ReadDir(job.Destination); len(entries) != 0 {
				t.Fatalf("incomplete backup left files: %v", entries)
			}
		})
	}
}
