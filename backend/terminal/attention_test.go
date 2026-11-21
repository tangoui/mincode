package terminal

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

type attentionHarness struct {
	tracker *attentionTracker
	clock   time.Time
	events  []terminalAttentionPayload
	ticks   int
}

func newAttentionHarness() *attentionHarness {
	h := &attentionHarness{clock: time.Unix(1_000, 0)}
	h.tracker = newAttentionTracker()
	h.tracker.now = func() time.Time { return h.clock }
	h.tracker.emit = func(payload terminalAttentionPayload) { h.events = append(h.events, payload) }
	h.tracker.startTicker = func() { h.ticks++ }
	return h
}

func (h *attentionHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *attentionHarness) kinds() []string {
	kinds := make([]string, 0, len(h.events))
	for _, event := range h.events {
		kinds = append(kinds, event.Kind+":"+event.Message)
	}
	return kinds
}

func TestAttentionBellIgnoresOSCTerminators(t *testing.T) {
	h := newAttentionHarness()
	// 设置窗口标题的 OSC 以 BEL 结束，shell 每次出提示符都会发，不能当作响铃。
	h.tracker.observeOutput("p1", "\x1b]0;~/work\x07prompt$ ")
	h.tracker.observeOutput("p1", "\x1b]2;title\x1b\\")
	h.tracker.observeOutput("p1", "\x1bP+q544e\x07")
	if len(h.events) != 0 {
		t.Fatalf("title/DCS sequences must not ring: %v", h.kinds())
	}
	h.tracker.observeOutput("p1", "done\x07")
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"bell:"}) {
		t.Fatalf("bell = %v", got)
	}
	if h.events[0].PaneID != "p1" || h.events[0].At != h.clock.UnixMilli() {
		t.Fatalf("payload = %+v", h.events[0])
	}
}

func TestAttentionNotificationsAcrossChunks(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeOutput("p1", "\x1b]9;Agent turn")
	h.tracker.observeOutput("p1", " complete\x1b")
	h.tracker.observeOutput("p1", "\\")
	h.advance(attentionSignalInterval)
	h.tracker.observeOutput("p1", "\x1b]777;notify;Build;Tests\tpassed\x07")
	h.advance(attentionSignalInterval)
	h.tracker.observeOutput("p1", "\x1b]99;i=1:d=0;Approval requested: rm -rf\x1b\\")
	want := []string{"notify:Agent turn complete", "notify:Build: Tests passed", "notify:Approval requested: rm -rf"}
	if got := h.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
}

func TestAttentionIgnoresProgressAndUnrelatedOSC(t *testing.T) {
	h := newAttentionHarness()
	for _, sequence := range []string{
		"\x1b]9;4;1;50\x07", // ConEmu/Windows Terminal 进度条
		"\x1b]9;9;C:\\\\work\x07",
		"\x1b]777;preexec\x07",
		"\x1b]8;;https://example.com\x07link\x1b]8;;\x07",
		"\x1b]133;D;0\x07",
		"\x1b[31mred\x1b[0m",
	} {
		h.tracker.observeOutput("p1", sequence)
	}
	if len(h.events) != 0 {
		t.Fatalf("unexpected signals: %v", h.kinds())
	}
}

func TestAttentionInterruptedStringAndLongPayload(t *testing.T) {
	h := newAttentionHarness()
	// 字符串被新的 CSI 打断后回到正常状态，随后的 BEL 仍然有效。
	h.tracker.observeOutput("p1", "\x1b]0;unterminated\x1b[2Jtext\x07")
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"bell:"}) {
		t.Fatalf("after interrupted string = %v", got)
	}
	h.events = nil
	h.advance(attentionSignalInterval)
	h.tracker.observeOutput("p1", "\x1b]9;"+strings.Repeat("界", 5000)+"\x07")
	// 只保留前 1024 字节（341 个“界”），再截到 200 字加省略号。
	if len(h.events) != 1 || len([]rune(h.events[0].Message)) != attentionMaxMessage+1 {
		t.Fatalf("long payload = %+v", h.events)
	}
	if !strings.HasSuffix(h.events[0].Message, "…") {
		t.Fatalf("long message not truncated: %q", h.events[0].Message)
	}
}

