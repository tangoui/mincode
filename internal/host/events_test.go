package host

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLargeEventGoesByReference(t *testing.T) {
	h, _, page := newTestHost()
	big := strings.Repeat("x", 128*1024)
	h.Emit("app:big", map[string]any{"blob": big})

	msgs := waitMsgs(t, page, 1)
	if !strings.HasPrefix(msgs[0], msgRef) || strings.Contains(msgs[0], big) {
		t.Fatalf("large event not sent by reference: %.80q", msgs[0])
	}
	id := strings.TrimPrefix(msgs[0], msgRef)
	if len(id) != 32 {
		t.Fatalf("ref id %q is not 128-bit hex", id)
	}
	// 页面经 eventRefPath 取回完整 payload（走 Handler 路由，验证已挂载）。
	get := func(p Page) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, eventRefPath+"?id="+id, nil)
		if p != nil {
			r = r.WithContext(WithPage(r.Context(), p))
		}
		w := httptest.NewRecorder()
		h.Handler(nil, "").ServeHTTP(w, r)
		return w
	}
	if w := get(&fakePage{}); w.Code != http.StatusNotFound {
		t.Fatalf("other page fetched the ref: %d", w.Code)
	}
	w := get(page)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), big) {
		t.Fatalf("event-ref fetch failed: %d len=%d", w.Code, w.Body.Len())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", w.Header().Get("Cache-Control"))
	}
	// 取回后引用应被移除。
	if w := get(page); w.Code != http.StatusNotFound {
		t.Fatalf("ref not removed after fetch: %d", w.Code)
	}
}

// 内联上限处的边界：恰好 inlineEventMax 字节内联，多 1 字节走引用。
func TestInlineEventBoundary(t *testing.T) {
	h, _, page := newTestHost()
	overhead := len(`{"data":"","name":"b"}`)
	h.Emit("b", strings.Repeat("y", inlineEventMax-overhead))
	h.Emit("b", strings.Repeat("y", inlineEventMax-overhead+1))
	msgs := waitMsgs(t, page, 2)
	if !strings.HasPrefix(msgs[0], msgInline) || len(msgs[0]) != len(msgInline)+inlineEventMax {
		t.Fatalf("event of exactly inlineEventMax bytes not inlined: prefix %.8q len %d", msgs[0], len(msgs[0]))
	}
	if !strings.HasPrefix(msgs[1], msgRef) {
		t.Fatalf("event over inlineEventMax not by reference: %.8q", msgs[1])
	}
}

// 多个窗口各拿到自己的引用，一个窗口取走不影响另一个。
func TestLargeEventRefPerPage(t *testing.T) {
	h, _, a := newTestHost()
	b := &fakePage{}
	h.AddPage(b)
	big := strings.Repeat("z", 32*1024)
	h.Emit("app:big", big)
	ma, mb := waitMsgs(t, a, 1)[0], waitMsgs(t, b, 1)[0]
	if ma == mb {
		t.Fatal("pages share one reference id")
	}
	for _, c := range []struct {
		p   Page
		msg string
	}{{a, ma}, {b, mb}} {
		data, ok := h.refs.take(strings.TrimPrefix(c.msg, msgRef), c.p)
		if !ok || !strings.Contains(data, big) {
			t.Fatalf("page could not take its own reference")
		}
	}
}

// 未取走的引用过期即失效；注销页面时清掉它的引用。
func TestRefStoreExpiryAndDropPage(t *testing.T) {
	s := newRefStore()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	p, q := &fakePage{}, &fakePage{}
	id1, _ := s.put(p, "one")
	id2, _ := s.put(q, "two")
	now = now.Add(refTTL)
	if _, ok := s.take(id1, p); ok {
		t.Fatal("expired reference was taken")
	}
	// 下一次 put 时清扫：过期项连同字节计数一起移除。
	s.put(p, "three")
	if _, ok := s.items[id2]; ok || s.bytes != len("three") {
		t.Fatalf("sweep left items=%d bytes=%d", len(s.items), s.bytes)
	}
	s.dropPage(p)
	if len(s.items) != 0 || s.bytes != 0 {
		t.Fatalf("dropPage left items=%d bytes=%d", len(s.items), s.bytes)
	}
}

// 暂存满了退回内联，不丢事件。
func TestRefStoreFullFallsBackInline(t *testing.T) {
	h, _, page := newTestHost()
	h.refs.bytes = refStoreMax
	h.Emit("app:big", strings.Repeat("w", 16*1024))
	if msg := waitMsgs(t, page, 1)[0]; !strings.HasPrefix(msg, msgInline) {
		t.Fatalf("full store did not fall back to inline: %.8q", msg)
	}
}
