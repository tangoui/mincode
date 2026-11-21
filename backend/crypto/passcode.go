// Passcode：应用锁屏口令的 PBKDF2-SHA256 派生与恒定时间校验。
//
// 口令本身从不落盘，持久化的只有 {salt, iterations, hash}；派生与比对都放在 Go 侧，
// 既遵循「凭据处理走后端」的边界，也避免 WebView 在非安全上下文下缺失 SubtleCrypto。
//
// 命令契约（入参 camelCase）：
//
//	passcode_hash   {passcode}                                    -> {algorithm, iterations, salt, hash}
//	passcode_verify {passcode, algorithm, iterations, salt, hash} -> bool
//
// 注意：passcode_verify 只是纯函数，它信任调用方给的记录，不能用于授权判断。
// 应用锁屏改由 backend/applock 读取后端自己保存的记录完成校验。
package crypto

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"

	"mincode/app/backend/core"
)

const (
	passcodeAlgorithm     = "pbkdf2-sha256"
	passcodeIterations    = 600_000
	passcodeMaxIterations = 5_000_000
	passcodeSaltLen       = 16
	passcodeKeyLen        = 32
)

// PasscodeRecord 是锁屏口令的可持久化校验材料。
type PasscodeRecord struct {
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
}

func init() {
	core.Register("passcode_hash", passcodeHashHandler)
	core.Register("passcode_verify", passcodeVerifyHandler)
}

type passcodeHashArg struct {
	Passcode string `json:"passcode"`
}

func passcodeHashHandler(_, argsJson string) (string, error) {
	var a passcodeHashArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	record, err := HashPasscode(a.Passcode)
	if err != nil {
		return "", fmt.Errorf("passcode_hash: %w", err)
	}
	return core.EncodeResult(record)
}

// HashPasscode 用新的随机盐派生一条口令校验记录。
func HashPasscode(passcode string) (PasscodeRecord, error) {
	if passcode == "" {
		return PasscodeRecord{}, errors.New("empty passcode")
	}
	salt := make([]byte, passcodeSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return PasscodeRecord{}, fmt.Errorf("read salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, passcode, salt, passcodeIterations, passcodeKeyLen)
	if err != nil {
		return PasscodeRecord{}, fmt.Errorf("derive: %w", err)
	}
	return PasscodeRecord{
		Algorithm:  passcodeAlgorithm,
		Iterations: passcodeIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(key),
	}, nil
}

// passcodeMaxHashLen 限制派生长度：hash 字段来自持久化记录，过长会让单次校验变成 CPU 消耗。
const passcodeMaxHashLen = 64

func decodePasscodeRecord(record PasscodeRecord) (salt, expected []byte, err error) {
	if record.Algorithm != passcodeAlgorithm {
		return nil, nil, fmt.Errorf("unsupported algorithm: %s", record.Algorithm)
	}
	// 迭代次数来自持久化记录：上界防止被改写成一个会让解锁永久卡住的数值。
	if record.Iterations <= 0 || record.Iterations > passcodeMaxIterations {
		return nil, nil, errors.New("invalid iteration count")
	}
	salt, err = base64.StdEncoding.DecodeString(record.Salt)
	if err != nil || len(salt) == 0 {
		return nil, nil, errors.New("invalid salt")
	}
	expected, err = base64.StdEncoding.DecodeString(record.Hash)
	if err != nil || len(expected) == 0 || len(expected) > passcodeMaxHashLen {
		return nil, nil, errors.New("invalid hash")
	}
	return salt, expected, nil
}

// ValidPasscodeRecord 报告记录能否用于校验。
func ValidPasscodeRecord(record PasscodeRecord) bool {
	_, _, err := decodePasscodeRecord(record)
	return err == nil
}

// VerifyPasscode 以恒定时间比较口令与一条可信记录。
// 记录必须来自后端自己的存储，不能取自调用方入参。
func VerifyPasscode(record PasscodeRecord, passcode string) (bool, error) {
	salt, expected, err := decodePasscodeRecord(record)
	if err != nil {
		return false, err
	}
	if passcode == "" {
		return false, nil
	}
	key, err := pbkdf2.Key(sha256.New, passcode, salt, record.Iterations, len(expected))
	if err != nil {
		return false, fmt.Errorf("derive: %w", err)
	}
	return subtle.ConstantTimeCompare(key, expected) == 1, nil
}

type passcodeVerifyArg struct {
	Passcode   string `json:"passcode"`
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
}

func passcodeVerifyHandler(_, argsJson string) (string, error) {
	var a passcodeVerifyArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	ok, err := VerifyPasscode(PasscodeRecord{
		Algorithm:  a.Algorithm,
		Iterations: a.Iterations,
		Salt:       a.Salt,
		Hash:       a.Hash,
	}, a.Passcode)
	if err != nil {
		return "", fmt.Errorf("passcode_verify: %w", err)
	}
	return core.EncodeResult(ok)
}
