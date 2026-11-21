package debug

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestWriteThenReadRoundTrip(t *testing.T) {
	in := Message{
		Seq:       1,
		Type:      "request",
		Command:   "initialize",
		Arguments: json.RawMessage(`{"adapterID":"x"}`),
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, in); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: ") {
		t.Fatalf("frame missing Content-Length header: %q", buf.String())
	}

	out, err := ReadMessage(bufio.NewReader(&buf), 4<<20)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if out.Seq != 1 || out.Type != "request" || out.Command != "initialize" {
		t.Fatalf("unexpected message: %+v", out)
	}
	if out.Success != nil || out.Body != nil || out.Event != "" {
		t.Fatalf("unexpected optional fields set: %+v", out)
	}
	var args map[string]string
	if err := json.Unmarshal(out.Arguments, &args); err != nil {
		t.Fatalf("unmarshal arguments: %v", err)
	}
	if args["adapterID"] != "x" {
		t.Fatalf("arguments = %s", out.Arguments)
	}
	if buf.Len() != 0 {
		t.Fatalf("reader left %d unread bytes", buf.Len())
	}
}

func TestReadMessageRejectsOversize(t *testing.T) {
	body := `{"seq":1,"type":"event"}`
	r := bufio.NewReader(strings.NewReader("Content-Length: 5000000\r\n\r\n" + body))

	_, err := ReadMessage(r, 4<<20)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != body {
		t.Fatalf("body was consumed; remaining = %q", rest)
	}
}

func TestReadMessageRejectsMissingLength(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Content-Type: x\r\n\r\n{}"))
	if _, err := ReadMessage(r, 4<<20); err == nil {
		t.Fatal("expected error for frame without Content-Length")
	}
}

func TestReadMessageIgnoresExtraHeaders(t *testing.T) {
	body := `{"seq":7,"type":"event","event":"stopped","body":{"reason":"breakpoint"}}`
	frame := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"content-length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	m, err := ReadMessage(bufio.NewReader(strings.NewReader(frame)), 4<<20)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if m.Seq != 7 || m.Type != "event" || m.Event != "stopped" {
		t.Fatalf("unexpected message: %+v", m)
	}
	if string(m.Body) != `{"reason":"breakpoint"}` {
		t.Fatalf("body = %s", m.Body)
	}
}

func TestReadMessageRejectsInvalidLength(t *testing.T) {
	for _, h := range []string{"Content-Length: abc", "Content-Length: -1"} {
		r := bufio.NewReader(strings.NewReader(h + "\r\n\r\n{}"))
		if _, err := ReadMessage(r, 4<<20); err == nil {
			t.Fatalf("expected error for header %q", h)
		}
	}
}

// countingReader yields n bytes of 'a' (no newline) and counts what was read.
type countingReader struct{ left, read int }

func (c *countingReader) Read(b []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := min(len(b), c.left)
	for i := range b[:n] {
		b[i] = 'a'
	}
	c.left -= n
	c.read += n
	return n, nil
}

func TestReadMessageBoundsNewlinelessHeaderLine(t *testing.T) {
	src := &countingReader{left: 64 << 20}
	_, err := ReadMessage(bufio.NewReader(src), 4<<20)
	if err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("err = %v, want header exceeds", err)
	}
	// The reader must stop shortly after the header limit instead of
	// buffering the whole newline-less line.
	if src.read > 4*maxHeaderBytes {
		t.Fatalf("read %d bytes of a newline-less header, want at most %d", src.read, 4*maxHeaderBytes)
	}
}
