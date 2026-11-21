package filetemplates

import (
	"context"
	"encoding/json"
	"fmt"
	"mincode/app/backend/core"
	"mincode/app/backend/storage"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	return &Store{DB: db}
}
func draft() Template {
	return Template{Kind: "file", Title: "Example", DefaultName: "Example", Files: []File{{Path: "{{mincode.namePascal}}.txt", Content: "Hello {{mincode.name}}\n"}}}
}
func TestCustomPersistenceAndConflicts(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	saved, err := s.Save(ctx, 0, draft())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Template.ID == "" || saved.Revision != 1 {
		t.Fatal(saved)
	}
	if _, err = s.Save(ctx, 0, saved.Template); err == nil {
		t.Fatal("stale edit accepted")
	}
	if _, err = s.Delete(ctx, 0, saved.Template.ID); err == nil {
		t.Fatal("stale delete accepted")
	}
	got, err := s.Get(ctx, saved.Template.ID)
	if err != nil || got.Template.Files[0].Content != "Hello {{mincode.name}}\n" {
		t.Fatal(got, err)
	}
	storage.Close()
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	s.DB = db
	got, err = s.Get(ctx, saved.Template.ID)
	if err != nil || got.Revision != 1 {
		t.Fatal(got, err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "Hello") {
		t.Fatal("list leaks bodies")
	}
	if _, err = s.Delete(ctx, 1, saved.Template.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, 2, saved.Template); err == nil {
		t.Fatal("deleted update became create")
	}
}
func TestCustomValidationAndFailedWrites(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	for _, path := range []string{"../bad", ".env", "a/../../b", "C:/escape"} {
		d := draft()
		d.Files[0].Path = path
		if _, err := s.Save(ctx, 0, d); err == nil {
			t.Fatal(path)
		}
	}
	d := draft()
	d.Kind = "project"
	d.Files = []File{{Path: "a", Content: "a"}, {Path: "a/b", Content: "b"}}
	if _, err := s.Save(ctx, 0, d); err == nil {
		t.Fatal("prefix collision")
	}
	s.DB.Close()
	if _, err := s.Save(ctx, 0, draft()); err == nil {
		t.Fatal("closed db save succeeded")
	}
}
func TestCustomCorruptDocumentAndStrictJSON(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	for _, raw := range []string{`{"version":99,"revision":0,"templates":[]}`, `{"version":1,"version":1,"revision":0,"templates":[]}`, `{"version":1,"revision":0,"templates":[]} {}`, `{"version":1,"revision":0,"templates":[],"unknown":1}`} {
		if _, err := s.DB.Exec("INSERT OR REPLACE INTO app_kv(key,value) VALUES(?,?)", storageKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(ctx); err == nil {
			t.Fatal("corrupt saved document accepted")
		}
	}
}

func TestCustomAllowsHumanDefaultNamesAndAtomicFailure(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	d := draft()
	d.DefaultName = "User Card"
	saved, err := s.Save(ctx, 0, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER deny_template_write BEFORE UPDATE ON app_kv BEGIN SELECT RAISE(ABORT,'fixture disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	edited := saved.Template
	edited.Title = "Not saved"
	if _, err = s.Save(ctx, saved.Revision, edited); err == nil {
		t.Fatal("failed update succeeded")
	}
	got, err := s.Get(ctx, saved.Template.ID)
	if err != nil || got.Template.Title != "Example" || got.Revision != 1 {
		t.Fatal(got, err)
	}
}
func TestCustomConcurrentRevisionAndBudgets(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.Save(ctx, 0, draft()); results <- err }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("lost update", successes)
	}
	list, err := s.List(ctx)
	if err != nil || len(list.Templates) != 1 {
		t.Fatal(list, err)
	}
	d := draft()
	d.Files[0].Content = strings.Repeat("x", (256<<10)+1)
	if _, err = s.Save(ctx, list.Revision, d); err == nil {
		t.Fatal("large file accepted")
	}
	d = draft()
	d.Files[0].Content = "{{mincode.unknown}}"
	if _, err = s.Save(ctx, list.Revision, d); err == nil {
		t.Fatal("unknown variable accepted")
	}
}

func TestCustomRejectsEmptyOrNullStoredDocument(t *testing.T) {
	s := fixture(t)
	for _, raw := range []string{`null`, `{}`, `[]`, `{"version":1,"revision":0}`, `{"revision":0,"templates":[]}`} {
		if _, err := s.DB.Exec("INSERT OR REPLACE INTO app_kv(key,value) VALUES(?,?)", storageKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(context.Background()); err == nil {
			t.Fatalf("invalid saved document accepted: %s", raw)
		}
	}
}

func TestCustomCommandsRejectWrongActionFieldsAndMissingRevision(t *testing.T) {
	fixture(t)
	for _, tc := range []struct{ command, raw string }{
		{"list", `null`}, {"list", `{"id":"ignored"}`}, {"get", `{}`},
		{"save", `{"template":{"kind":"file","title":"Example","defaultName":"Example","files":[{"path":"a.txt","content":"x"}]}}`},
		{"delete", `{"revision":0,"id":"","template":{}}`},
	} {
		if _, err := core.DispatchContext(context.Background(), "file_template_custom_"+tc.command, tc.raw); err == nil {
			t.Fatalf("unexpected request accepted: %s", tc.command)
		}
	}
}

func TestCustomDefaultNameCannotPoisonReadableLibrary(t *testing.T) {
	s := fixture(t)
	for _, name := range []string{".env ", " .env", ".git ", "{{mincode.unknown}}"} {
		d := draft()
		d.DefaultName = name
		if _, err := s.Save(context.Background(), 0, d); err == nil {
			t.Fatalf("invalid default name committed: %q", name)
		}
	}
	got, err := s.List(context.Background())
	if err != nil || len(got.Templates) != 0 {
		t.Fatalf("library changed after refusal: %+v %v", got, err)
	}
}

func TestCustomStoredFileRequiresExplicitTextContent(t *testing.T) {
	s := fixture(t)
	saved, err := s.Save(context.Background(), 0, draft())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"content":null`, `"other":"ignored"`} {
		raw := fmt.Sprintf(`{"version":1,"revision":1,"templates":[{"id":%q,"revision":1,"kind":"file","title":"Example","description":"","category":"","defaultName":"Example","files":[{"path":"a.txt",%s}]}]}`, saved.Template.ID, field)
		if field == `"other":"ignored"` {
			raw = strings.ReplaceAll(raw, `,"other":"ignored"`, "")
		}
		if _, err := s.DB.Exec("UPDATE app_kv SET value=? WHERE key=?", raw, storageKey); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(context.Background()); err == nil {
			t.Fatalf("missing text accepted: %s", field)
		}
		var preserved string
		if err := s.DB.QueryRow("SELECT value FROM app_kv WHERE key=?", storageKey).Scan(&preserved); err != nil || preserved != raw {
			t.Fatal("invalid library rewritten", err)
		}
	}
}
