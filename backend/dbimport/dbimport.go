// Package dbimport imports database connections saved by other tools
// (DBeaver, Navicat, DataGrip/JetBrains, MySQL Workbench, HeidiSQL).
//
// Files are parsed here, in Go. The preview returned to the WebView never
// carries a password: it only reports hasPassword. Decrypted secrets stay in
// an in-memory import session until the user confirms; the commit then
// appends the chosen connections to the persisted connection list with every
// secret encrypted through crypto.EncryptForStorage (the same enc:v2 format
// encrypt_field produces). Secrets are never logged.
//
// Command contract (camelCase JSON):
//
//	db_import_detect  {}                                          -> [DetectedLocation]
//	db_import_preview {source, path}                              -> Preview
//	db_import_commit  {importId, group?, keepFolders, rows:[{id, driver?}]} -> CommitResult
//	db_import_discard {importId}                                  -> void
package dbimport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mincode/app/backend/core"
)

const (
	// maxFileBytes bounds every file an importer reads.
	maxFileBytes = 8 << 20
	// maxCandidates bounds the rows of one preview.
	maxCandidates = 2000
	// maxSourceFiles bounds how many data-source files one folder may yield.
	maxSourceFiles = 16
	// maxFieldBytes bounds any single imported string field.
	maxFieldBytes = 4096
	maxSessions   = 4
	sessionTTL    = 30 * time.Minute
)

// Source identifiers.
const (
	SourceDBeaver   = "dbeaver"
	SourceNavicat   = "navicat"
	SourceDataGrip  = "datagrip"
	SourceWorkbench = "workbench"
	SourceHeidiSQL  = "heidisql"
)

// Candidate statuses.
const (
	StatusReady       = "ready"
	StatusNeedsPlugin = "needsPlugin"
	StatusUnsupported = "unsupported"
)

// Warning codes attached to candidates or previews (translated by the UI).
const (
	WarnTunnelIncomplete    = "tunnel_incomplete"
	WarnTunnelUnsupported   = "tunnel_unsupported"
	WarnTunnelPartial       = "tunnel_partial"
	WarnHostMissing         = "host_missing"
	WarnPasswordUndecrypted = "password_undecryptable"
	WarnCredentialsMissing  = "credentials_missing"
	WarnCredentialsInvalid  = "credentials_unreadable"
	WarnURLUnparsed         = "url_unparsed"
	WarnSocketUnsupported   = "socket_unsupported"
	WarnTruncated           = "truncated"
	WarnSkippedEntries      = "entries_skipped"
)

func init() {
	core.Register("db_import_detect", detectHandler)
	core.Register("db_import_preview", previewHandler)
	core.Register("db_import_commit", commitHandler)
	core.Register("db_import_discard", discardHandler)
}

// Tunnel is the preview of an SSH / SOCKS5 tunnel (no secrets).
type Tunnel struct {
	Type          string `json:"type"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username,omitempty"`
	AuthType      string `json:"authType,omitempty"`
	KeyPath       string `json:"keyPath,omitempty"`
	HasPassword   bool   `json:"hasPassword"`
	HasPassphrase bool   `json:"hasPassphrase"`

	password   string
	passphrase string
}

// Candidate is one importable connection. Unexported fields hold secrets and
// are never serialized.
type Candidate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Folder     string `json:"folder,omitempty"`
	SourceType string `json:"sourceType"`
	// DbType is the mapped built-in type (empty for plugin-only/unsupported).
	DbType string `json:"dbType,omitempty"`
	// PluginDriverID is the databaseDrivers id a plugin-only type needs.
	PluginDriverID string `json:"pluginDriverId,omitempty"`
	Status         string `json:"status"`

	Host       string `json:"host"`
	Port       int    `json:"port"`
	Database   string `json:"database,omitempty"`
	Username   string `json:"username,omitempty"`
	FilePath   string `json:"filePath,omitempty"`
	AuthSource string `json:"authSource,omitempty"`
	SSL        bool   `json:"ssl"`

	HasPassword bool `json:"hasPassword"`
	// PasswordUnavailable: the password cannot be imported (kept in the OS
	// keychain / KeePass, not saved or not exported by the source, or
	// undecryptable); the user must enter it.
	PasswordUnavailable bool     `json:"passwordUnavailable"`
	Tunnel              *Tunnel  `json:"tunnel,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`

	password string
}

func (c *Candidate) warn(code string) {
	for _, existing := range c.Warnings {
		if existing == code {
			return
		}
	}
	c.Warnings = append(c.Warnings, code)
}

func (c *Candidate) setPassword(value string) {
	c.password = value
	c.HasPassword = value != ""
}

// Preview is the password-free result of reading one source.
type Preview struct {
	ImportID string      `json:"importId"`
	Source   string      `json:"source"`
	Path     string      `json:"path"`
	Rows     []Candidate `json:"rows"`
	Warnings []string    `json:"warnings,omitempty"`
}

type parseResult struct {
	rows     []Candidate
	warnings []string
}

func (r *parseResult) warn(code string) {
	for _, existing := range r.warnings {
		if existing == code {
			return
		}
	}
	r.warnings = append(r.warnings, code)
}

// add appends a normalized candidate, enforcing the row cap.
func (r *parseResult) add(c Candidate) {
	if len(r.rows) >= maxCandidates {
		r.warn(WarnTruncated)
		return
	}
	normalizeCandidate(&c)
	c.ID = fmt.Sprintf("r%d", len(r.rows)+1)
	r.rows = append(r.rows, c)
}

