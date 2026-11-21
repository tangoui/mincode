package plugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestExchange(t *testing.T) (*serviceExchange, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "exchange")
	return newServiceExchange(root), root
}

// useExchangeClock pins serviceExchangeNow and returns a function that moves
// it forward.
func useExchangeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	previous := serviceExchangeNow
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	serviceExchangeNow = func() time.Time { return now }
	t.Cleanup(func() { serviceExchangeNow = previous })
	return func(step time.Duration) { now = now.Add(step) }
}

func writeExchangeSource(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireExchangeCode(t *testing.T, err error, code string) {
	t.Helper()
	var coded *contributionError
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v, want code %q", err, code)
	}
}

func mustBeginExchangeCall(t *testing.T, x *serviceExchange, callID string) {
	t.Helper()
	if err := x.beginCall(callID); err != nil {
		t.Fatal(err)
	}
}

func mustCreateOutput(t *testing.T, x *serviceExchange, callID, provider, name string, data []byte) exchangeFile {
	t.Helper()
	session, err := x.createBegin(callID, provider, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.createAppend(provider, session, 0, data); err != nil {
		t.Fatal(err)
	}
	file, err := x.createCommit(provider, session)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func mustUpload(t *testing.T, x *serviceExchange, owner, name string, data []byte) string {
	t.Helper()
	session, err := x.uploadBegin(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.uploadAppend(owner, session, 0, data); err != nil {
		t.Fatal(err)
	}
	token, err := x.uploadCommit(owner, session, name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func mustReadExchange(t *testing.T, x *serviceExchange, reader, token string) []byte {
	t.Helper()
	data, err := x.read(reader, token, 0, serviceChunkMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// exchangeDiskFiles lists the regular files under root.
func exchangeDiskFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestExchangeSnapshotIsolatedFromSource(t *testing.T) {
	x, root := newTestExchange(t)
	source := writeExchangeSource(t, "notes.txt", []byte("old"))
	mustBeginExchangeCall(t, x, "call-1")
	file, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(file.Token, "sf_") || len(file.Token) != 3+48 || strings.Trim(file.Token[3:], "0123456789abcdef") != "" {
		t.Fatalf("token = %q, want sf_ + 48 hex", file.Token)
	}
	if file.Name != "notes.txt" || file.Mime != "text/plain" || file.Size != 3 {
		t.Fatalf("file = %+v", file)
	}
	if err := os.WriteFile(source, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mustReadExchange(t, x, "provider.a", file.Token); string(got) != "old" {
		t.Fatalf("read = %q, want the snapshot", got)
	}
	for _, path := range exchangeDiskFiles(t, root) {
		if strings.Contains(path, "notes") {
			t.Fatalf("disk name %q reuses the original file name", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file mode = %o, want 600", perm)
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("root mode = %o, want 700", perm)
	}
	// Directories and non-files are not snapshots.
	_, err = x.snapshotPath("call-1", "provider.a", filepath.Dir(source))
	if err == nil {
		t.Fatal("directory snapshot succeeded")
	}
	_, err = x.snapshotPath("unknown-call", "provider.a", source)
	requireExchangeCode(t, err, contributionErrNotFound)
}

func TestExchangeTokenOwnership(t *testing.T) {
	x, _ := newTestExchange(t)
	source := writeExchangeSource(t, "a.txt", []byte("input"))
	mustBeginExchangeCall(t, x, "call-1")
	input, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.read("provider.b", input.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.read("provider.a", "sf_"+strings.Repeat("0", 48), 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)

	session, err := x.createBegin("call-1", "provider.a", "out.png")
	if err != nil {
		t.Fatal(err)
	}
	requireExchangeCode(t, x.createAppend("provider.b", session, 0, []byte("x")), contributionErrNotFound)
	if err := x.createAppend("provider.a", session, 0, []byte("output")); err != nil {
		t.Fatal(err)
	}
	_, err = x.createCommit("provider.b", session)
	requireExchangeCode(t, err, contributionErrNotFound)
	output, err := x.createCommit("provider.a", session)
	if err != nil {
		t.Fatal(err)
	}
	if output.Name != "out.png" || output.Mime != "image/png" || output.Size != 6 {
		t.Fatalf("output = %+v", output)
	}
	// Nobody reads an output before it is handed over.
	_, err = x.read("caller.c", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.read("provider.a", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	// Only the creating provider can hand over, only within its call.
	_, err = x.handOutputs("call-1", "provider.b", "caller.c", []string{output.Token})
	requireExchangeCode(t, err, contributionErrNotFound)
	mustBeginExchangeCall(t, x, "call-2")
	_, err = x.handOutputs("call-2", "provider.a", "caller.c", []string{output.Token})
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.handOutputs("call-1", "provider.a", "caller.c", []string{input.Token})
	requireExchangeCode(t, err, contributionErrNotFound)
	handed, err := x.handOutputs("call-1", "provider.a", "caller.c", []string{output.Token})
	if err != nil {
		t.Fatal(err)
	}
	if len(handed) != 1 || handed[0] != output {
		t.Fatalf("handed = %+v", handed)
	}
	if got := mustReadExchange(t, x, "caller.c", output.Token); string(got) != "output" {
		t.Fatalf("caller read = %q", got)
	}
	_, err = x.read("provider.a", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)

	// Uploads are referenced only by their uploader and read only by the
	// provider they are adopted for.
	upload := mustUpload(t, x, "caller.c", "data.json", []byte(`{"a":1}`))
	_, err = x.read("caller.c", upload, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.adoptUpload("call-2", "caller.d", upload, "provider.a")
	requireExchangeCode(t, err, contributionErrNotFound)
	adopted, err := x.adoptUpload("call-2", "caller.c", upload, "provider.a")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Name != "data.json" || adopted.Mime != "application/json" || adopted.Size != 7 || adopted.Token == upload {
		t.Fatalf("adopted = %+v", adopted)
	}
	if got := mustReadExchange(t, x, "provider.a", adopted.Token); string(got) != `{"a":1}` {
		t.Fatalf("adopted read = %q", got)
	}
	_, err = x.read("caller.c", adopted.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.adoptUpload("call-2", "caller.c", upload, "provider.a")
	requireExchangeCode(t, err, contributionErrNotFound)

	// copyOut follows the same read rules.
	destination := filepath.Join(t.TempDir(), "saved.png")
	requireExchangeCode(t, x.copyOut("caller.d", output.Token, destination), contributionErrNotFound)
	if err := x.copyOut("caller.c", output.Token, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "output" {
		t.Fatalf("copyOut = %q, %v", got, err)
	}
	// release only works for the current reader.
	x.release("caller.d", output.Token)
	mustReadExchange(t, x, "caller.c", output.Token)
	x.release("caller.c", output.Token)
	_, err = x.read("caller.c", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
}

func TestExchangeChunkLimits(t *testing.T) {
	x, _ := newTestExchange(t)
	source := writeExchangeSource(t, "a.bin", []byte("0123456789"))
	mustBeginExchangeCall(t, x, "call-1")
	input, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.read("provider.a", input.Token, 0, serviceChunkMaxBytes+1)
	requireExchangeCode(t, err, contributionErrTooLarge)
	if got, err := x.read("provider.a", input.Token, 4, 3); err != nil || string(got) != "456" {
		t.Fatalf("ranged read = %q, %v", got, err)
	}
	if got, err := x.read("provider.a", input.Token, 8, 100); err != nil || string(got) != "89" {
		t.Fatalf("tail read = %q, %v", got, err)
	}
	if got, err := x.read("provider.a", input.Token, 10, 100); err != nil || len(got) != 0 {
		t.Fatalf("read at end = %q, %v", got, err)
	}
	if _, err := x.read("provider.a", input.Token, -1, 1); err == nil {
		t.Fatal("negative offset accepted")
	}

	session, err := x.createBegin("call-1", "provider.a", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.createAppend("provider.a", session, 1, []byte("x")); err == nil {
		t.Fatal("non-contiguous append accepted")
	}
	requireExchangeCode(t, x.createAppend("provider.a", session, 0, make([]byte, serviceChunkMaxBytes+1)), contributionErrTooLarge)
	chunk := make([]byte, serviceChunkMaxBytes)
	var offset int64
	for offset < serviceFileMaxBytes {
		if err := x.createAppend("provider.a", session, offset, chunk); err != nil {
			t.Fatalf("append at %d: %v", offset, err)
		}
		offset += int64(len(chunk))
	}
	requireExchangeCode(t, x.createAppend("provider.a", session, offset, []byte("x")), contributionErrTooLarge)
	full, err := x.createCommit("provider.a", session)
	if err != nil {
		t.Fatal(err)
	}
	if full.Size != serviceFileMaxBytes {
		t.Fatalf("size = %d, want %d", full.Size, serviceFileMaxBytes)
	}

	upload, err := x.uploadBegin("caller.c")
	if err != nil {
		t.Fatal(err)
	}
	requireExchangeCode(t, x.uploadAppend("caller.c", upload, 0, make([]byte, serviceChunkMaxBytes+1)), contributionErrTooLarge)
	if err := x.uploadAppend("caller.c", upload, 3, []byte("x")); err == nil {
		t.Fatal("non-contiguous upload append accepted")
	}

	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, serviceFileMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	_, err = x.snapshotPath("call-1", "provider.a", big)
	requireExchangeCode(t, err, contributionErrTooLarge)
}

func TestExchangeCallTotal(t *testing.T) {
	previous := serviceCallMaxBytes
	serviceCallMaxBytes = 10
	t.Cleanup(func() { serviceCallMaxBytes = previous })
	x, _ := newTestExchange(t)
	six := writeExchangeSource(t, "six.txt", []byte("123456"))
	mustBeginExchangeCall(t, x, "call-1")
	if _, err := x.snapshotPath("call-1", "provider.a", six); err != nil {
		t.Fatal(err)
	}
	_, err := x.snapshotPath("call-1", "provider.a", six)
	requireExchangeCode(t, err, contributionErrTooLarge)
	upload := mustUpload(t, x, "caller.c", "u.txt", []byte("123456"))
	_, err = x.adoptUpload("call-1", "caller.c", upload, "provider.a")
	requireExchangeCode(t, err, contributionErrTooLarge)
	session, err := x.createBegin("call-1", "provider.a", "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.createAppend("provider.a", session, 0, []byte("1234")); err != nil {
		t.Fatal(err)
	}
	requireExchangeCode(t, x.createAppend("provider.a", session, 4, []byte("5")), contributionErrTooLarge)
	// A separate call has its own budget.
	mustBeginExchangeCall(t, x, "call-2")
	if _, err := x.snapshotPath("call-2", "provider.a", six); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeSanitizesNames(t *testing.T) {
	x, root := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")
	long := strings.Repeat("x", 296) + ".png"
	cases := []struct{ raw, want string }{
		{"../../a.png", "a.png"},
		{`a/b\c.png`, "c.png"},
		{long, ""},
		{"\xff.png", "_.png"},
		{"..", "file"},
		{"", "file"},
		{"evil‮gnp.exe", "evil_gnp.exe"},
	}
	check := func(raw string, want string, file exchangeFile) {
		t.Helper()
		name := file.Name
		if want != "" && name != want {
			t.Fatalf("name(%q) = %q, want %q", raw, name, want)
		}
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || !utf8.ValidString(name) || len(name) > 255 {
			t.Fatalf("name(%q) = %q is not a safe base name", raw, name)
		}
	}
	for _, tc := range cases {
		output := mustCreateOutput(t, x, "call-1", "provider.a", tc.raw, []byte("x"))
		check(tc.raw, tc.want, output)
		token := mustUpload(t, x, "caller.c", tc.raw, []byte("y"))
		adopted, err := x.adoptUpload("call-1", "caller.c", token, "provider.a")
		if err != nil {
			t.Fatal(err)
		}
		check(tc.raw, tc.want, adopted)
	}
	longOutput := mustCreateOutput(t, x, "call-1", "provider.a", long, []byte("x"))
	if !strings.HasSuffix(longOutput.Name, ".png") || len(longOutput.Name) > 255 {
		t.Fatalf("long name = %q", longOutput.Name)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range exchangeDiskFiles(t, root) {
		rel, err := filepath.Rel(absRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("disk path %q escapes root %q", path, absRoot)
		}
		if strings.Contains(filepath.Base(path), "png") || strings.Contains(filepath.Base(path), "exe") {
			t.Fatalf("disk name %q derives from the plugin's file name", path)
		}
	}
	for token, entry := range x.files {
		rel, err := filepath.Rel(absRoot, entry.path)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("entry %s path %q escapes root", token, entry.path)
		}
	}
}

func TestExchangeEndCallDropsInputs(t *testing.T) {
	advance := useExchangeClock(t)
	x, root := newTestExchange(t)
	source := writeExchangeSource(t, "in.txt", []byte("input"))
	mustBeginExchangeCall(t, x, "call-1")
	input, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	output := mustCreateOutput(t, x, "call-1", "provider.a", "out.txt", []byte("output"))
	unhanded := mustCreateOutput(t, x, "call-1", "provider.a", "extra.txt", []byte("extra"))
	pending, err := x.createBegin("call-1", "provider.a", "pending.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.handOutputs("call-1", "provider.a", "caller.c", []string{output.Token}); err != nil {
		t.Fatal(err)
	}
	x.endCall("call-1", true)
	_, err = x.read("provider.a", input.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	if _, ok := x.files[unhanded.Token]; ok {
		t.Fatal("unhanded output survived endCall")
	}
	requireExchangeCode(t, x.createAppend("provider.a", pending, 0, []byte("x")), contributionErrNotFound)
	if got := mustReadExchange(t, x, "caller.c", output.Token); string(got) != "output" {
		t.Fatalf("output read = %q", got)
	}
	if files := exchangeDiskFiles(t, root); len(files) != 1 {
		t.Fatalf("disk files after endCall = %v, want only the output", files)
	}
	// The call ID is free again and handOutputs no longer accepts the call.
	_, err = x.handOutputs("call-1", "provider.a", "caller.c", []string{output.Token})
	requireExchangeCode(t, err, contributionErrNotFound)

	advance(serviceOutputRetention - time.Second)
	mustReadExchange(t, x, "caller.c", output.Token)
	advance(2 * time.Second)
	_, err = x.read("caller.c", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	if files := exchangeDiskFiles(t, root); len(files) != 0 {
		t.Fatalf("disk files after retention = %v", files)
	}

	// A failed or cancelled call keeps nothing.
	mustBeginExchangeCall(t, x, "call-2")
	dropped := mustCreateOutput(t, x, "call-2", "provider.a", "out.txt", []byte("output"))
	if _, err := x.handOutputs("call-2", "provider.a", "caller.c", []string{dropped.Token}); err != nil {
		t.Fatal(err)
	}
	x.endCall("call-2", false)
	_, err = x.read("caller.c", dropped.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	if files := exchangeDiskFiles(t, root); len(files) != 0 {
		t.Fatalf("disk files after failed call = %v", files)
	}
}

func TestExchangeReleaseOnPluginRemoved(t *testing.T) {
	x, root := newTestExchange(t)
	source := writeExchangeSource(t, "in.txt", []byte("input"))
	mustBeginExchangeCall(t, x, "call-1")
	input, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	unhanded := mustCreateOutput(t, x, "call-1", "provider.a", "out.txt", []byte("out"))
	creating, err := x.createBegin("call-1", "provider.a", "more.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.createAppend("provider.a", creating, 0, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	uploading, err := x.uploadBegin("provider.a")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.uploadAppend("provider.a", uploading, 0, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	upload := mustUpload(t, x, "provider.a", "u.txt", []byte("upload"))
	// Files of other plugins stay.
	other := mustUpload(t, x, "caller.c", "keep.txt", []byte("keep"))

	x.dropPlugin("provider.a")
	_, err = x.read("provider.a", input.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	_, err = x.handOutputs("call-1", "provider.a", "caller.c", []string{unhanded.Token})
	requireExchangeCode(t, err, contributionErrNotFound)
	requireExchangeCode(t, x.createAppend("provider.a", creating, 7, []byte("x")), contributionErrNotFound)
	requireExchangeCode(t, x.uploadAppend("provider.a", uploading, 7, []byte("x")), contributionErrNotFound)
	_, err = x.adoptUpload("call-1", "provider.a", upload, "provider.b")
	requireExchangeCode(t, err, contributionErrNotFound)
	files := exchangeDiskFiles(t, root)
	if len(files) != 1 || files[0] != x.files[other].path {
		t.Fatalf("disk files after dropPlugin = %v, want only the other plugin's upload", files)
	}
	if x.used != 4 {
		t.Fatalf("used = %d, want 4", x.used)
	}
	// The call itself is still usable.
	if _, err := x.adoptUpload("call-1", "caller.c", other, "provider.b"); err != nil {
		t.Fatal(err)
	}
	x.dropPlugin("")
	if len(x.files) != 1 {
		t.Fatalf("dropPlugin(\"\") removed files: %d left", len(x.files))
	}
}

// Providers are idle-stopped routinely after a call; outputs already handed
// to the caller belong to the caller and outlive the provider.
func TestExchangeHandedOutputsSurviveProviderDrop(t *testing.T) {
	x, _ := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")
	output := mustCreateOutput(t, x, "call-1", "provider.a", "out.txt", []byte("result"))
	if _, err := x.handOutputs("call-1", "provider.a", "caller.c", []string{output.Token}); err != nil {
		t.Fatal(err)
	}
	x.endCall("call-1", true)
	x.dropPlugin("provider.a")
	if got := mustReadExchange(t, x, "caller.c", output.Token); string(got) != "result" {
		t.Fatalf("read = %q", got)
	}
	x.dropPlugin("caller.c")
	_, err := x.read("caller.c", output.Token, 0, 10)
	requireExchangeCode(t, err, contributionErrNotFound)
	if x.used != 0 {
		t.Fatalf("used = %d, want 0", x.used)
	}
}

func TestExchangeQuota(t *testing.T) {
	previous := serviceExchangeMaxBytes
	serviceExchangeMaxBytes = 100
	t.Cleanup(func() { serviceExchangeMaxBytes = previous })
	x, root := newTestExchange(t)
	full := writeExchangeSource(t, "full.bin", bytes.Repeat([]byte("a"), 100))
	upload, err := x.uploadBegin("caller.c")
	if err != nil {
		t.Fatal(err)
	}
	mustBeginExchangeCall(t, x, "call-1")
	if _, err := x.snapshotPath("call-1", "provider.a", full); err != nil {
		t.Fatal(err)
	}
	requireExchangeCode(t, x.beginCall("call-2"), contributionErrBusy)
	one := writeExchangeSource(t, "one.bin", []byte("a"))
	_, err = x.snapshotPath("call-1", "provider.a", one)
	requireExchangeCode(t, err, contributionErrBusy)
	requireExchangeCode(t, x.uploadAppend("caller.c", upload, 0, []byte("a")), contributionErrBusy)
	_, err = x.uploadBegin("caller.c")
	requireExchangeCode(t, err, contributionErrBusy)

	if err := x.clearAll(); err != nil {
		t.Fatal(err)
	}
	if x.used != 0 || len(x.files) != 0 || len(x.calls) != 0 || len(x.sessions) != 0 {
		t.Fatalf("clearAll left state: used=%d files=%d calls=%d sessions=%d", x.used, len(x.files), len(x.calls), len(x.sessions))
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root after clearAll: %v", err)
	}
	mustBeginExchangeCall(t, x, "call-2")
	if _, err := x.snapshotPath("call-2", "provider.a", one); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeConcurrentFileCalls(t *testing.T) {
	x, _ := newTestExchange(t)
	for index := 0; index < serviceFileCallsConcurrent; index++ {
		mustBeginExchangeCall(t, x, "call-"+string(rune('a'+index)))
	}
	requireExchangeCode(t, x.beginCall("call-extra"), contributionErrBusy)
	if err := x.beginCall("call-a"); err == nil {
		t.Fatal("duplicate call ID accepted")
	}
	x.endCall("call-a", true)
	mustBeginExchangeCall(t, x, "call-extra")
}

func TestExchangeUploadSessionIdle(t *testing.T) {
	previous := serviceExchangeSessionIdle
	serviceExchangeSessionIdle = 50 * time.Millisecond
	t.Cleanup(func() { serviceExchangeSessionIdle = previous })
	x, root := newTestExchange(t)
	session, err := x.uploadBegin("caller.c")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.uploadAppend("caller.c", session, 0, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		x.mu.Lock()
		_, alive := x.sessions[session]
		x.mu.Unlock()
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle upload session was not discarded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireExchangeCode(t, x.uploadAppend("caller.c", session, 3, []byte("d")), contributionErrNotFound)
	if files := exchangeDiskFiles(t, root); len(files) != 0 {
		t.Fatalf("staged upload left on disk: %v", files)
	}
	x.mu.Lock()
	used := x.used
	x.mu.Unlock()
	if used != 0 {
		t.Fatalf("used = %d, want 0", used)
	}
}

func TestExchangeLifecycleHookDropsPlugin(t *testing.T) {
	// serviceFiles resolves its root from userDataDir, so this stays in the
	// temporary data directory.
	data := useTemporaryPluginData(t)
	x := serviceFiles
	session, err := x.uploadBegin("hooked.plugin")
	if err != nil {
		t.Fatal(err)
	}
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	manager.runRecordLifecycleHooks(&pluginRecord{id: "hooked.plugin"})
	requireExchangeCode(t, x.uploadAppend("hooked.plugin", session, 0, []byte("x")), contributionErrNotFound)
	if files := exchangeDiskFiles(t, filepath.Join(data, pluginDataDirectory, serviceExchangeDirectory)); len(files) != 0 {
		t.Fatalf("hook left session files: %v", files)
	}
}

func TestExchangeErrorsHideExchangePaths(t *testing.T) {
	x, root := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")
	output := mustCreateOutput(t, x, "call-1", "provider.a", "out.txt", []byte("x"))
	if _, err := x.handOutputs("call-1", "provider.a", "caller.c", []string{output.Token}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(x.files[output.Token].path); err != nil {
		t.Fatal(err)
	}
	_, err := x.read("caller.c", output.Token, 0, 1)
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("read error = %v, want an error without the exchange path", err)
	}
	err = x.copyOut("caller.c", output.Token, filepath.Join(t.TempDir(), "out.txt"))
	if err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("copyOut error = %v, want an error without the exchange path", err)
	}
}

// The first use in a process removes what a previous run left; later uses do
// not clear again.
func TestExchangeClearsLeftoversOnFirstUse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exchange")
	stale := filepath.Join(root, "calls", "old", "leftover")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old run"), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newServiceExchange(root)
	// Nothing is touched before the exchange is used.
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("leftover removed before first use: %v", err)
	}
	mustBeginExchangeCall(t, x, "call-1")
	if _, err := os.Stat(filepath.Join(root, "calls", "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leftover after first use: %v", err)
	}
	source := writeExchangeSource(t, "in.txt", []byte("input"))
	input, err := x.snapshotPath("call-1", "provider.a", source)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.uploadBegin("caller.c"); err != nil {
		t.Fatal(err)
	}
	mustBeginExchangeCall(t, x, "call-2")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("second use cleared the root again: %v", err)
	}
	if got := mustReadExchange(t, x, "provider.a", input.Token); string(got) != "input" {
		t.Fatalf("read after later uses = %q", got)
	}
}

// stepReader returns each of chunks on one Read, then blocks until release
// is closed (when set) or returns EOF. reads counts the bytes handed out.
type stepReader struct {
	mu      sync.Mutex
	chunks  [][]byte
	release chan struct{}
	reads   int64
}

func (r *stepReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(r.chunks) > 0 {
		n := copy(p, r.chunks[0])
		r.chunks[0] = r.chunks[0][n:]
		if len(r.chunks[0]) == 0 {
			r.chunks = r.chunks[1:]
		}
		r.reads += int64(n)
		r.mu.Unlock()
		return n, nil
	}
	release := r.release
	r.mu.Unlock()
	if release != nil {
		<-release
		return 0, errors.New("closed")
	}
	return 0, io.EOF
}

func (r *stepReader) bytesRead() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func TestSnapshotReaderReportsProgressAndHonoursCancel(t *testing.T) {
	x, root := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")

	// A complete read: progress after each chunk, the file as written, and a
	// source longer than its announced size is cut there.
	var seen []int64
	source := &stepReader{chunks: [][]byte{[]byte("hello "), []byte("world"), []byte("!extra")}}
	file, err := x.snapshotReader(context.Background(), "call-1", "provider.a", "dir/app.log", source, 12, func(done int64) { seen = append(seen, done) })
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "app.log" || file.Size != 12 || !strings.HasPrefix(file.Token, serviceExchangeTokenPrefix) {
		t.Fatalf("file = %+v", file)
	}
	if got := mustReadExchange(t, x, "provider.a", file.Token); string(got) != "hello world!" {
		t.Fatalf("snapshot = %q", got)
	}
	if !slices.Equal(seen, []int64{6, 11, 12}) {
		t.Fatalf("progress = %v", seen)
	}
	if x.used != 12 {
		t.Fatalf("used = %d", x.used)
	}

	// Cancelled while a Read blocks: the snapshot returns at once, its
	// partial file is gone and the reservation is returned.
	blocked := &stepReader{chunks: [][]byte{[]byte("partial")}, release: make(chan struct{})}
	defer close(blocked.release)
	ctx, cancel := context.WithCancel(context.Background())
	_, err = x.snapshotReader(ctx, "call-1", "provider.a", "big.log", blocked, 1000, func(done int64) {
		if done == 7 {
			cancel()
		}
	})
	requireExchangeCode(t, err, contributionErrCancelled)
	if x.used != 12 || x.calls["call-1"].bytes != 12 {
		t.Fatalf("after cancel used = %d, call bytes = %d", x.used, x.calls["call-1"].bytes)
	}
	if files := exchangeDiskFiles(t, root); len(files) != 1 {
		t.Fatalf("disk files after cancel = %v", files)
	}

	// The context's cause is reported when it is a contribution error (the
	// remote stall timeout).
	stalled := &stepReader{release: make(chan struct{})}
	defer close(stalled.release)
	stallCtx, stall := context.WithCancelCause(context.Background())
	stall(newContributionError(contributionErrTimeout, "stalled"))
	_, err = x.snapshotReader(stallCtx, "call-1", "provider.a", "s.log", stalled, 10, nil)
	requireExchangeCode(t, err, contributionErrTimeout)

	x.endCall("call-1", false)
	if x.used != 0 {
		t.Fatalf("used after endCall = %d", x.used)
	}
}

func TestSnapshotReaderSizeCap(t *testing.T) {
	x, root := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")
	// Over the per-file limit: refused before a single byte is read.
	source := &stepReader{chunks: [][]byte{[]byte("x")}}
	_, err := x.snapshotReader(context.Background(), "call-1", "provider.a", "a.log", source, serviceFileMaxBytes+1, nil)
	requireExchangeCode(t, err, contributionErrTooLarge)
	// Over the call's total: refused before reading too.
	previous := serviceCallMaxBytes
	serviceCallMaxBytes = 4
	t.Cleanup(func() { serviceCallMaxBytes = previous })
	_, err = x.snapshotReader(context.Background(), "call-1", "provider.a", "a.log", source, 5, nil)
	requireExchangeCode(t, err, contributionErrTooLarge)
	if source.bytesRead() != 0 || x.used != 0 || len(exchangeDiskFiles(t, root)) != 0 {
		t.Fatalf("read %d bytes, used %d", source.bytesRead(), x.used)
	}
	// A failing source leaves nothing behind either.
	failing := &stepReader{chunks: [][]byte{[]byte("ab")}, release: make(chan struct{})}
	close(failing.release)
	_, err = x.snapshotReader(context.Background(), "call-1", "provider.a", "a.log", failing, 4, nil)
	requireExchangeCode(t, err, contributionErrUnavailable)
	if x.used != 0 || len(exchangeDiskFiles(t, root)) != 0 {
		t.Fatalf("failed read left used = %d", x.used)
	}
}

func TestSnapshotBytesQuota(t *testing.T) {
	previous := serviceExchangeMaxBytes
	serviceExchangeMaxBytes = 10
	t.Cleanup(func() { serviceExchangeMaxBytes = previous })
	x, _ := newTestExchange(t)
	mustBeginExchangeCall(t, x, "call-1")
	file, err := x.snapshotBytes("call-1", "provider.a", "result.json", []byte(`{"rows":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "result.json" || file.Mime != "application/json" || file.Size != 10 {
		t.Fatalf("file = %+v", file)
	}
	if got := mustReadExchange(t, x, "provider.a", file.Token); string(got) != `{"rows":1}` {
		t.Fatalf("read %q", got)
	}
	_, err = x.snapshotBytes("call-1", "provider.a", "more.json", []byte("x"))
	requireExchangeCode(t, err, contributionErrBusy)
	if _, err := x.snapshotBytes("call-unknown", "provider.a", "a.json", []byte("{}")); err == nil {
		t.Fatal("snapshot into an unknown call accepted")
	}
	if _, err := x.snapshotBytes("call-1", "", "a.json", []byte("{}")); err == nil {
		t.Fatal("snapshot without owner accepted")
	}
	// An empty body is a valid, empty file.
	empty, err := x.snapshotBytes("call-1", "provider.a", "empty.json", nil)
	if err != nil || empty.Size != 0 {
		t.Fatalf("empty = %+v %v", empty, err)
	}
	x.endCall("call-1", false)
	if x.used != 0 {
		t.Fatalf("used = %d", x.used)
	}
}
