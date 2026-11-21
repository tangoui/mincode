package terminal

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"mincode/app/backend/core"
)

// 终端“需要你回来看”的信号，从 PTY 输出里直接识别，所以对后台标签、
// 被 KeepAlive 卸载的终端视图同样有效。四种信号：
//   - bell：程序响铃（不含 OSC 字符串的 BEL 结束符）；
//   - notify：OSC 9 / 777 / 99 桌面通知（codex、iTerm2 风格工具都会发）；
//   - quiet：回车提交命令后持续输出一段时间又安静下来，通常是任务结束或在等输入；
//   - exited：终端进程自己退出（用户关闭的不算）。
//
// 是否真的打扰用户（标签正在看就不提醒）由前端决定。
const (
	terminalAttentionEvent = "terminal-attention"

	// 用户按键后这段时间内的输出视为回显，不算“在忙”。
	attentionEchoWindow = 250 * time.Millisecond
	// 回车后至少连续输出这么久，之后的安静才值得提醒，避免每条短命令都响。
	// 短到能覆盖 3 秒左右的 AI 回合（Claude Code 的 spinner 一直在刷新）。
	attentionBusyMin = 2 * time.Second
	// 输出停止这么久后判定为安静。
	attentionQuietAfter = 4 * time.Second
	// 按键后这段时间内的响铃是对按键本身的反馈（补全失败、行首退格），不提醒。
	attentionBellAfterInput = time.Second
	// 同一终端两次 bell/notify 提醒的最小间隔，挡住连续响铃。
	attentionSignalInterval = 3 * time.Second
	attentionTickInterval   = time.Second
	// OSC 字符串只保留这么多字节；更长的只截断，不影响解析状态。
	attentionMaxOSCBytes = 1024
	attentionMaxMessage  = 200
)

type terminalAttentionPayload struct {
	PaneID  string `json:"paneId"`
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	At      int64  `json:"at"`
}

type escapeState uint8

const (
	escGround escapeState = iota
	escEscape
	escString    // OSC/DCS/APC/PM/SOS 字符串体
	escStringEsc // 字符串体中遇到 ESC，等待 '\'
)

// escapeScanner 跨输出块识别 BEL 与 OSC 通知；只关心字符串边界，其余转义序列
// 回到 ground 后按普通可打印字符跳过即可。
type escapeScanner struct {
	state    escapeState
	osc      bool
	payload  []byte
	overflow bool
}

type attentionSignal struct {
	kind    string
	message string
}

func (s *escapeScanner) scan(data string, found func(attentionSignal)) {
	for i := 0; i < len(data); i++ {
		s.step(data[i], found)
	}
}

func (s *escapeScanner) step(b byte, found func(attentionSignal)) {
	switch s.state {
	case escGround:
		switch b {
		case 0x1b:
			s.state = escEscape
		case 0x07:
			found(attentionSignal{kind: "bell"})
		}
	case escEscape:
		switch b {
		case ']':
			s.beginString(true)
		case 'P', '_', '^', 'X':
			s.beginString(false)
		case 0x1b:
		default:
			s.state = escGround
		}
	case escString:
		switch b {
		case 0x07:
			s.endString(found)
		case 0x1b:
			s.state = escStringEsc
		case 0x18, 0x1a: // CAN/SUB 中止序列
			s.reset()
		default:
			if s.osc {
				if len(s.payload) < attentionMaxOSCBytes {
					s.payload = append(s.payload, b)
				} else {
					s.overflow = true
				}
			}
		}
	case escStringEsc:
		if b == '\\' {
			s.endString(found)
			return
		}
		// 未以 ST 结束的字符串被新的转义序列打断：丢弃它，按 ESC 后的字节处理。
		s.reset()
		s.state = escEscape
		s.step(b, found)
	}
}

func (s *escapeScanner) beginString(osc bool) {
	s.state = escString
	s.osc = osc
	s.payload = s.payload[:0]
	s.overflow = false
}

