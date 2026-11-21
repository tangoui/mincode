package host

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// 事件下行（Go → 页面）经 tango Surface.Send 投递，消息格式：
//
//	"wev:"  + 事件 JSON     小事件内联
//	"wref:" + 32 位十六进制  大事件按引用：载荷暂存在 refStore，页面 GET eventRefPath?id= 取走
//
// 页面侧由 vue-app/src/services/desktop-events.ts 经运行时 onRaw 接收，交给顺序分发器。
// 不再把事件拼成 JS 源码交给 Eval：Send 与 tango 其他下行消息共用有序队列并合批，
// 页面只做 JSON.parse。大事件不走 evaluateJavaScript——载荷超过约 8–16 KiB 时
// WebKit 改用共享内存传输且宿主进程持续保留这些区域（此前所用桌面框架实测：100 个/秒的
// 1 MB 事件涨到 11.5 GB），改走资源请求则没有这个问题。
const (
	msgInline = "wev:"
	msgRef    = "wref:"

	eventRefPath = "/_mincode/event-ref"

	// inlineEventMax 是允许内联的事件 JSON 最大字节数，取 WebKit 未出现保留的最大实测值。
	inlineEventMax = 8 << 10
	// refTTL 是未被取走的载荷最长保留时间（页面在投递与取走之间刷新或关闭时兜底）。
	refTTL = 30 * time.Second
	// refStoreMax 限制暂存总字节；超出时该事件退回内联，宁可多付传输代价也不丢事件。
	refStoreMax = 64 << 20
)

// eventMessage 把事件 JSON 转成发给页面 p 的下行消息；大事件尽量改为引用。
// 每个页面各存一份引用：取走是一次性的，多窗口共用一个 id 会让后取的窗口丢事件。
func (h *Host) eventMessage(p Page, payload string) string {
	if len(payload) > inlineEventMax {
		if id, ok := h.refs.put(p, payload); ok {
			return msgRef + id
		}
	}
	return msgInline + payload
}

// handleEventRef 服务 GET eventRefPath?id=…：一次性交付暂存的大事件载荷，
// 只交给投递时的页面。
func (h *Host) handleEventRef(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, ok := h.refs.take(r.URL.Query().Get("id"), PageFrom(r.Context()))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(data))
}

type parkedEvent struct {
	page    Page
	data    string
	created time.Time
}

// refStore 暂存待页面取走的大事件载荷。过期项在 put 时顺带清理
// （最多每 refTTL/4 扫一次），无需后台 goroutine。
type refStore struct {
	mu        sync.Mutex
	items     map[string]parkedEvent
	bytes     int
	lastSweep time.Time
	now       func() time.Time // 测试可替换
}

func newRefStore() *refStore {
	return &refStore{items: map[string]parkedEvent{}, now: time.Now}
}

// put 暂存 data 并返回不可猜测的 id；存储已满或取随机数失败时 ok 为 false，调用方改为内联。
func (s *refStore) put(p Page, data string) (id string, ok bool) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", false
	}
	id = hex.EncodeToString(buf[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Sub(s.lastSweep) >= refTTL/4 {
		s.sweepLocked(now)
	}
	if s.bytes+len(data) > refStoreMax {
		return "", false
	}
	s.items[id] = parkedEvent{page: p, data: data, created: now}
	s.bytes += len(data)
	return id, true
}

// take 取走 id 对应载荷（只能取一次）。p 非 nil 时须与投递页面一致；
// 请求未带页面标识（p 为 nil）时只凭 128 位随机 id 放行。
func (s *refStore) take(id string, p Page) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok || s.now().Sub(item.created) >= refTTL {
		return "", false
	}
	if p != nil && item.page != p {
		return "", false
	}
	delete(s.items, id)
	s.bytes -= len(item.data)
	return item.data, true
}

// dropPage 丢弃已注销页面的暂存载荷。
func (s *refStore) dropPage(p Page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.items {
		if item.page == p {
			delete(s.items, id)
			s.bytes -= len(item.data)
		}
	}
}

func (s *refStore) sweepLocked(now time.Time) {
	s.lastSweep = now
	for id, item := range s.items {
		if now.Sub(item.created) >= refTTL {
			delete(s.items, id)
			s.bytes -= len(item.data)
		}
	}
}
