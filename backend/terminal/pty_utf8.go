package terminal

import "unicode/utf8"

// maxIncompleteTail 与 Rust 侧 MAX_INCOMPLETE_TAIL 对齐：carry 缓冲超过该长度仍
// 无法构成合法 UTF-8 前缀时，丢弃 1 字节并替换为 U+FFFD，避免死等。
const maxIncompleteTail = 8

// clampPtyColsRows 与 start_ssh_shell / 前端 clamp 保持一致的尺寸约束。
func clampPtyColsRows(cols, rows *uint32) (uint16, uint16) {
	c := uint32(120)
	if cols != nil {
		c = *cols
	}
	if c < 20 {
		c = 20
	} else if c > 512 {
		c = 512
	}
	r := uint32(32)
	if rows != nil {
		r = *rows
	}
	if r < 5 {
		r = 5
	} else if r > 256 {
		r = 256
	}
	return uint16(c), uint16(r)
}

// ptyPushAndTakeUTF8 将新读到的字节拼入 carry 缓冲，只取出已经构成完整 UTF-8 的
// 文本，跨 read 边界被截断的多字节字符保留在 carry 中等待下次拼接；非法字节序列
// 替换为 U+FFFD。语义对齐 Rust 侧 pty_push_and_take_utf8。
func ptyPushAndTakeUTF8(carry *[]byte, chunk []byte) string {
	*carry = append(*carry, chunk...)
	buf := *carry
	var out []byte
	for len(buf) > 0 {
		if utf8.Valid(buf) {
			out = append(out, buf...)
			buf = nil
			break
		}
		r, size := utf8.DecodeRune(buf)
		if r != utf8.RuneError {
			out = append(out, buf[:size]...)
			buf = buf[size:]
			continue
		}
		// size==0 表示空输入（不会发生，buf非空）；size==1 表示单字节非法序列
		// 或不完整的多字节序列结尾。区分二者：尝试找出 buf 中最长的合法前缀。
		validLen := validPrefixLen(buf)
		if validLen > 0 {
			out = append(out, buf[:validLen]...)
			buf = buf[validLen:]
			continue
		}
		// 无合法前缀：buf[0] 要么是非法字节，要么是尚不完整的多字节序列起始。
		if isIncompleteLeadByte(buf) && len(buf) <= maxIncompleteTail {
			// 可能仍在等待后续字节，保留待下次拼接
			break
		}
		// 非法字节，或超过最大等待长度仍不完整：替换为 U+FFFD 并丢弃 1 字节
		out = append(out, "\uFFFD"...)
		buf = buf[1:]
	}
	*carry = append([]byte(nil), buf...)
	return string(out)
}

// validPrefixLen 返回 buf 中最长的、可被安全输出的合法 UTF-8 前缀字节数
// （不包含末尾可能不完整的多字节序列）。
func validPrefixLen(buf []byte) int {
	valid := 0
	for valid < len(buf) {
		r, size := utf8.DecodeRune(buf[valid:])
		if r == utf8.RuneError && size <= 1 {
			break
		}
		valid += size
	}
	return valid
}

// isIncompleteLeadByte 判断 buf 是否以一个多字节 UTF-8 序列的起始字节开头，且
// 目前累积的字节数不足以构成完整字符（即等待更多字节）。
func isIncompleteLeadByte(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	b0 := buf[0]
	var want int
	switch {
	case b0&0x80 == 0x00:
		return false // ASCII，必然合法，不会走到这里
	case b0&0xE0 == 0xC0:
		want = 2
	case b0&0xF0 == 0xE0:
		want = 3
	case b0&0xF8 == 0xF0:
		want = 4
	default:
		return false // 非法起始字节
	}
	if len(buf) >= want {
		return false // 已够长仍非法，说明是真正的非法序列而非不完整
	}
	for i := 1; i < len(buf); i++ {
		if buf[i]&0xC0 != 0x80 {
			return false // 后续字节不是合法的续字节
		}
	}
	return true
}
