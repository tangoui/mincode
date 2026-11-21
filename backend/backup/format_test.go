package backup

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

var testPassword = []byte("correct horse battery")

func TestMain(m *testing.M) {
	// Lower the KDF cost so the suite stays fast; Open always reads the
	// parameters from the header, so this only affects files sealed here.
	argon2Params = kdfParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}
	m.Run()
}

func testManifest() Manifest {
	return Manifest{
		CreatedAt:       time.Date(2026, 10, 29, 8, 30, 0, 0, time.UTC),
		AppVersion:      "0.2.0",
		Device:          "test-mac",
		IncludesSecrets: true,
		Sections: []SectionSummary{
			{ID: "settings", Title: "设置", Items: 2, Bytes: 42},
			{ID: "plugin:notes", Title: "笔记", Items: 1, Bytes: 7, Plugin: "notes"},
		},
	}
}

func testEntries() []Entry {
	return []Entry{
		{Path: "settings/app.json", Data: []byte(`{"theme":"dark"}`)},
		{Path: "plugins/notes/kv.json", Data: []byte("hello\x00world")},
	}
}

func sealToBytes(t *testing.T, manifest Manifest, entries []Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Seal(&buf, testPassword, manifest, entries); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return buf.Bytes()
}

func TestSealOpenRoundTrip(t *testing.T) {
	manifest := testManifest()
	entries := testEntries()
	data := sealToBytes(t, manifest, entries)

	if !bytes.HasPrefix(data, []byte("MCBAK1\n\x01")) {
		t.Fatalf("missing magic/version prefix: %q", data[:8])
	}

	got, gotEntries, err := Open(bytes.NewReader(data), testPassword)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Format != "mincode-backup" || got.Version != 1 {
		t.Fatalf("format/version = %q/%d", got.Format, got.Version)
	}
	if !got.CreatedAt.Equal(manifest.CreatedAt) || got.AppVersion != manifest.AppVersion ||
		got.Device != manifest.Device || got.IncludesSecrets != manifest.IncludesSecrets {
		t.Fatalf("manifest mismatch: %+v", got)
	}
	if len(got.Sections) != len(manifest.Sections) {
		t.Fatalf("sections = %+v", got.Sections)
	}
	for i := range manifest.Sections {
		if got.Sections[i] != manifest.Sections[i] {
			t.Fatalf("section %d = %+v, want %+v", i, got.Sections[i], manifest.Sections[i])
		}
	}
	if got.ContentSHA256 != ContentHash(entries) || len(got.ContentSHA256) != 64 {
		t.Fatalf("content hash = %q", got.ContentSHA256)
	}
	if len(gotEntries) != len(entries) {
		t.Fatalf("entries = %d, want %d", len(gotEntries), len(entries))
	}
	for i := range entries {
		if gotEntries[i].Path != entries[i].Path || !bytes.Equal(gotEntries[i].Data, entries[i].Data) {
			t.Fatalf("entry %d = %q/%q", i, gotEntries[i].Path, gotEntries[i].Data)
		}
	}
}

func TestOpenWrongPassword(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	_, entries, err := Open(bytes.NewReader(data), []byte("wrong password!!"))
	if !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err = %v, want ErrWrongPassword", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
}

func TestOpenTamperedHeader(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	data[saltOffset] ^= 0x01
	if _, _, err := Open(bytes.NewReader(data), testPassword); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err = %v, want ErrWrongPassword", err)
	}
}

func TestOpenTamperedCiphertext(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	data[len(data)-1] ^= 0x01
	if _, _, err := Open(bytes.NewReader(data), testPassword); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err = %v, want ErrWrongPassword", err)
	}
}

// endlessReader yields a valid header followed by an unbounded stream of
// zeros, counting how many bytes Open pulled from it.
type endlessReader struct {
	header []byte
	read   int64
}

func (r *endlessReader) Read(p []byte) (int, error) {
	n := 0
	if len(r.header) > 0 {
		n = copy(p, r.header)
		r.header = r.header[n:]
	}
	for i := n; i < len(p); i++ {
		p[i] = 0
	}
	r.read += int64(len(p))
	return len(p), nil
}

