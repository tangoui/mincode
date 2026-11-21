package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

type recordingStreamConn struct {
	mu     sync.Mutex
	sent   [][]byte
	fail   bool
	closed bool
}

func (c *recordingStreamConn) Send(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("closed")
	}
	c.sent = append(c.sent, data)
	return nil
}
func (c *recordingStreamConn) SendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Send(data)
}
func (c *recordingStreamConn) Receive() ([]byte, error) { return nil, errors.New("unused") }
func (c *recordingStreamConn) ReceiveJSON(any) error    { return errors.New("unused") }
func (c *recordingStreamConn) Close() error             { c.closed = true; return nil }
func (c *recordingStreamConn) Context() context.Context { return context.Background() }

func TestEmitAcpEventSharesEncodedFrame(t *testing.T) {
	first, second, broken := &recordingStreamConn{}, &recordingStreamConn{}, &recordingStreamConn{fail: true}
	acpStreamSubs.Lock()
	previous := acpStreamSubs.conns
	acpStreamSubs.conns = map[core.StreamConn]struct{}{first: {}, second: {}, broken: {}}
	acpStreamSubs.Unlock()
	t.Cleanup(func() {
		acpStreamSubs.Lock()
		acpStreamSubs.conns = previous
		acpStreamSubs.Unlock()
	})

	payload := map[string]any{"sessionId": "s1", "update": map[string]string{"sessionUpdate": "agent_message_chunk"}}
	emitAcpEvent("acp:session-update", payload)

	want, err := json.Marshal(map[string]any{"event": "acp:session-update", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*recordingStreamConn{first, second} {
		if len(conn.sent) != 1 || !bytes.Equal(conn.sent[0], want) {
			t.Fatalf("frame = %q, want %q", conn.sent, want)
		}
	}
	if &first.sent[0][0] != &second.sent[0][0] {
		t.Fatal("event re-encoded per subscriber")
	}
	acpStreamSubs.Lock()
	_, stillSubscribed := acpStreamSubs.conns[broken]
	acpStreamSubs.Unlock()
	if stillSubscribed || !broken.closed {
		t.Fatal("failed subscriber not removed")
	}
}
