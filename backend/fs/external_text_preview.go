package fs

import (
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

const externalTextProbeBytes = 8 * 1024

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