func normalizeCandidate(c *Candidate) {
	c.Name = clip(c.Name)
	c.Folder = strings.Trim(clip(c.Folder), "/ ")
	c.SourceType = clip(c.SourceType)
	c.Host = clip(c.Host)
	c.Database = clip(c.Database)
	c.Username = clip(c.Username)
	c.FilePath = clip(c.FilePath)
	c.AuthSource = clip(c.AuthSource)
	c.Port = validPort(c.Port)
	if c.Status == "" {
		c.Status = StatusUnsupported
	}
	if c.Status != StatusUnsupported && !fileTypes[c.DbType] && c.Host == "" {
		c.Host = "localhost"
		if !slices.Contains(c.Warnings, WarnURLUnparsed) && !slices.Contains(c.Warnings, WarnSocketUnsupported) {
			c.warn(WarnHostMissing)
		}
	}
	if fileTypes[c.DbType] {
		c.Host, c.Port = "", 0
	}
	if c.Name == "" {
		switch {
		case c.FilePath != "":
			c.Name = filepath.Base(c.FilePath)
		case c.Host != "":
			c.Name = c.Host
		default:
			c.Name = c.SourceType
		}
	}
	if len(c.password) > maxFieldBytes {
		c.setPassword("")
		c.PasswordUnavailable = true
		c.warn(WarnPasswordUndecrypted)
	}
	if t := c.Tunnel; t != nil {
		t.Host = clip(t.Host)
		t.Username = clip(t.Username)
		t.KeyPath = clip(t.KeyPath)
		t.Port = validPort(t.Port)
		if t.Port == 0 {
			if t.Type == "ssh" {
				t.Port = 22
			} else {
				t.Port = 1080
			}
		}
		if len(t.password) > maxFieldBytes {
			t.password = ""
		}
		if len(t.passphrase) > maxFieldBytes {
			t.passphrase = ""
		}
		t.HasPassword = t.password != ""
		t.HasPassphrase = t.passphrase != ""
		if t.Host == "" || (t.Type == "ssh" && t.Username == "") {
			c.warn(WarnTunnelIncomplete)
		}
	}
}

func clip(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxFieldBytes {
		return value[:maxFieldBytes]
	}
	return value
}

// ── import sessions ──

type session struct {
	preview Preview
	created time.Time
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*session{}
	now        = time.Now
)

func storeSession(preview Preview) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	pruneSessionsLocked()
	for len(sessions) >= maxSessions {
		var oldestID string
		var oldest time.Time
		for id, s := range sessions {
			if oldestID == "" || s.created.Before(oldest) {
				oldestID, oldest = id, s.created
			}
		}
		delete(sessions, oldestID)
	}
	sessions[preview.ImportID] = &session{preview: preview, created: now()}
}

// claimSession removes and returns a live session.
func claimSession(id string) (*session, bool) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	pruneSessionsLocked()
	s, ok := sessions[id]
	delete(sessions, id)
	return s, ok
}

func restoreSession(id string, s *session) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if len(sessions) < maxSessions {
		sessions[id] = s
	}
}

func dropSession(id string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	delete(sessions, id)
}

func pruneSessionsLocked() {
	for id, s := range sessions {
		if now().Sub(s.created) > sessionTTL {
			delete(sessions, id)
		}
	}
}

// ── handlers ──

type previewArgs struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

func previewHandler(_, argsJSON string) (string, error) {
	var args previewArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	preview, err := buildPreview(args.Source, args.Path)
	if err != nil {
		return "", err
	}
	storeSession(preview)
	return core.EncodeResult(preview)
}

// errCode builds a stable, translatable error ("db_import: <code>").
func errCode(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("db_import: %s", code)
	}
	return fmt.Errorf("db_import: %s: %w", code, cause)
}

func buildPreview(source, rawPath string) (Preview, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" || !filepath.IsAbs(path) {
		return Preview{}, errCode("path_invalid", nil)
	}
	path = filepath.Clean(path)
	var (
		result parseResult
		err    error
	)
	switch source {
	case SourceDBeaver:
		result, err = parseDBeaverPath(path)
	case SourceNavicat:
		result, err = parseNavicatPath(path)
	case SourceDataGrip:
		result, err = parseDataGripPath(path)
	case SourceWorkbench:
		result, err = parseWorkbenchPath(path)
	case SourceHeidiSQL:
		result, err = parseHeidiPath(path)
	default:
		return Preview{}, errCode("source_invalid", nil)
	}
	if err != nil {
		return Preview{}, err
	}
	if result.rows == nil {
		result.rows = []Candidate{}
	}
	return Preview{
		ImportID: uuid.NewString(),
		Source:   source,
		Path:     path,
		Rows:     result.rows,
		Warnings: result.warnings,
	}, nil
}

type discardArgs struct {
	ImportID string `json:"importId"`
}

func discardHandler(_, argsJSON string) (string, error) {
	var args discardArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	dropSession(args.ImportID)
	return core.EncodeResult(nil)
}

// ── bounded file access ──

// readBounded reads a regular file of at most maxFileBytes.
func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errCode("not_found", nil)
		}
		return nil, errCode("read_failed", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errCode("not_a_file", nil)
	}
	if info.Size() > maxFileBytes {
		return nil, errCode("file_too_large", nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errCode("read_failed", err)
	}
	defer file.Close()
	data, err := core.ReadAllLimited(file, maxFileBytes)
	if err != nil {
		return nil, errCode("file_too_large", err)
	}
	return data, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// globBounded lists files in dir whose names match pattern, sorted, capped.
func globBounded(dir, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		if isFile(match) {
			files = append(files, match)
		}
	}
	sort.Strings(files)
	if len(files) > maxSourceFiles {
		files = files[:maxSourceFiles]
	}
	return files
}