func TestOpenRejectsOversize(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	r := &endlessReader{header: append([]byte(nil), data[:headerLen]...)}
	_, entries, err := Open(r, testPassword)
	if !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("err = %v, want ErrBackupTooLarge", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
	if r.read > MaxBackupBytes+64*1024 {
		t.Fatalf("Open read %d bytes; should stop just past MaxBackupBytes", r.read)
	}
}

func TestSealRejectsOversize(t *testing.T) {
	big := make([]byte, MaxBackupBytes)
	var buf bytes.Buffer
	err := Seal(&buf, testPassword, testManifest(), []Entry{{Path: "big.bin", Data: big}})
	if !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("err = %v, want ErrBackupTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("Seal wrote %d bytes on failure", buf.Len())
	}
}

// sealZip encrypts a hand-built ZIP so tests can exercise the post-decrypt
// validation paths with archives Seal itself would never produce.
func sealZip(t *testing.T, files []Entry) []byte {
	t.Helper()
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for _, f := range files {
		w, err := zw.Create(f.Path)
		if err != nil {
			t.Fatalf("zip create %q: %v", f.Path, err)
		}
		if _, err := w.Write(f.Data); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	var out bytes.Buffer
	if err := encrypt(&out, testPassword, zbuf.Bytes()); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return out.Bytes()
}

func manifestJSON(t *testing.T, entries []Entry) []byte {
	t.Helper()
	m := testManifest()
	m.Format = manifestFormat
	m.Version = manifestVersion
	m.ContentSHA256 = ContentHash(entries)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

func TestOpenRejectsZipPathTraversal(t *testing.T) {
	for _, name := range []string{"../x", "a/../b", "/abs", "/etc/passwd", "a/../../x", `a\b`, "a/./b", "a//b", "..", ".", "a\x00b", "C:x", ""} {
		t.Run(name, func(t *testing.T) {
			bad := []Entry{{Path: name, Data: []byte("evil")}}
			files := append([]Entry{{Path: manifestName, Data: manifestJSON(t, bad)}}, bad...)
			_, entries, err := Open(bytes.NewReader(sealZip(t, files)), testPassword)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
			if entries != nil {
				t.Fatalf("entries = %v, want nil", entries)
			}
		})
	}
}

// Plugin storage accepts these names, so a backup of them must round-trip.
func TestSealOpenAllowsDotsInsideName(t *testing.T) {
	entries := []Entry{
		{Path: "plugins/x/files/a..b.txt", Data: []byte("dots")},
		{Path: "plugins/x/files/..hidden", Data: []byte("leading")},
		{Path: "plugins/x/files/tail..", Data: []byte("trailing")},
		{Path: "plugins/x/files/tab\tand\nnewline", Data: []byte("control")},
	}
	_, got, err := Open(bytes.NewReader(sealToBytes(t, testManifest(), entries)), testPassword)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("entries = %d, want %d", len(got), len(entries))
	}
	for i := range entries {
		if got[i].Path != entries[i].Path || !bytes.Equal(got[i].Data, entries[i].Data) {
			t.Fatalf("entry %d = %q/%q", i, got[i].Path, got[i].Data)
		}
	}
}

func TestOpenRequiresManifestFirst(t *testing.T) {
	entries := testEntries()
	files := append(append([]Entry(nil), entries...), Entry{Path: manifestName, Data: manifestJSON(t, entries)})
	if _, _, err := Open(bytes.NewReader(sealZip(t, files)), testPassword); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestSealRejectsBadEntryPaths(t *testing.T) {
	cases := [][]Entry{
		{{Path: manifestName, Data: []byte("{}")}},
		{{Path: "../x"}},
		{{Path: "/abs"}},
		{{Path: `a\b`}},
		{{Path: "a\x00b"}},
		{{Path: "dup"}, {Path: "dup"}},
	}
	for _, entries := range cases {
		var buf bytes.Buffer
		if err := Seal(&buf, testPassword, testManifest(), entries); err == nil {
			t.Fatalf("Seal(%q) succeeded, want error", entries[0].Path)
		}
	}
}

func TestOpenDetectsContentMismatch(t *testing.T) {
	m := testManifest()
	m.ContentSHA256 = ContentHash([]Entry{{Path: "other", Data: []byte("x")}})
	var buf bytes.Buffer
	if err := sealRaw(&buf, testPassword, m, testEntries()); err != nil {
		t.Fatalf("sealRaw: %v", err)
	}
	_, entries, err := Open(&buf, testPassword)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if errors.Is(err, ErrWrongPassword) {
		t.Fatalf("content mismatch must not be reported as wrong password")
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil", entries)
	}
}

func TestOpenRejectsOutOfRangeKDFParams(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	cases := []struct {
		name  string
		patch func(h []byte)
	}{
		{"time zero", func(h []byte) { binary.BigEndian.PutUint32(h[timeOffset:], 0) }},
		{"time huge", func(h []byte) { binary.BigEndian.PutUint32(h[timeOffset:], 11) }},
		{"memory small", func(h []byte) { binary.BigEndian.PutUint32(h[memoryOffset:], 8*1024-1) }},
		{"memory huge", func(h []byte) { binary.BigEndian.PutUint32(h[memoryOffset:], 1024*1024+1) }},
		{"threads zero", func(h []byte) { h[threadsOffset] = 0 }},
		{"threads huge", func(h []byte) { h[threadsOffset] = 17 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patched := append([]byte(nil), data...)
			tc.patch(patched)
			start := time.Now()
			_, _, err := Open(bytes.NewReader(patched), testPassword)
			if !errors.Is(err, ErrInvalidFormat) {
				t.Fatalf("err = %v, want ErrInvalidFormat", err)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("rejection took %v; KDF must not run", d)
			}
		})
	}
}

func TestOpenRejectsBadMagicAndShortInput(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	badMagic := append([]byte(nil), data...)
	badMagic[0] = 'X'
	badVersion := append([]byte(nil), data...)
	badVersion[versionOffset] = 2
	for name, in := range map[string][]byte{
		"empty":       nil,
		"short":       data[:headerLen-1],
		"bad magic":   badMagic,
		"bad version": badVersion,
	} {
		if _, _, err := Open(bytes.NewReader(in), testPassword); !errors.Is(err, ErrInvalidFormat) {
			t.Fatalf("%s: err = %v, want ErrInvalidFormat", name, err)
		}
	}
}

func TestOpenUsesHeaderParams(t *testing.T) {
	data := sealToBytes(t, testManifest(), testEntries())
	saved := argon2Params
	argon2Params = kdfParams{Time: 2, MemoryKiB: 16 * 1024, Threads: 2}
	defer func() { argon2Params = saved }()
	if _, _, err := Open(bytes.NewReader(data), testPassword); err != nil {
		t.Fatalf("Open with different package params: %v", err)
	}
}

func TestDefaultKDFParams(t *testing.T) {
	want := kdfParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}
	if defaultArgon2Params != want {
		t.Fatalf("default params = %+v, want %+v", defaultArgon2Params, want)
	}
}

func TestValidatePasswordMinimum(t *testing.T) {
	if err := ValidatePassword([]byte("12345678901")); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("11 chars: err = %v, want ErrPasswordTooShort", err)
	}
	if err := ValidatePassword([]byte("123456789012")); err != nil {
		t.Fatalf("12 chars: %v", err)
	}
	// 11 runes but 33 bytes: still too short because length counts runes.
	if err := ValidatePassword([]byte("密码密码密码密码密码密")); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("11 runes: err = %v, want ErrPasswordTooShort", err)
	}
	if err := ValidatePassword([]byte("密码密码密码密码密码密码")); err != nil {
		t.Fatalf("12 runes: %v", err)
	}
	var buf bytes.Buffer
	if err := Seal(&buf, []byte("short"), testManifest(), testEntries()); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("Seal short password: err = %v", err)
	}
}

func TestContentHashStableAcrossOrder(t *testing.T) {
	entries := testEntries()
	reversed := []Entry{entries[1], entries[0]}
	if ContentHash(entries) != ContentHash(reversed) {
		t.Fatalf("content hash depends on entry order")
	}
	a, _, err := Open(bytes.NewReader(sealToBytes(t, testManifest(), entries)), testPassword)
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, _, err := Open(bytes.NewReader(sealToBytes(t, testManifest(), reversed)), testPassword)
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	if a.ContentSHA256 != b.ContentSHA256 {
		t.Fatalf("sealed hashes differ: %s vs %s", a.ContentSHA256, b.ContentSHA256)
	}
	// Length framing keeps boundaries unambiguous.
	x := []Entry{{Path: "a", Data: []byte("bc")}}
	y := []Entry{{Path: "ab", Data: []byte("c")}}
	if ContentHash(x) == ContentHash(y) {
		t.Fatalf("hash collides across path/data boundary")
	}
}

var _ io.Reader = (*endlessReader)(nil)