func (s *escapeScanner) reset() {
	s.state = escGround
	s.osc = false
	s.payload = s.payload[:0]
	s.overflow = false
}

func (s *escapeScanner) endString(found func(attentionSignal)) {
	if s.osc && isNotificationOSC(s.payload) {
		if message, ok := oscNotification(string(s.payload)); ok {
			found(attentionSignal{kind: "notify", message: message})
		}
	}
	s.reset()
}

// isNotificationOSC 先按字节判断前缀，常见的标题、超链接 OSC 不用转成字符串。
func isNotificationOSC(payload []byte) bool {
	return bytes.HasPrefix(payload, []byte("9;")) || bytes.HasPrefix(payload, []byte("777;")) || bytes.HasPrefix(payload, []byte("99;"))
}

// oscNotification 识别三种通知格式：OSC 9（iTerm2；ConEmu 的 "9;<数字>;…"
// 是进度等控制命令，不算通知）、OSC 777;notify;标题;正文、OSC 99（kitty）。
func oscNotification(payload string) (string, bool) {
	code, rest, ok := strings.Cut(payload, ";")
	if !ok {
		return "", false
	}
	switch code {
	case "9":
		sub, _, _ := strings.Cut(rest, ";")
		if sub != "" && strings.Trim(sub, "0123456789") == "" {
			return "", false
		}
		return attentionMessage(rest), true
	case "777":
		kind, body, _ := strings.Cut(rest, ";")
		if kind != "notify" {
			return "", false
		}
		title, text, _ := strings.Cut(body, ";")
		title, text = attentionMessage(title), attentionMessage(text)
		switch {
		case title != "" && text != "":
			return title + ": " + text, true
		case text != "":
			return text, true
		}
		return title, true
	case "99":
		_, text, _ := strings.Cut(rest, ";")
		return attentionMessage(text), true
	}
	return "", false
}

// attentionMessage 去掉控制字符、合并空白并截断，结果只作为提示文字展示。
func attentionMessage(value string) string {
	value = strings.ToValidUTF8(value, "")
	var builder strings.Builder
	space := false
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			space = builder.Len() > 0
			continue
		}
		if count >= attentionMaxMessage {
			builder.WriteString("…")
			break
		}
		if space {
			builder.WriteByte(' ')
			count++
			space = false
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String()
}

type attentionSession struct {
	scanner   escapeScanner
	lastInput time.Time
	// armed：用户回车提交了命令/提示词，之后的“忙后安静”才算数；打字、
	// 后台日志不会触发。
	armed      bool
	lastOutput time.Time
	busySince  time.Time // 零值表示不在忙
	lastSignal time.Time
}

type attentionTracker struct {
	mu       sync.Mutex
	sessions map[string]*attentionSession
	ticking  bool
	now      func() time.Time
	emit     func(terminalAttentionPayload)
	// startTicker 启动安静检测的轮询；测试里替换为空操作，直接调用 sweep。
	startTicker func()
}

func newAttentionTracker() *attentionTracker {
	t := &attentionTracker{
		sessions: make(map[string]*attentionSession),
		now:      time.Now,
		emit: func(payload terminalAttentionPayload) {
			core.Emit(terminalAttentionEvent, payload)
		},
	}
	t.startTicker = func() { go t.tickLoop() }
	return t
}

var attention = newAttentionTracker()

func (t *attentionTracker) session(id string) *attentionSession {
	s := t.sessions[id]
	if s == nil {
		s = &attentionSession{}
		t.sessions[id] = s
	}
	return s
}

// terminalReportPattern 匹配 xterm.js 代替用户发给程序的数据：焦点进出、
// 鼠标上报、光标位置/设备属性/模式查询的回复、OSC/DCS 回复。它们不是按键。
var terminalReportPattern = regexp.MustCompile(`^(?:\x1b\[[IO]|\x1b\[M[\s\S]{3}|\x1b\[<\d+;\d+;\d+[Mm]|\x1b\[\d+;\d+R|\x1b\[[?>]?[\d;]*c|\x1b\[\?[\d;]+\$y|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1bP[^\x1b]*\x1b\\)+$`)

