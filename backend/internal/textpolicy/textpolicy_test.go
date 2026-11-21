package textpolicy

import "testing"

func TestUnsafeRune(t *testing.T) {
	for _, r := range []rune{0x00, 0x1f, 0x7f, 0x85, 0x9f, 0x2028, 0x2029, 0x202a, 0x202e, 0x2066, 0x2069, 0x200b, 0x200f, 0x2060, 0xfeff, '\r'} {
		if !UnsafeRune(r, true, true) {
			t.Errorf("U+%04X allowed", r)
		}
	}
	for _, r := range []rune{'a', ' ', '~', 0xa0, 0x4e2d, 0x2030, 0x2065, 0x206a, 0x1f600} {
		if UnsafeRune(r, false, false) {
			t.Errorf("U+%04X refused", r)
		}
	}
	if !UnsafeRune('\n', false, true) || UnsafeRune('\n', true, false) {
		t.Error("newline flag ignored")
	}
	if !UnsafeRune('\t', true, false) || UnsafeRune('\t', false, true) {
		t.Error("tab flag ignored")
	}
}
