package terminal

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTOTPPeriod = 30
	defaultTOTPDigits = 6
)

// terminalTOTPConfiguration describes a parsed TOTP input without retaining its source string.
type terminalTOTPConfiguration struct {
	secret    []byte
	algorithm func() hash.Hash
	digits    int
	period    int
}

// generateTerminalTOTP creates the current code from an otpauth TOTP URI or a
// standalone Base32 secret. The input, secret, and generated code must remain in
// process and must never be logged or included in an error message.
func generateTerminalTOTP(input string, now time.Time) (string, error) {
	configuration, err := parseTerminalTOTPInput(input)
	if err != nil {
		return "", err
	}

	counter := now.Unix() / int64(configuration.period)
	if counter < 0 {
		return "", invalidTerminalTOTPConfiguration()
	}
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], uint64(counter))

	mac := hmac.New(configuration.algorithm, configuration.secret)
	_, _ = mac.Write(counterBytes[:])
	digest := mac.Sum(nil)
	offset := int(digest[len(digest)-1] & 0x0f)
	if offset+4 > len(digest) {
		return "", invalidTerminalTOTPConfiguration()
	}
	binaryCode := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	modulo := uint64(1)
	for range configuration.digits {
		modulo *= 10
	}
	return fmt.Sprintf("%0*d", configuration.digits, uint64(binaryCode)%modulo), nil
}

// parseTerminalTOTPInput accepts an otpauth URI (including the common
// optauth:// typo) or a raw Base32 shared secret.
func parseTerminalTOTPInput(input string) (terminalTOTPConfiguration, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return terminalTOTPConfiguration{}, invalidTerminalTOTPConfiguration()
	}
	if strings.HasPrefix(strings.ToLower(input), "optauth://") {
		input = "otpauth://" + input[len("optauth://"):]
	}
	if !strings.HasPrefix(strings.ToLower(input), "otpauth://") {
		secret, err := decodeTerminalTOTPSecret(input)
		if err != nil {
			return terminalTOTPConfiguration{}, err
		}
		return terminalTOTPConfiguration{
			secret: secret, algorithm: sha1.New, digits: defaultTOTPDigits, period: defaultTOTPPeriod,
		}, nil
	}

	parsed, err := url.Parse(input)
	if err != nil || !strings.EqualFold(parsed.Scheme, "otpauth") || !strings.EqualFold(parsed.Host, "totp") {
		return terminalTOTPConfiguration{}, invalidTerminalTOTPConfiguration()
	}

	secret, err := decodeTerminalTOTPSecret(parsed.Query().Get("secret"))
	if err != nil {
		return terminalTOTPConfiguration{}, err
	}
	algorithm, err := terminalTOTPAlgorithm(parsed.Query().Get("algorithm"))
	if err != nil {
		return terminalTOTPConfiguration{}, err
	}
	digits, err := terminalTOTPParameter(parsed.Query().Get("digits"), defaultTOTPDigits, 4, 10)
	if err != nil {
		return terminalTOTPConfiguration{}, err
	}
	period, err := terminalTOTPParameter(parsed.Query().Get("period"), defaultTOTPPeriod, 1, 300)
	if err != nil {
		return terminalTOTPConfiguration{}, err
	}
	return terminalTOTPConfiguration{secret: secret, algorithm: algorithm, digits: digits, period: period}, nil
}

func shouldAutoRespondTerminalTOTP(questions []string, totpURI *string) bool {
	if len(questions) != 1 || totpURI == nil || strings.TrimSpace(*totpURI) == "" {
		return false
	}
	return isTerminalTOTPQuestion(questions[0])
}

func isTerminalTOTPQuestion(question string) bool {
	question = strings.ToLower(question)
	for _, marker := range []string{
		"otp", "totp", "mfa", "2fa", "one-time", "one time", "verification", "verify",
		"authenticator", "passcode", "token", "验证码", "动态口令", "动态密码", "令牌",
	} {
		if strings.Contains(question, marker) {
			return true
		}
	}
	return false
}

func decodeTerminalTOTPSecret(value string) ([]byte, error) {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	normalized = strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "-", "", "=", "").Replace(normalized)
	if normalized == "" {
		return nil, invalidTerminalTOTPConfiguration()
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(secret) == 0 {
		return nil, invalidTerminalTOTPConfiguration()
	}
	return secret, nil
}

func terminalTOTPAlgorithm(value string) (func() hash.Hash, error) {
	switch strings.ToUpper(strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.TrimSpace(value))) {
	case "", "SHA1":
		return sha1.New, nil
	case "SHA256":
		return sha256.New, nil
	case "SHA512":
		return sha512.New, nil
	default:
		return nil, invalidTerminalTOTPConfiguration()
	}
}

func terminalTOTPParameter(value string, fallback, minimum, maximum int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	if strings.TrimSpace(value) != value {
		return 0, invalidTerminalTOTPConfiguration()
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, invalidTerminalTOTPConfiguration()
	}
	return parsed, nil
}

func invalidTerminalTOTPConfiguration() error {
	return fmt.Errorf("无效的 TOTP 配置")
}
