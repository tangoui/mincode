package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const externalTextChunkBytes = 512 * 1024
const externalTextProbeBytes = 8 * 1024

type externalTextChunk struct {
	Text        string `json:"text"`
	StartOffset int64  `json:"startOffset"`
	NextOffset  int64  `json:"nextOffset"`
	TotalBytes  int64  `json:"totalBytes"`
	EOF         bool   `json:"eof"`
	Version     string `json:"version"`
}

func init() {
	core.RegisterCtx("local_fs_read_external_text_chunk", handleReadExternalTextChunk)
}

// completeUTF8Prefix only removes a truncated final code point. Invalid bytes
// remain in the buffer so utf8.Valid can reject them, rather than replacing them.
func completeUTF8Prefix(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	start := len(data) - 1
	for start > 0 && !utf8.RuneStart(data[start]) && len(data)-start < utf8.UTFMax {
		start--
	}
	if utf8.RuneStart(data[start]) && !utf8.FullRune(data[start:]) {
		return data[:start]
	}
	return data
}

func externalTextProbe(file *os.File, info os.FileInfo) ([]byte, bool, error) {
	// A few extra bytes allow a code point straddling the sampling boundary.
	data := make([]byte, externalTextProbeBytes+utf8.UTFMax-1)
	n, err := file.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	data = data[:n]
	if int64(n) < info.Size() {
		data = completeUTF8Prefix(data)
	}
	return data, utf8.Valid(data) && !sampleLooksLikeBinaryForPreview(data), nil
}

func externalTextPreviewAvailable(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("not_a_file")
	}
	_, text, err := externalTextProbe(file, info)
	return text, err
}

// A best-effort change token, not a locked content snapshot: include the prefix
// so replacing a file with the same size/time but a different header is noticed.
func externalTextVersion(info os.FileInfo, probe []byte) string {
	digest := sha256.Sum256(probe)
	return fmt.Sprintf("%x-%x-%x", info.Size(), info.ModTime().UnixNano(), digest[:])
}

func handleReadExternalTextChunk(ctx context.Context, _ string, argsJSON string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args struct {
		Path            string `json:"path"`
		Offset          int64  `json:"offset"`
		ExpectedVersion string `json:"expectedVersion"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Offset < 0 {
		return "", fmt.Errorf("invalid_offset")
	}
	path, original, err := externalRegularFile(args.Path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not_a_file")
	}
	if !os.SameFile(original, info) {
		return "", fmt.Errorf("file_changed")
	}
	probe, isText, err := externalTextProbe(file, info)
	if err != nil {
		return "", err
	}
	version := externalTextVersion(info, probe)
	// Check the version before offset/type errors when a previously viewed file changes.
	if args.ExpectedVersion != "" && args.ExpectedVersion != version {
		return "", fmt.Errorf("file_changed")
	}
	if !isText {
		return "", fmt.Errorf("not_a_text_file")
	}
	if args.Offset > info.Size() {
		return "", fmt.Errorf("invalid_offset")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	length := min(int64(externalTextChunkBytes), info.Size()-args.Offset)
	data := make([]byte, int(length))
	n, err := file.ReadAt(data, args.Offset)
	if err != nil && err != io.EOF {
		return "", err
	}
	if int64(n) != length {
		return "", fmt.Errorf("file_changed")
	}
	data = data[:n]
	if args.Offset+int64(n) < info.Size() {
		data = completeUTF8Prefix(data)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("not_a_text_file")
	}
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	current, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file_changed: %w", err)
	}
	if !os.SameFile(info, current) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return "", fmt.Errorf("file_changed")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	next := args.Offset + int64(len(data))
	return core.EncodeResult(externalTextChunk{Text: string(data), StartOffset: args.Offset, NextOffset: next, TotalBytes: info.Size(), EOF: next == info.Size(), Version: version})
}
