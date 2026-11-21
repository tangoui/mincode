package db

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Produced by an isolated Redis 8.10.2 server (RDB v15), not by our checksum code.
func TestRedisDumpNativeFixtures(t *testing.T) {
	for _, raw := range []string{
		"000568656c6c6f0f0078f6ba7acb98ddf2", // hello
		"00c02a0f009a7f39f628cfb2a8",         // integer 42
	} {
		payload, err := hex.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRedisDump(payload); err != nil {
			t.Fatalf("valid native dump rejected: %v", err)
		}
		payload[len(payload)-1] ^= 1
		if err := validateRedisDump(payload); err == nil {
			t.Fatal("corrupt native checksum accepted")
		}
	}
}

func TestRedisDumpUnsupportedNativeEncodingIsManual(t *testing.T) {
	for _, raw := range []string{
		"00c31e47d0017878e0ff00e0ff00e0ff00e0ff00e0ff00e0ff00e0ff00e08b000178780f00871d1a45000705b7", // LZF
		"1201020d0d0000000200816102816202ff0f00736f6f85bded4649",                                     // quicklist2
		"100c0c00000002008161020101ff0f009f22f6e404bb1923",                                           // hash listpack
	} {
		payload, _ := hex.DecodeString(raw)
		if err := validateRedisDump(payload); err == nil || !strings.Contains(err.Error(), "requires_manual_recovery") {
			t.Fatalf("unsupported encoding not manual: %v", err)
		}
	}
}
