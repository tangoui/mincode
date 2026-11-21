// Package debug is the Debug Adapter Protocol (DAP) client used by debugger
// contributions: it frames and exchanges DAP messages with a debug adapter
// process on behalf of the editor.
package debug

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrFrameTooLarge is returned by ReadMessage when a frame's Content-Length
// exceeds the caller's limit. The frame body is left unread.
var ErrFrameTooLarge = errors.New("dap: frame too large")

// maxHeaderBytes bounds the header section of a single frame. Header lines are
// read in bufio-buffer-sized slices and the total is checked after each one,
// so a misbehaving adapter writing a newline-less line makes ReadMessage fail
// after at most maxHeaderBytes plus one buffer, instead of buffering it all.
const maxHeaderBytes = 8 << 10

// Message is a DAP protocol message: a request, response, or event.
type Message struct {
	Seq        int64           `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command,omitempty"`
	Event      string          `json:"event,omitempty"`
	RequestSeq int64           `json:"request_seq,omitempty"`
	Success    *bool           `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// ReadMessage reads one Content-Length framed DAP message from r. Header
// names are matched case-insensitively and unknown headers are ignored.
// Frames whose body exceeds maxBytes fail with ErrFrameTooLarge before any
// body bytes are consumed.
func ReadMessage(r *bufio.Reader, maxBytes int) (Message, error) {
	length := -1
	headerBytes := 0
	for {
		raw, err := readHeaderLine(r, &headerBytes)
		if err != nil {
			return Message{}, err
		}
		line := strings.TrimRight(raw, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return Message{}, fmt.Errorf("dap: malformed header %q", line)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return Message{}, fmt.Errorf("dap: invalid Content-Length %q", value)
		}
		length = n
	}
	if length < 0 {
		return Message{}, errors.New("dap: missing Content-Length header")
	}
	if length > maxBytes {
		return Message{}, fmt.Errorf("%w: %d > %d bytes", ErrFrameTooLarge, length, maxBytes)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return Message{}, fmt.Errorf("dap: read body: %w", err)
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return Message{}, fmt.Errorf("dap: decode message: %w", err)
	}
	return m, nil
}

// readHeaderLine reads one header line (including its '\n'), adding its size
// to *total and failing once *total exceeds maxHeaderBytes. It never holds
// more than one bufio buffer of an over-long line beyond that limit.
func readHeaderLine(r *bufio.Reader, total *int) (string, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		*total += len(chunk)
		if *total > maxHeaderBytes {
			return "", fmt.Errorf("dap: header exceeds %d bytes", maxHeaderBytes)
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return string(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return "", fmt.Errorf("dap: read header: %w", err)
		}
	}
}

// WriteMessage encodes m and writes the complete frame (header and body) to w
// in a single Write call. It does not synchronize; callers sharing w must
// serialize calls themselves.
func WriteMessage(w io.Writer, m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("dap: encode message: %w", err)
	}
	var frame bytes.Buffer
	frame.Grow(len(body) + 32)
	fmt.Fprintf(&frame, "Content-Length: %d\r\n\r\n", len(body))
	frame.Write(body)
	if _, err := w.Write(frame.Bytes()); err != nil {
		return fmt.Errorf("dap: write frame: %w", err)
	}
	return nil
}
