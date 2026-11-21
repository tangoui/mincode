package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const maxMessageBytes = 16 * 1024 * 1024

var writeBufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// errInvalidPayload: a complete frame whose body is not JSON. The framing is
// intact, so the reader may skip it and keep the session.
var errInvalidPayload = errors.New("invalid LSP JSON payload")

func readMessage(reader *bufio.Reader) (json.RawMessage, error) {
	return readMessageLimit(reader, maxMessageBytes)
}

// readMessageLimit reads one framed message whose body is at most limit bytes
// (plugin language servers use the smaller contract cap).
func readMessageLimit(reader *bufio.Reader, limit int) (json.RawMessage, error) {
	if limit <= 0 || limit > maxMessageBytes {
		limit = maxMessageBytes
	}
	contentLength := -1
	headerBytes := 0
	for {
		lineBytes, err := reader.ReadSlice('\n')
		headerBytes += len(lineBytes)
		if headerBytes > 8192 || err == bufio.ErrBufferFull {
			return nil, fmt.Errorf("LSP headers exceed size limit")
		}
		line := string(lineBytes)
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid LSP header")
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if contentLength >= 0 {
				return nil, fmt.Errorf("duplicate LSP Content-Length")
			}
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > limit {
				return nil, fmt.Errorf("invalid LSP Content-Length")
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing LSP Content-Length")
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	if !json.Valid(payload) {
		return nil, errInvalidPayload
	}
	return payload, nil
}

// writeFrameBytes writes a pre-serialized JSON payload with Content-Length framing.
// Uses a pooled buffer to avoid per-call allocations.
func writeFrameBytes(writer io.Writer, payload []byte) error {
	buf := writeBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	_, _ = fmt.Fprintf(buf, "Content-Length: %d\r\n\r\n", len(payload))
	_, _ = buf.Write(payload)
	n, err := writer.Write(buf.Bytes())
	if err == nil && n != buf.Len() {
		err = io.ErrShortWrite
	}
	writeBufferPool.Put(buf)
	if err != nil {
		return fmt.Errorf("write LSP message: %w", err)
	}
	return nil
}

func writeMessage(writer io.Writer, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode LSP message: %w", err)
	}
	if len(payload) > maxMessageBytes {
		return fmt.Errorf("LSP message exceeds size limit")
	}
	return writeFrameBytes(writer, payload)
}
