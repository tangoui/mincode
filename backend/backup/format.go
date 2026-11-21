// Package backup implements the MinCode encrypted backup file format.
//
// A backup file is a fixed header followed by an AES-256-GCM ciphertext:
//
//	magic    "MCBAK1\n"  7 bytes
//	version  1           1 byte
//	time     uint32      Argon2id iterations (big endian)
//	memory   uint32      Argon2id memory in KiB (big endian)
//	threads  uint8       Argon2id parallelism
//	salt     16 bytes
//	nonce    12 bytes
//	ciphertext ...
//
// The whole header is GCM additional data, so any change to it fails
// authentication. The plaintext is a ZIP archive whose first file is
// manifest.json; the remaining files are the backup entries.
package backup

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// MaxBackupBytes bounds both the encrypted file size and the total
// decompressed size of its contents.
const MaxBackupBytes = 256 << 20

// MinPasswordRunes is the minimum backup password length in characters.
const MinPasswordRunes = 12

var (
	ErrWrongPassword    = errors.New("备份密码错误或备份已损坏")
	ErrPasswordTooShort = errors.New("备份密码至少需要 12 个字符")
	ErrCorrupt          = errors.New("备份内容校验失败")
	ErrInvalidFormat    = errors.New("不是有效的 MinCode 备份文件")
	ErrBackupTooLarge   = errors.New("备份超过 256 MiB 上限")
)

// Manifest describes a backup. It is stored as manifest.json inside the
// encrypted archive.
type Manifest struct {
	Format          string           `json:"format"`
	Version         int              `json:"version"`
	CreatedAt       time.Time        `json:"createdAt"`
	AppVersion      string           `json:"appVersion"`
	Device          string           `json:"device"`
	IncludesSecrets bool             `json:"includesSecrets"`
	Sections        []SectionSummary `json:"sections"`
	ContentSHA256   string           `json:"contentSha256"`
}

// SectionSummary summarizes one logical part of a backup.
type SectionSummary struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Items  int    `json:"items"`
	Bytes  int64  `json:"bytes"`
	Plugin string `json:"plugin,omitempty"`
}

// Entry is one file inside a backup archive.
type Entry struct {
	Path string
	Data []byte
}

const (
	magic           = "MCBAK1\n"
	fileVersion     = 1
	manifestName    = "manifest.json"
	manifestFormat  = "mincode-backup"
	manifestVersion = 1

	saltLen  = 16
	nonceLen = 12
	keyLen   = 32

	versionOffset = len(magic)
	timeOffset    = versionOffset + 1
	memoryOffset  = timeOffset + 4
	threadsOffset = memoryOffset + 4
	saltOffset    = threadsOffset + 1
	nonceOffset   = saltOffset + saltLen
	headerLen     = nonceOffset + nonceLen
)

type kdfParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// Bounds for KDF parameters accepted from a file header, so a crafted file
// cannot force unbounded CPU or memory use.
const (
	minKDFTime      = 1
	maxKDFTime      = 10
	minKDFMemoryKiB = 8 * 1024
	maxKDFMemoryKiB = 1024 * 1024
	minKDFThreads   = 1
	maxKDFThreads   = 16
)

var defaultArgon2Params = kdfParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// argon2Params is used when sealing. Tests lower it; Open always uses the
// parameters recorded in the file header.
var argon2Params = defaultArgon2Params

func (p kdfParams) valid() bool {
	return p.Time >= minKDFTime && p.Time <= maxKDFTime &&
		p.MemoryKiB >= minKDFMemoryKiB && p.MemoryKiB <= maxKDFMemoryKiB &&
		p.Threads >= minKDFThreads && p.Threads <= maxKDFThreads
}

// ValidatePassword reports whether password is long enough to protect a
// backup. Length is counted in characters (runes), not bytes.
func ValidatePassword(password []byte) error {
	if utf8.RuneCount(password) < MinPasswordRunes {
		return ErrPasswordTooShort
	}
	return nil
}