func isTerminalReport(input string) bool {
	return input != "" && terminalReportPattern.MatchString(input)
}

func (t *attentionTracker) observeInput(id, input string) {
	if id == "" || input == "" || isTerminalReport(input) {
		return
	}
	t.mu.Lock()
	s := t.session(id)
	s.lastInput = t.now()
	if strings.ContainsAny(input, "\r\n") {
		s.armed = true
		s.busySince = time.Time{}
	}
	t.mu.Unlock()
}

func (t *attentionTracker) observeOutput(id, data string) {
	if id == "" || data == "" {
		return
	}
	var pending []terminalAttentionPayload
	t.mu.Lock()
	now := t.now()
	s := t.session(id)
	// 绝大多数输出块不含 ESC/BEL，直接跳过逐字节扫描。
	if s.scanner.state != escGround || strings.ContainsAny(data, "\x07\x1b") {
		s.scanner.scan(data, func(signal attentionSignal) {
			if signal.kind == "bell" && now.Sub(s.lastInput) < attentionBellAfterInput {
				return
			}
			// 显式信号取代“忙后安静”的推断，避免同一件事提醒两次。
			s.busySince = time.Time{}
			s.armed = false
			if !s.lastSignal.IsZero() && now.Sub(s.lastSignal) < attentionSignalInterval {
				return
			}
			s.lastSignal = now
			pending = append(pending, terminalAttentionPayload{PaneID: id, Kind: signal.kind, Message: signal.message, At: now.UnixMilli()})
		})
	}
	if len(pending) == 0 {
		if s.armed && s.busySince.IsZero() && now.Sub(s.lastInput) >= attentionEchoWindow {
			s.busySince = now
		}
		s.lastOutput = now
	}
	start := !s.busySince.IsZero() && !t.ticking
	if start {
		t.ticking = true
	}
	t.mu.Unlock()
	if start {
		t.startTicker()
	}
	for _, payload := range pending {
		t.emit(payload)
	}
}

// sweep 找出忙了足够久又安静下来的终端；返回是否还有终端在忙。
func (t *attentionTracker) sweep() bool {
	var pending []terminalAttentionPayload
	t.mu.Lock()
	now := t.now()
	busy := false
	for id, s := range t.sessions {
		if s.busySince.IsZero() {
			continue
		}
		if now.Sub(s.lastOutput) < attentionQuietAfter {
			busy = true
			continue
		}
		if s.lastOutput.Sub(s.busySince) >= attentionBusyMin {
			pending = append(pending, terminalAttentionPayload{PaneID: id, Kind: "quiet", At: now.UnixMilli()})
			s.armed = false
		}
		s.busySince = time.Time{}
	}
	if !busy {
		t.ticking = false
	}
	t.mu.Unlock()
	for _, payload := range pending {
		t.emit(payload)
	}
	return busy
}

func (t *attentionTracker) tickLoop() {
	ticker := time.NewTicker(attentionTickInterval)
	defer ticker.Stop()
	for range ticker.C {
		if !t.sweep() {
			return
		}
	}
}

// forget 在用户主动关闭终端时调用：之后的进程结束不再算“需要你处理”。
func (t *attentionTracker) forget(id string) {
	t.mu.Lock()
	delete(t.sessions, id)
	t.mu.Unlock()
}

// ended 在终端进程自己结束时调用；仍在跟踪（没被用户关闭）才提醒。
func (t *attentionTracker) ended(id string) {
	t.mu.Lock()
	_, tracked := t.sessions[id]
	delete(t.sessions, id)
	now := t.now()
	t.mu.Unlock()
	if tracked {
		t.emit(terminalAttentionPayload{PaneID: id, Kind: "exited", At: now.UnixMilli()})
	}
}

func (t *attentionTracker) reset() {
	t.mu.Lock()
	t.sessions = make(map[string]*attentionSession)
	t.mu.Unlock()
}
