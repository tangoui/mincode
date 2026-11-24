package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Match acp-go-sdk's receive scanner limit. Larger lines are streamed unchanged.
const maxScopedElicitationLineBytes = 10 * 1024 * 1024

// scopeElicitationParams preserves form routing fields that the SDK does not decode.
func scopeElicitationParams(r io.Reader) io.Reader {
	return &elicitationScopeReader{reader: bufio.NewReader(r)}
}

type elicitationScopeReader struct {
	reader      *bufio.Reader
	line        []byte
	err         error
	passthrough bool
}

func (r *elicitationScopeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.line) == 0 && r.err == nil {
		r.readLine()
	}
	if len(r.line) == 0 {
		return 0, r.err
	}
	n := copy(p, r.line)
	r.line = r.line[n:]
	return n, nil
}

func (r *elicitationScopeReader) readLine() {
	r.line = nil
	for {
		part, err := r.reader.ReadSlice('\n')
		r.line = append(r.line, part...)
		if err != bufio.ErrBufferFull {
			if !r.passthrough {
				r.line = scopeElicitationLine(r.line)
			}
			r.passthrough, r.err = false, err
			return
		}
		if r.passthrough || len(r.line) >= maxScopedElicitationLineBytes {
			r.passthrough = true
			return
		}
	}
}

func scopeElicitationLine(line []byte) []byte {
	var header struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &header) != nil || header.Method != acpsdk.ClientMethodElicitationCreate {
		return line
	}

	var message, params map[string]json.RawMessage
	if json.Unmarshal(line, &message) != nil || json.Unmarshal(message["params"], &params) != nil {
		return line
	}
	var meta map[string]json.RawMessage
	if raw, ok := params["_meta"]; ok && json.Unmarshal(raw, &meta) != nil {
		return line
	}
	if meta == nil {
		meta = make(map[string]json.RawMessage)
	}
	copied := false
	for field, key := range map[string]string{"sessionId": elicitationSessionMetaKey, "toolCallId": elicitationToolCallMetaKey} {
		if raw, ok := params[field]; ok {
			meta[key] = raw
			copied = true
		}
	}
	if !copied {
		return line
	}

	// All values came from the successfully decoded JSON, so re-encoding is safe.
	params["_meta"], _ = json.Marshal(meta)
	message["params"], _ = json.Marshal(params)
	scoped, _ := json.Marshal(message)
	return append(scoped, line[len(bytes.TrimRight(line, "\r\n")):]...)
}
