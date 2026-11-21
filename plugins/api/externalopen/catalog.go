// Package externalopen defines the closed vocabulary of system open handlers.
// It is dependency-free and shared with the plugin CLI by contract generation.
package externalopen

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Type struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Catalog returns independent values: these are candidates, not default apps.
func Catalog() []Type {
	return []Type{
		{"magnet", "scheme", "BitTorrent magnet", "magnet"}, {"ed2k", "scheme", "eDonkey links", "ed2k"},
		{"thunder", "scheme", "Thunder links", "thunder"}, {"ftp", "scheme", "FTP", "ftp"}, {"ftps", "scheme", "FTPS", "ftps"},
		{"sftp", "scheme", "SFTP", "sftp"}, {"ssh", "scheme", "SSH", "ssh"}, {"webcal", "scheme", "Web calendars", "webcal"},
		{"webcals", "scheme", "Secure web calendars", "webcals"}, {"mailto", "scheme", "Email links", "mailto"},
		{"torrent", "file", "BitTorrent seed", "torrent"}, {"metalink", "file", "Metalink", "metalink"}, {"meta4", "file", "Metalink 4", "meta4"},
		{"ics", "file", "Calendar", "ics"}, {"vcf", "file", "Contacts", "vcf"}, {"epub", "file", "EPUB book", "epub"},
		{"mobi", "file", "MOBI book", "mobi"}, {"cbz", "file", "Comic ZIP", "cbz"}, {"cbr", "file", "Comic RAR", "cbr"},
	}
}
func Lookup(id string) (Type, bool) {
	for _, t := range Catalog() {
		if t.ID == id {
			return t, true
		}
	}
	return Type{}, false
}
func FileType(path string) (Type, bool) {
	t, ok := Lookup(strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")))
	return t, ok && t.Kind == "file"
}

// URI validates only routing syntax. Plugins must validate their own protocol
// before performing any networking; URI values never become shell commands.
func URI(raw string) (Type, error) {
	if raw == "" || len(raw) > 4096 || !utf8.ValidString(raw) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return Type{}, errors.New("invalid external link")
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || strings.IndexFunc(decoded, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return Type{}, errors.New("invalid escaped link")
	}
	// ed2k uses vertical bars in its authority, which net/url correctly rejects
	// for HTTP but which are part of this protocol’s native syntax.
	if strings.HasPrefix(strings.ToLower(raw), "ed2k://|") && strings.HasSuffix(raw, "|/") {
		t, _ := Lookup("ed2k")
		return t, nil
	}
	u, e := url.Parse(raw)
	if e != nil {
		return Type{}, errors.New("invalid external link")
	}
	t, ok := Lookup(strings.ToLower(u.Scheme))
	if !ok || t.Kind != "scheme" {
		return Type{}, errors.New("unsupported link type")
	}
	if len(raw) <= len(u.Scheme)+1 {
		return Type{}, errors.New("empty external link")
	}
	return t, nil
}