func TestAttentionRateLimitsSignals(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeOutput("p1", "\x07\x07")
	h.advance(time.Second)
	h.tracker.observeOutput("p1", "\x07")
	h.tracker.observeOutput("p2", "\x07")
	h.advance(attentionSignalInterval)
	h.tracker.observeOutput("p1", "\x07")
	if got := len(h.events); got != 3 {
		t.Fatalf("events = %d (%v), want p1, p2, p1", got, h.events)
	}
}

func TestAttentionQuietAfterSustainedOutput(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeInput("p1", "make\r")
	h.advance(time.Second)
	for i := 0; i < 6; i++ {
		h.tracker.observeOutput("p1", "working...\r\n")
		h.advance(time.Second)
	}
	if h.ticks != 1 {
		t.Fatalf("ticker starts = %d, want 1", h.ticks)
	}
	h.advance(attentionQuietAfter - 2*time.Second)
	if !h.tracker.sweep() || len(h.events) != 0 {
		t.Fatalf("still within the quiet window: %v", h.kinds())
	}
	h.advance(2 * time.Second)
	if h.tracker.sweep() {
		t.Fatal("no session should stay busy")
	}
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"quiet:"}) {
		t.Fatalf("quiet = %v", got)
	}
	// 只提醒一次；之后再次输出才重新计时。
	h.advance(10 * time.Second)
	h.tracker.sweep()
	if len(h.events) != 1 {
		t.Fatalf("quiet repeated: %v", h.kinds())
	}
}

func TestAttentionShortBurstsAndEchoDoNotCountAsBusy(t *testing.T) {
	h := newAttentionHarness()
	// 短命令：回车后输出不到 2 秒。
	h.tracker.observeInput("p1", "ls\r")
	h.advance(time.Second)
	h.tracker.observeOutput("p1", "ls output\r\n")
	h.advance(time.Second)
	h.tracker.observeOutput("p1", "more\r\n")
	h.advance(attentionQuietAfter)
	h.tracker.sweep()
	// 打字回显（命令已提交过一次）：每次输出都紧跟输入。
	h.tracker.observeInput("p2", "\r")
	for i := 0; i < 20; i++ {
		h.tracker.observeInput("p2", "x")
		h.advance(10 * time.Millisecond)
		h.tracker.observeOutput("p2", "x")
		h.advance(500 * time.Millisecond)
	}
	h.advance(attentionQuietAfter)
	h.tracker.sweep()
	if len(h.events) != 0 {
		t.Fatalf("unexpected quiet: %v", h.kinds())
	}
}

func TestAttentionExplicitSignalReplacesQuiet(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeInput("p1", "\r")
	h.advance(time.Second)
	for i := 0; i < 6; i++ {
		h.tracker.observeOutput("p1", "spinner")
		h.advance(time.Second)
	}
	h.tracker.observeOutput("p1", "\x1b]9;Agent turn complete\x07")
	h.advance(attentionQuietAfter)
	h.tracker.sweep()
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"notify:Agent turn complete"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestAttentionExitOnlyWhenNotClosedByUser(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeOutput("p1", "bye")
	h.tracker.observeOutput("p2", "bye")
	h.tracker.forget("p2")
	h.tracker.ended("p1")
	h.tracker.ended("p2")
	h.tracker.ended("never-seen")
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"exited:"}) || h.events[0].PaneID != "p1" {
		t.Fatalf("exit events = %+v", h.events)
	}
	h.tracker.mu.Lock()
	remaining := len(h.tracker.sessions)
	h.tracker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("sessions leaked: %d", remaining)
	}
}

func TestAttentionMessageSanitizes(t *testing.T) {
	if got := attentionMessage("  a\x00b\n\n c\u200b \xff"); got != "a b c\u200b" {
		t.Fatalf("message = %q", got)
	}
}

