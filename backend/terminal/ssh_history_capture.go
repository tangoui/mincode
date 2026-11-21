package terminal

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// sshInputCapture 从发往 PTY 的输入流还原用户键入并回车提交的命令行，
// 用于静默记录本机 SSH 历史：不读取远端历史文件、不向终端注入钩子或脚本，
// 远端（含堡垒机审计）不会留下任何采集痕迹。
//
// 输入流只包含按键本身，不含 shell 回显，因此补全结果、历史召回文本无法还原；
// 这类行被标记为 tainted 并在提交时丢弃，宁可漏记也不保存错误或不完整的命令。
//
// 远端在密码/口令/验证码提示符下关闭回显，输入流与普通命令无法区分；
// 因此同时跟踪远端输出的最后一行（outputTail），提示符命中时整行丢弃。
type sshInputCapture struct {
	buf        []rune
	tainted    bool
	outputTail []byte
}

// sshOutputTailLimit 限制保留的远端输出末行长度；提示符判断只依赖行尾。
const sshOutputTailLimit = 256

// secretPromptPattern 匹配以冒号结尾的密码、口令、PIN、验证码类提示符，
// 如 "[sudo] password for u: "、"Enter passphrase for key '...': "、"请输入密码："。
var secretPromptPattern = regexp.MustCompile(`(?i)(pass(word|phrase|code)|pass phrase|\bpin\b|\botp\b|\btoken\b|\bsecret\b|(verification|authentication|security|2fa|mfa|one[- ]time|sms|auth)\s*code|密码|口令|验证码|动态码)[^\r\n]{0,80}[:：]\s*$`)

// observeOutput 记录远端输出的末行片段（按 \r/\n 截断，上限 sshOutputTailLimit 字节）。
func (c *sshInputCapture) observeOutput(text string) {
	if i := strings.LastIndexAny(text, "\r\n"); i >= 0 {
		c.outputTail = c.outputTail[:0]
		text = text[i+1:]
	}
	if len(text) >= sshOutputTailLimit {
		c.outputTail = append(c.outputTail[:0], text[len(text)-sshOutputTailLimit:]...)
		return
	}
	c.outputTail = append(c.outputTail, text...)
	if over := len(c.outputTail) - sshOutputTailLimit; over > 0 {
		c.outputTail = append(c.outputTail[:0], c.outputTail[over:]...)
	}
}

// atSecretPrompt 判断远端输出末行（去除终端控制序列后）是否为敏感输入提示符。
func (c *sshInputCapture) atSecretPrompt() bool {
	if len(c.outputTail) == 0 {
		return false
	}
	return secretPromptPattern.MatchString(stripTerminalControls(c.outputTail))
}

// stripTerminalControls 去除 CSI/OSC 等转义序列与控制字符，仅保留可见文本。
func stripTerminalControls(s []byte) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\x1b' && i+1 < len(s) && s[i+1] == '[':
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
		case c == '\x1b' && i+1 < len(s) && s[i+1] == ']':
			i += 2
			for i < len(s) && s[i] != '\a' && !(s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\') {
				i++
			}
			if i < len(s) && s[i] == '\x1b' {
				i++
			}
		case c == '\x1b':
			i++
		case c == '\t':
			b.WriteByte(' ')
		case c < 0x20 || c == 0x7f:
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// pasteEndSequence 为 bracketed paste 结束标记；粘贴内容已随粘贴发给 shell，
// 最后一行无需等待用户回车即可提交。
const pasteEndSequence = "[201~"

// commit 提交当前缓冲（tainted 时丢弃）并复位状态。
func (c *sshInputCapture) commit(out *[]string) {
	if !c.tainted {
		if line := string(c.buf); line != "" {
			*out = append(*out, line)
		}
	}
	c.buf, c.tainted = c.buf[:0], false
}

// feed 消费一段终端输入，返回其中被回车/换行提交的命令行（未过滤，过滤由记录阶段负责）。
func (c *sshInputCapture) feed(input string) []string {
	var committed []string
	// 本段输入期间远端停在敏感提示符（回显已关闭）时，当前行及本段内的后续行均不记录；
	// 本段恰好在提交处结束时不延续标记，后续输入按新的远端输出重新判断。
	secret := c.atSecretPrompt()
	markSecret := func(next int) {
		if secret && next < len(input) {
			c.tainted = true
		}
	}
	markSecret(0)
	for i := 0; i < len(input); {
		switch b := input[i]; {
		case b == '\x1b':
			seq := escapeSequence(input[i:])
			if cursorMoveSequence(seq) {
				// 方向键/Home/End/Delete 会移动光标或召回历史，后续编辑无法可靠还原。
				c.tainted = true
			}
			i += 1 + len(seq)
			if seq == pasteEndSequence {
				c.commit(&committed)
				markSecret(i)
			}
		case b == '\t':
			// 补全结果来自 shell 回显，输入流中只有前缀。
			c.tainted = true
			i++
		case b == '\x7f', b == '\b':
			if n := len(c.buf); n > 0 {
				c.buf = c.buf[:n-1]
			}
			i++
		case b == '\x15': // Ctrl+U 清空当前行
			c.buf = c.buf[:0]
			i++
		case b == '\x03': // Ctrl+C 放弃当前命令
			c.buf, c.tainted = c.buf[:0], false
			i++
			markSecret(i)
		case b == '\r', b == '\n':
			c.commit(&committed)
			i++
			markSecret(i)
		case b < 0x20:
			// 其余控制字符（如 Ctrl+R 搜索前缀）不参与命令行还原。
			i++
		default:
			r, size := utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size <= 1 {
				i++
				continue
			}
			c.buf = append(c.buf, r)
			i += size
		}
	}
	return committed
}

// escapeSequence 返回 \x1b 之后的序列体（不含 \x1b 本身）。
// CSI 序列读到终结符（0x40-0x7e）为止；不完整序列消费剩余输入，避免被误当文本。
func escapeSequence(s string) string {
	if len(s) < 2 {
		return ""
	}
	if s[1] == '[' {
		for j := 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return s[1 : j+1]
			}
		}
		return s[1:]
	}
	return s[1:2]
}

