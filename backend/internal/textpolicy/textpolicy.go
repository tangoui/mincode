// Package textpolicy holds the host's shared rule for characters that must
// never appear in host-rendered or host-forwarded untrusted text (plugin
// contributed text, debugger source addresses): they can disguise what the
// user sees or copies. Callers reject such text; they never strip it.
package textpolicy

// UnsafeRune reports whether r is never allowed: C0 controls (except \n and
// \t when allowed), DEL, C1 controls, U+2028/U+2029, bidi
// embeddings/overrides/isolates and zero-width characters (including the BOM).
func UnsafeRune(r rune, allowNewline, allowTab bool) bool {
	switch {
	case r == '\n':
		return !allowNewline
	case r == '\t':
		return !allowTab
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x2028, r == 0x2029:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200b && r <= 0x200f, r == 0x2060, r == 0xfeff:
		return true
	}
	return false
}
