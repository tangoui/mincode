package crypto

import (
	"encoding/json"
	"strings"
	"testing"
)

func hashPasscode(t *testing.T, passcode string) PasscodeRecord {
	t.Helper()
	out, err := passcodeHashHandler("passcode_hash", `{"passcode":`+jsonString(passcode)+`}`)
	if err != nil {
		t.Fatalf("passcode_hash: %v", err)
	}
	var record PasscodeRecord
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	return record
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func verifyPasscode(t *testing.T, record PasscodeRecord, passcode string) bool {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"passcode":   passcode,
		"algorithm":  record.Algorithm,
		"iterations": record.Iterations,
		"salt":       record.Salt,
		"hash":       record.Hash,
	})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	out, err := passcodeVerifyHandler("passcode_verify", string(args))
	if err != nil {
		t.Fatalf("passcode_verify: %v", err)
	}
	return out == "true"
}

func TestPasscodeHashRoundTrip(t *testing.T) {
	record := hashPasscode(t, "correct horse battery staple")
	if record.Algorithm != passcodeAlgorithm || record.Iterations != passcodeIterations {
		t.Fatalf("unexpected record parameters: %+v", record)
	}
	if record.Salt == "" || record.Hash == "" {
		t.Fatalf("record must carry salt and hash: %+v", record)
	}
	if !verifyPasscode(t, record, "correct horse battery staple") {
		t.Fatal("expected the original passcode to verify")
	}
	if verifyPasscode(t, record, "Correct horse battery staple") {
		t.Fatal("expected a different passcode to be rejected")
	}
	if verifyPasscode(t, record, "") {
		t.Fatal("expected an empty passcode to be rejected")
	}
}

func TestPasscodeHashUsesFreshSalt(t *testing.T) {
	first := hashPasscode(t, "same-passcode")
	second := hashPasscode(t, "same-passcode")
	if first.Salt == second.Salt || first.Hash == second.Hash {
		t.Fatal("expected a fresh random salt for every derivation")
	}
}

func TestPasscodeHashRejectsEmptyPasscode(t *testing.T) {
	if _, err := passcodeHashHandler("passcode_hash", `{"passcode":""}`); err == nil {
		t.Fatal("expected an error for an empty passcode")
	}
}

func TestPasscodeVerifyRejectsUnusableRecords(t *testing.T) {
	record := hashPasscode(t, "passcode")
	cases := map[string]map[string]any{
		"unsupported algorithm": {"passcode": "passcode", "algorithm": "sha1", "iterations": record.Iterations, "salt": record.Salt, "hash": record.Hash},
		"zero iterations":       {"passcode": "passcode", "algorithm": record.Algorithm, "iterations": 0, "salt": record.Salt, "hash": record.Hash},
		"excessive iterations":  {"passcode": "passcode", "algorithm": record.Algorithm, "iterations": passcodeMaxIterations + 1, "salt": record.Salt, "hash": record.Hash},
		"invalid salt":          {"passcode": "passcode", "algorithm": record.Algorithm, "iterations": record.Iterations, "salt": "not base64!", "hash": record.Hash},
		"invalid hash":          {"passcode": "passcode", "algorithm": record.Algorithm, "iterations": record.Iterations, "salt": record.Salt, "hash": ""},
	}
	for name, args := range cases {
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("%s: encode args: %v", name, err)
		}
		if _, err := passcodeVerifyHandler("passcode_verify", string(encoded)); err == nil {
			t.Fatalf("%s: expected an error", name)
		} else if !strings.HasPrefix(err.Error(), "passcode_verify:") {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
	}
}