// Seal writes an encrypted backup of entries to w. Format, Version and
// ContentSHA256 of manifest are filled in by Seal. Nothing is written to w
// unless the whole backup is built successfully.
func Seal(w io.Writer, password []byte, manifest Manifest, entries []Entry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.Path == manifestName {
			return fmt.Errorf("seal backup: 条目名 %q 为保留名称", e.Path)
		}
		if !validEntryPath(e.Path) {
			return fmt.Errorf("seal backup: 非法条目路径 %q", e.Path)
		}
		if _, dup := seen[e.Path]; dup {
			return fmt.Errorf("seal backup: 条目路径重复 %q", e.Path)
		}
		seen[e.Path] = struct{}{}
	}
	manifest.ContentSHA256 = ContentHash(entries)
	return sealRaw(w, password, manifest, entries)
}

// sealRaw seals manifest as given (apart from Format/Version), without
// validating entries or recomputing ContentSHA256.
func sealRaw(w io.Writer, password []byte, manifest Manifest, entries []Entry) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	manifest.Format = manifestFormat
	manifest.Version = manifestVersion
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("seal backup: encode manifest: %w", err)
	}

	total := int64(len(manifestData))
	for _, e := range entries {
		total += int64(len(e.Data))
		if total > MaxBackupBytes {
			return ErrBackupTooLarge
		}
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	files := append([]Entry{{Path: manifestName, Data: manifestData}}, entries...)
	for _, f := range files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate})
		if err != nil {
			return fmt.Errorf("seal backup: add %q: %w", f.Path, err)
		}
		if _, err := fw.Write(f.Data); err != nil {
			return fmt.Errorf("seal backup: write %q: %w", f.Path, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("seal backup: finish archive: %w", err)
	}
	return encrypt(w, password, zbuf.Bytes())
}

// encrypt writes header and GCM ciphertext of plaintext to w using the
// current argon2Params.
func encrypt(w io.Writer, password []byte, plaintext []byte) error {
	if headerLen+len(plaintext)+16 > MaxBackupBytes {
		return ErrBackupTooLarge
	}
	params := argon2Params
	header := make([]byte, headerLen)
	copy(header, magic)
	header[versionOffset] = fileVersion
	binary.BigEndian.PutUint32(header[timeOffset:], params.Time)
	binary.BigEndian.PutUint32(header[memoryOffset:], params.MemoryKiB)
	header[threadsOffset] = params.Threads
	if _, err := rand.Read(header[saltOffset:headerLen]); err != nil {
		return fmt.Errorf("seal backup: random salt/nonce: %w", err)
	}

	gcm, err := newGCM(password, header[saltOffset:nonceOffset], params)
	if err != nil {
		return fmt.Errorf("seal backup: %w", err)
	}
	out := gcm.Seal(header, header[nonceOffset:headerLen], plaintext, header)
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("seal backup: write: %w", err)
	}
	return nil
}

func newGCM(password, salt []byte, p kdfParams) (cipher.AEAD, error) {
	key := argon2.IDKey(password, salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return gcm, nil
}

// Open reads, decrypts and verifies a backup. Entries are returned in
// archive order, without manifest.json. On any error the returned entries
// are nil.
func Open(r io.Reader, password []byte) (Manifest, []Entry, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Manifest{}, nil, fmt.Errorf("%w: 文件过短", ErrInvalidFormat)
		}
		return Manifest{}, nil, fmt.Errorf("open backup: read header: %w", err)
	}
	if string(header[:len(magic)]) != magic {
		return Manifest{}, nil, ErrInvalidFormat
	}
	if header[versionOffset] != fileVersion {
		return Manifest{}, nil, fmt.Errorf("%w: 不支持的版本 %d", ErrInvalidFormat, header[versionOffset])
	}
	params := kdfParams{
		Time:      binary.BigEndian.Uint32(header[timeOffset:]),
		MemoryKiB: binary.BigEndian.Uint32(header[memoryOffset:]),
		Threads:   header[threadsOffset],
	}
	if !params.valid() {
		return Manifest{}, nil, fmt.Errorf("%w: 密钥派生参数超出范围", ErrInvalidFormat)
	}

	body, err := io.ReadAll(io.LimitReader(r, int64(MaxBackupBytes-headerLen+1)))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("open backup: read: %w", err)
	}
	if headerLen+len(body) > MaxBackupBytes {
		return Manifest{}, nil, ErrBackupTooLarge
	}

	gcm, err := newGCM(password, header[saltOffset:nonceOffset], params)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("open backup: %w", err)
	}
	plaintext, err := gcm.Open(nil, header[nonceOffset:headerLen], body, header)
	if err != nil {
		return Manifest{}, nil, ErrWrongPassword
	}

	manifest, entries, err := readArchive(plaintext)
	if err != nil {
		return Manifest{}, nil, err
	}
	return manifest, entries, nil
}

