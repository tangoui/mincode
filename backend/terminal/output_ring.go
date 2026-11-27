package terminal

import (
	"strings"
	"unicode/utf8"
)

const (
	controlOutputBytes = 1 << 20
	controlOutputLines = 5000
)

type outputRing struct {
	buffer                []byte
	start, length, breaks int
	maxBytes, maxLines    int
	escape                outputEscape
}

type outputEscape uint8

const (
	outputGround outputEscape = iota
	outputEsc
	outputIntermediate
	outputCSI
	outputString
	outputStringEsc
)

func newOutputRing(maxBytes, maxLines int) outputRing {
	return outputRing{maxBytes: maxBytes, maxLines: maxLines}
}

func (r *outputRing) write(data string) {
	for _, char := range data {
		if r.visible(char) {
			var encoded [utf8.UTFMax]byte
			n := utf8.EncodeRune(encoded[:], char)
			for _, b := range encoded[:n] {
				r.push(b)
			}
		}
	}
	for r.length > 0 && !utf8.RuneStart(r.buffer[r.start]) {
		r.drop()
	}
}

func (r *outputRing) visible(char rune) bool {
	switch r.escape {
	case outputEsc:
		switch char {
		case '[':
			r.escape = outputCSI
		case ']', 'P', '_', '^', 'X':
			r.escape = outputString
		case '\x1b':
		default:
			if char >= ' ' && char <= '/' {
				r.escape = outputIntermediate
			} else {
				r.escape = outputGround
			}
		}
	case outputIntermediate, outputCSI:
		switch {
		case char == '\x1b':
			r.escape = outputEsc
		case char == '\x18' || char == '\x1a' || char >= '@' && char <= '~' || r.escape == outputIntermediate && char >= '0' && char <= '?':
			r.escape = outputGround
		}
	case outputString:
		switch char {
		case '\x07', '\x18', '\x1a', '\u009c':
			r.escape = outputGround
		case '\x1b':
			r.escape = outputStringEsc
		}
	case outputStringEsc:
		if char == '\\' {
			r.escape = outputGround
		} else {
			r.escape = outputString
		}
	case outputGround:
		switch char {
		case '\x1b':
			r.escape = outputEsc
		case '\u009b':
			r.escape = outputCSI
		case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
			r.escape = outputString
		default:
			return char == '\n' || char == '\t' || char >= ' ' && (char < '\x7f' || char > '\u009f')
		}
	}
	return false
}

func (r *outputRing) push(b byte) {
	if r.length == r.maxBytes {
		r.drop()
	}
	if r.length == len(r.buffer) {
		next := make([]byte, min(r.maxBytes, max(4096, 2*len(r.buffer))))
		r.copyTo(next)
		r.buffer, r.start = next, 0
	}
	r.buffer[(r.start+r.length)%len(r.buffer)] = b
	r.length++
	if b == '\n' {
		r.breaks++
	}
	for r.breaks >= r.maxLines && (r.breaks > r.maxLines || b != '\n') {
		r.drop()
	}
}

func (r *outputRing) drop() {
	if r.buffer[r.start] == '\n' {
		r.breaks--
	}
	r.start = (r.start + 1) % len(r.buffer)
	r.length--
}

func (r *outputRing) copyTo(dst []byte) {
	first := min(r.length, len(r.buffer)-r.start)
	copy(dst, r.buffer[r.start:r.start+first])
	copy(dst[first:], r.buffer[:r.length-first])
}

func (r *outputRing) read(lines int) string {
	data := make([]byte, r.length)
	r.copyTo(data)
	return tailOutputLines(string(data), lines)
}

func tailOutputLines(data string, lines int) string {
	if lines <= 0 || len(data) == 0 {
		return data
	}
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	for range lines {
		end = strings.LastIndexByte(data[:end], '\n')
		if end < 0 {
			return data
		}
	}
	return data[end+1:]
}
