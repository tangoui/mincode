package filetemplates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"mincode/app/backend/internal/filetemplatepolicy"
	"mincode/app/plugins/api/filetmpl"
	"strings"
	"unicode"
	"unicode/utf8"
)

const storageKey = filetemplatepolicy.StorageKey
const maxRevision = 9007199254740990
const maxDocumentBytes = 32 << 20
const maxRequestBytes = 16 << 20

type Store struct{ DB *sql.DB }
type reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func strict(raw []byte, out any) error {
	if !utf8.Valid(raw) || len(raw) > maxDocumentBytes {
		return errors.New("file_template_invalid_json")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("depth")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					k, ok := key.(string)
					if !ok || seen[k] {
						return errors.New("duplicate")
					}
					seen[k] = true
					if err = walk(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err = walk(depth + 1); err != nil {
						return err
					}
				}
			default:
				return errors.New("delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("file_template_invalid_json")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("file_template_invalid_json")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("file_template_invalid_json")
	}
	return nil
}
func line(s string, max int, required bool) bool {
	if !utf8.ValidString(s) || len([]rune(s)) > max || (required && strings.TrimSpace(s) == "") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
func validate(t Template) (Template, error) {
	fail := func() (Template, error) { return t, errors.New("file_template_invalid") }
	if (t.Kind != "file" && t.Kind != "project") || !line(t.Title, 64, true) || !line(t.Description, 256, false) || !line(t.Category, 24, false) || !line(t.DefaultName, 64, true) || len(t.Files) < 1 || len(t.Files) > 64 || (t.Kind == "file" && len(t.Files) != 1) {
		return fail()
	}
	if strings.TrimSpace(t.DefaultName) != t.DefaultName || strings.ContainsAny(t.DefaultName, "/\\{}") || strings.Contains(t.DefaultName, "..") {
		return fail()
	}
	if _, err := filetmpl.TargetPath(strings.ReplaceAll(t.DefaultName, " ", "-")); err != nil {
		return fail()
	}
	seen := []string{}
	total := 0
	for i, f := range t.Files {
		if _, err := filetmpl.TargetPath(f.Path); err != nil {
			return fail()
		}
		p := strings.ToLower(f.Path)
		for _, old := range seen {
			if p == old || strings.HasPrefix(p, old+"/") || strings.HasPrefix(old, p+"/") {
				return fail()
			}
		}
		seen = append(seen, p)
		if len(f.Content) > 256<<10 {
			return fail()
		}
		body, _, err := filetmpl.Asset("", ".tmpl", []byte(f.Content))
		if err != nil {
			return fail()
		}
		t.Files[i].Content = string(body)
		total += len(body)
	}
	if total > 2<<20 {
		return fail()
	}
	return t, nil
}
func read(ctx context.Context, db reader) (Document, error) {
	out := Document{Version: 1, Templates: []Template{}}
	var raw []byte
	err := db.QueryRowContext(ctx, "SELECT value FROM app_kv WHERE key=?", storageKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out = Document{}
	if err = strict(raw, &out); err != nil {
		return out, err
	}
	if err = validateDocument(out); err != nil {
		return out, err
	}
	return out, nil
}
func validateDocument(d Document) error {
	if d.Version != 1 || d.Revision > maxRevision || d.Templates == nil || len(d.Templates) > 64 {
		return errors.New("file_template_saved_invalid")
	}
	seen := map[string]bool{}
	total := 0
	for _, t := range d.Templates {
		if _, err := uuid.Parse(t.ID); err != nil || seen[t.ID] || t.Revision == 0 || t.Revision > d.Revision {
			return errors.New("file_template_saved_invalid")
		}
		seen[t.ID] = true
		if _, err := validate(t); err != nil {
			return err
		}
		for _, f := range t.Files {
			total += len(f.Content)
		}
	}
	if total > 4<<20 {
		return errors.New("file_template_capacity")
	}
	return nil
}
func (s *Store) List(ctx context.Context) (Snapshot, error) {
	d, err := read(ctx, s.DB)
	out := Snapshot{Version: 1, Revision: d.Revision, Templates: []Summary{}}
	if err != nil {
		return out, err
	}
	for _, t := range d.Templates {
		item := Summary{ID: t.ID, Revision: t.Revision, Kind: t.Kind, Title: t.Title, Description: t.Description, Category: t.Category, DefaultName: t.DefaultName, Files: []FileSummary{}}
		for _, f := range t.Files {
			h := sha256.Sum256([]byte(f.Content))
			item.Files = append(item.Files, FileSummary{f.Path, hex.EncodeToString(h[:]), len(f.Content)})
		}
		out.Templates = append(out.Templates, item)
	}
	return out, nil
}
func (s *Store) Get(ctx context.Context, id string) (Saved, error) {
	d, err := read(ctx, s.DB)
	if err != nil {
		return Saved{}, err
	}
	for _, t := range d.Templates {
		if t.ID == id {
			return Saved{d.Revision, t}, nil
		}
	}
	return Saved{}, errors.New("file_template_not_found")
}
func (s *Store) mutate(ctx context.Context, revision uint64, t Template, id string, deleting bool) (Saved, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Saved{}, err
	}
	defer tx.Rollback()
	d, err := read(ctx, tx)
	if err != nil {
		return Saved{}, err
	}
	if d.Revision != revision || revision >= maxRevision {
		return Saved{}, errors.New("file_template_revision_conflict")
	}
	index := -1
	for i, item := range d.Templates {
		if item.ID == id {
			index = i
			break
		}
	}
	if deleting || id != "" {
		if index < 0 {
			return Saved{}, errors.New("file_template_not_found")
		}
	}
	d.Revision++
	if deleting {
		d.Templates = append(d.Templates[:index], d.Templates[index+1:]...)
	} else {
		t, err = validate(t)
		if err != nil {
			return Saved{}, err
		}
		t.Revision = d.Revision
		if id == "" {
			t.ID = uuid.NewString()
			d.Templates = append(d.Templates, t)
		} else {
			t.ID = id
			d.Templates[index] = t
		}
	}
	if err = validateDocument(d); err != nil {
		return Saved{}, err
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > maxDocumentBytes {
		return Saved{}, errors.New("file_template_capacity")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO app_kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP", storageKey, raw); err != nil {
		return Saved{}, fmt.Errorf("file_template_save_failed: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Saved{}, err
	}
	return Saved{d.Revision, t}, nil
}
func (s *Store) Save(ctx context.Context, revision uint64, t Template) (Saved, error) {
	return s.mutate(ctx, revision, t, t.ID, false)
}
func (s *Store) Delete(ctx context.Context, revision uint64, id string) (Saved, error) {
	return s.mutate(ctx, revision, Template{}, id, true)
}