func readArchive(plaintext []byte) (Manifest, []Entry, error) {
	zr, err := zip.NewReader(bytes.NewReader(plaintext), int64(len(plaintext)))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: 无法读取归档: %v", ErrCorrupt, err)
	}
	if len(zr.File) == 0 || zr.File[0].Name != manifestName {
		return Manifest{}, nil, fmt.Errorf("%w: 归档首项不是 %s", ErrCorrupt, manifestName)
	}

	var (
		manifest  Manifest
		entries   = make([]Entry, 0, len(zr.File)-1)
		seen      = make(map[string]struct{}, len(zr.File))
		remaining = int64(MaxBackupBytes)
	)
	for i, f := range zr.File {
		if i > 0 && (f.Name == manifestName || !validEntryPath(f.Name)) {
			return Manifest{}, nil, fmt.Errorf("%w: 非法条目路径 %q", ErrCorrupt, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return Manifest{}, nil, fmt.Errorf("%w: 条目路径重复 %q", ErrCorrupt, f.Name)
		}
		seen[f.Name] = struct{}{}
		if f.UncompressedSize64 > uint64(remaining) {
			return Manifest{}, nil, ErrBackupTooLarge
		}
		data, err := readZipFile(f, remaining)
		if err != nil {
			return Manifest{}, nil, err
		}
		remaining -= int64(len(data))
		if i == 0 {
			if err := json.Unmarshal(data, &manifest); err != nil {
				return Manifest{}, nil, fmt.Errorf("%w: manifest 解析失败: %v", ErrCorrupt, err)
			}
			continue
		}
		entries = append(entries, Entry{Path: f.Name, Data: data})
	}

	if manifest.Format != manifestFormat || manifest.Version != manifestVersion {
		return Manifest{}, nil, fmt.Errorf("%w: 不支持的备份格式 %q 版本 %d", ErrInvalidFormat, manifest.Format, manifest.Version)
	}
	if manifest.ContentSHA256 != ContentHash(entries) {
		return Manifest{}, nil, ErrCorrupt
	}
	return manifest, entries, nil
}

// readZipFile reads f, failing if it decompresses to more than limit bytes.
// The zip reader verifies the CRC-32 when the file is fully read.
func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: 无法读取条目 %q: %v", ErrCorrupt, f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 无法读取条目 %q: %v", ErrCorrupt, f.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, ErrBackupTooLarge
	}
	return data, nil
}

// validEntryPath accepts clean, relative, slash-separated UTF-8 paths whose
// segments are neither empty, "." nor "..", with no NUL, backslash or drive
// letter. Like plugin file storage (validPluginStoragePath), dots inside a
// name ("a..b") and other control characters are allowed.
func validEntryPath(name string) bool {
	if name == "" || !utf8.ValidString(name) ||
		strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, "\\\x00") ||
		path.Clean(name) != name {
		return false
	}
	if len(name) >= 2 && name[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// ContentHash is the lowercase hex content hash Seal records as
// Manifest.ContentSHA256. It is independent of entry order: entries are sorted
// by path and each contributes path NUL len NUL data.
func ContentHash(entries []Entry) string {
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, e := range sorted {
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(len(e.Data))))
		h.Write([]byte{0})
		h.Write(e.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