// cursorMoveSequence 判断是否为会破坏行还原的光标移动/历史召回序列。
// bracketed paste 标记（[200~ / [201~）等其余序列不影响缓冲。
func cursorMoveSequence(seq string) bool {
	switch seq {
	case "[A", "[B", "[C", "[D", "[H", "[F", "[1~", "[3~", "[4~":
		return true
	}
	return false
}

// captureSSHInput 仅对 SSH 主会话还原并提交命令行；本地 shell、派生会话
// （docker exec 等 ephemeral）与缺少 profile 标识的会话不记录。
func captureSSHInput(proc *shellProcess, input string) {
	if input == "" {
		return
	}
	proc.mu.Lock()
	capture := sshHistoryCaptureLocked(proc)
	if capture == nil {
		proc.mu.Unlock()
		return
	}
	commands := capture.feed(input)
	payload := sshHistoryPayload{
		ProfileID: proc.sshHistoryProfileID,
		Host:      proc.sshHistoryHost,
		Port:      proc.sshHistoryPort,
		Username:  proc.sshHistoryUser,
	}
	proc.mu.Unlock()
	recordSSHCommands(payload, commands)
}

// observeSSHOutput 记录远端输出末行，供 captureSSHInput 识别密码等敏感提示符。
func observeSSHOutput(proc *shellProcess, output string) {
	if output == "" {
		return
	}
	proc.mu.Lock()
	if capture := sshHistoryCaptureLocked(proc); capture != nil {
		capture.observeOutput(output)
	}
	proc.mu.Unlock()
}

// sshHistoryCaptureLocked 返回需要记录历史的会话的捕获状态（按需创建），其余会话返回 nil。
// 调用方必须持有 proc.mu。
func sshHistoryCaptureLocked(proc *shellProcess) *sshInputCapture {
	if proc.sshHistoryProfileID == "" || proc.sshHistoryHost == "" || proc.ephemeral {
		return nil
	}
	if proc.historyCapture == nil {
		proc.historyCapture = &sshInputCapture{}
	}
	return proc.historyCapture
}

// recordSSHCommands 将用户提交的命令静默合并进本机历史，与手动导入共用同一
// 存储、过滤与上限规则；已暂停保存（enabled=false）时不记录。失败仅放弃本次记录。
// userDataDirFunc 为存储根目录提供注入点，测试可替换为临时目录。
var userDataDirFunc = core.UserDataDir

func recordSSHCommands(p sshHistoryPayload, commands []string) {
	recordSSHCommandsAt(userDataDirFunc(), p, commands)
}

// recordSSHCommandsAt 为 recordSSHCommands 的可注入存储根目录版本，供测试使用。
func recordSSHCommandsAt(root string, p sshHistoryPayload, commands []string) {
	if len(commands) == 0 {
		return
	}
	safe := make([]string, 0, len(commands))
	for _, command := range commands {
		if safeHistoryCommand(command) {
			safe = append(safe, command)
		}
	}
	if len(safe) == 0 {
		return
	}
	sshHistoryMu.Lock()
	defer sshHistoryMu.Unlock()
	path, err := sshHistoryPath(root, p)
	if err != nil {
		return
	}
	data, err := readSSHHistory(path)
	if err != nil || !data.Enabled {
		return
	}
	if err := writeSSHHistory(path, mergeSSHHistory(data, safe)); err != nil {
		return
	}
}