func BenchmarkAttentionPlainOutput(b *testing.B) {
	h := newAttentionHarness()
	chunk := strings.Repeat("compiling module foo/bar/baz.go ok\r\n", 400)
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		h.tracker.observeOutput("p1", chunk)
	}
}

func BenchmarkAttentionColoredOutput(b *testing.B) {
	h := newAttentionHarness()
	chunk := strings.Repeat("\x1b[32mPASS\x1b[0m test/foo \x1b]8;;file:///x\x07link\x1b]8;;\x07\r\n", 200)
	b.SetBytes(int64(len(chunk)))
	for i := 0; i < b.N; i++ {
		h.tracker.observeOutput("p1", chunk)
	}
}

func TestAttentionIgnoresBellsFromTyping(t *testing.T) {
	h := newAttentionHarness()
	// Tab 补全失败时 shell 响铃：这是对按键的回应，用户正在打字，不需要提醒。
	h.tracker.observeInput("p1", "x")
	h.advance(50 * time.Millisecond)
	h.tracker.observeOutput("p1", "\x07")
	h.advance(attentionBellAfterInput)
	h.tracker.observeOutput("p1", "\x07")
	// 通知不受按键影响。
	h.tracker.observeInput("p2", "x")
	h.tracker.observeOutput("p2", "\x1b]9;Approval requested\x07")
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"bell:", "notify:Approval requested"}) {
		t.Fatalf("events = %v", got)
	}
}

// A three-second Claude Code turn (spinner, then the answer) must alert: the busy
// window counts from the first output after Enter.
func TestAttentionShortAgentTurnAfterEnter(t *testing.T) {
	h := newAttentionHarness()
	h.tracker.observeInput("p1", "你好\r")
	h.advance(100 * time.Millisecond)
	h.tracker.observeOutput("p1", "redraw prompt") // echo of the submitted line
	for i := 0; i < 30; i++ {
		h.advance(100 * time.Millisecond)
		h.tracker.observeOutput("p1", "\x1b[2K✻ Cooking…")
	}
	h.advance(attentionQuietAfter)
	h.tracker.sweep()
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"quiet:"}) {
		t.Fatalf("events = %v", got)
	}
}

// Composing a prompt (no Enter) never arms the quiet alert, even when the TUI
// redraws hints asynchronously between keystrokes.
func TestAttentionComposingWithoutEnterIsNotBusy(t *testing.T) {
	h := newAttentionHarness()
	for i := 0; i < 10; i++ {
		h.tracker.observeInput("p1", "a")
		h.advance(400 * time.Millisecond)
		h.tracker.observeOutput("p1", "hint: Image in clipboard")
		h.advance(400 * time.Millisecond)
	}
	h.advance(attentionQuietAfter)
	h.tracker.sweep()
	if len(h.events) != 0 {
		t.Fatalf("unexpected events: %v", h.kinds())
	}
}

// Focus, mouse and query replies that xterm.js sends on the user's behalf are not
// keystrokes: they must not arm, and must not mute a bell as "feedback to typing".
func TestAttentionIgnoresTerminalReportsAsInput(t *testing.T) {
	for _, report := range []string{"\x1b[I", "\x1b[O", "\x1b[I\x1b[O", "\x1b[<0;12;5M", "\x1b[M !!", "\x1b[12;40R", "\x1b[?62;22c", "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b[?2026;2$y"} {
		if !isTerminalReport(report) {
			t.Fatalf("%q should be a terminal report", report)
		}
	}
	for _, input := range []string{"a", "\r", "\x1b[A", "\x1b", "\x03", "\x1b[200~paste\x1b[201~", ""} {
		if isTerminalReport(input) {
			t.Fatalf("%q is user input", input)
		}
	}
	h := newAttentionHarness()
	h.tracker.observeInput("p1", "\x1b[I")
	h.tracker.observeOutput("p1", "\x07")
	if got := h.kinds(); !reflect.DeepEqual(got, []string{"bell:"}) {
		t.Fatalf("a focus report muted the bell: %v", got)
	}
}
