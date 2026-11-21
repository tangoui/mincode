package agentprogress

import (
	"context"
	"errors"
	"testing"

	"mincode/app/backend/core"
)

type failingProgressStreamConn struct{ closed bool }

func (c *failingProgressStreamConn) Send([]byte) error        { return errors.New("send failed") }
func (c *failingProgressStreamConn) SendJSON(any) error       { return errors.New("send failed") }
func (c *failingProgressStreamConn) Receive() ([]byte, error) { return nil, errors.New("closed") }
func (c *failingProgressStreamConn) ReceiveJSON(any) error    { return errors.New("closed") }
func (c *failingProgressStreamConn) Close() error             { c.closed = true; return nil }
func (c *failingProgressStreamConn) Context() context.Context { return context.Background() }

var _ core.StreamConn = (*failingProgressStreamConn)(nil)

func TestEmitProgressRemovesFailedSubscriber(t *testing.T) {
	conn := &failingProgressStreamConn{}
	progressStreamSubs.Lock()
	progressStreamSubs.conns = map[core.StreamConn]struct{}{conn: {}}
	progressStreamSubs.Unlock()

	emitProgress(map[string]string{"runId": "run-1"})

	progressStreamSubs.Lock()
	_, retained := progressStreamSubs.conns[conn]
	progressStreamSubs.conns = make(map[core.StreamConn]struct{})
	progressStreamSubs.Unlock()
	if retained || !conn.closed {
		t.Fatalf("failed subscriber retained=%v closed=%v", retained, conn.closed)
	}
}
