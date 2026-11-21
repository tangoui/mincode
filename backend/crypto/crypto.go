// Crypto 域：字段级 / 对象级 AES-256-GCM 加解密，密钥由机器指纹经 HKDF-SHA256 派生。
//
// 与 Tauri src-tauri/src/crypto.rs 对齐：
//   - 密文格式 enc:v2:<base64(nonce(12) ‖ ciphertext)>
//   - HKDF-SHA256(IKM=deviceFingerprint, salt=每机随机32B, info="mincode-aes-gcm-key-v2")
//   - salt 持久化于 <dataDir>/com.mincode.desktop/.key_salt
//
// 命令契约（入参 camelCase，返回裸字符串）：
//
//	encrypt_field {plain}   -> string
//	decrypt_field {cipher}  -> string
//	encrypt_fields {plains}  -> string[]（与入参一一对应；任一失败则整体失败）
//	decrypt_fields {ciphers} -> [{value} | {error}]（逐项容错，一个损坏的字段不影响其它字段）
//	seal_object   {json}    -> string
//	unseal_object {cipher}  -> string
package crypto

import (
	"mincode/app/backend/core"

	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	encPrefixV2      = "enc:v2:"
	hkdfInfo         = "mincode-aes-gcm-key-v2"
	saltLen          = 32
	maxDecryptLayers = 4
	// 批量字段数上限：覆盖数百个连接 × 多个凭据字段，同时拒绝异常巨大的单次请求。
	maxBatchFields = 10000
)

func init() {
	core.Register("encrypt_field", encryptFieldHandler)
	core.Register("decrypt_field", decryptFieldHandler)
	core.Register("encrypt_fields", encryptFieldsHandler)
	core.Register("decrypt_fields", decryptFieldsHandler)
	core.Register("seal_object", sealObjectHandler)
	core.Register("unseal_object", unsealObjectHandler)
}

// ---- 命令处理器 ----

type plainArg struct {
	Plain string `json:"plain"`
}

func encryptFieldHandler(_, argsJson string) (string, error) {
	var a plainArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Plain == "" || isEncryptedValue(a.Plain) {
		return core.EncodeResult(a.Plain)
	}
	out, err := encryptValue(a.Plain)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

type cipherArg struct {
	Cipher string `json:"cipher"`
}

func decryptFieldHandler(_, argsJson string) (string, error) {
	var a cipherArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Cipher == "" {
		return core.EncodeResult("")
	}
	out, err := decryptValue(a.Cipher)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

type plainsArg struct {
	Plains []string `json:"plains"`
}

// encryptFieldsHandler 在一次 IPC 中加密多个字段，语义与逐个调用 encrypt_field 相同。
func encryptFieldsHandler(_, argsJson string) (string, error) {
	var a plainsArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if len(a.Plains) > maxBatchFields {
		return "", fmt.Errorf("encrypt_fields: 字段数 %d 超过上限 %d", len(a.Plains), maxBatchFields)
	}
	out := make([]string, len(a.Plains))
	for i, plain := range a.Plains {
		if plain == "" || isEncryptedValue(plain) {
			out[i] = plain
			continue
		}
		encrypted, err := encryptValue(plain)
		if err != nil {
			return "", fmt.Errorf("encrypt_fields[%d]: %w", i, err)
		}
		out[i] = encrypted
	}
	return core.EncodeResult(out)
}

type ciphersArg struct {
	Ciphers []string `json:"ciphers"`
}

type decryptFieldResult struct {
	Value string `json:"value"`
	Error string `json:"error,omitempty"`
}

// decryptFieldsHandler 在一次 IPC 中解密多个字段，逐项返回结果或错误。
func decryptFieldsHandler(_, argsJson string) (string, error) {
	var a ciphersArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if len(a.Ciphers) > maxBatchFields {
		return "", fmt.Errorf("decrypt_fields: 字段数 %d 超过上限 %d", len(a.Ciphers), maxBatchFields)
	}
	out := make([]decryptFieldResult, len(a.Ciphers))
	for i, value := range a.Ciphers {
		if value == "" {
			continue
		}
		plain, err := decryptValue(value)
		if err != nil {
			out[i] = decryptFieldResult{Error: err.Error()}
			continue
		}
		out[i] = decryptFieldResult{Value: plain}
	}
	return core.EncodeResult(out)
}

type jsonArg struct {
	JSON string `json:"json"`
}

func sealObjectHandler(_, argsJson string) (string, error) {
	var a jsonArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.JSON == "" {
		return "", errors.New("seal_object: empty input")
	}
	out, err := encryptValue(a.JSON)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

func unsealObjectHandler(_, argsJson string) (string, error) {
	var a cipherArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Cipher == "" {
		return "", errors.New("unseal_object: empty input")
	}
	if !strings.HasPrefix(a.Cipher, encPrefixV2) {
		return "", errors.New("unseal_object: expected enc:v2 ciphertext")
	}
	out, err := decryptValue(a.Cipher)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

// ---- 密钥派生与加解密核心 ----

func isEncryptedValue(v string) bool {
	return strings.HasPrefix(v, encPrefixV2)
}

var (
	machineKeyMu    sync.Mutex
	machineKeyV2    [32]byte
	machineKeyReady bool
	userDataDir     = core.UserDataDir
)

// machineKey 只缓存成功派生的密钥；失败（如 .key_salt 暂时不可读写）不缓存，
// 下次调用会重新派生，避免一次瞬时错误让本进程内所有加密写入失败直到重启。
func machineKey() ([32]byte, error) {
	machineKeyMu.Lock()
	defer machineKeyMu.Unlock()
	if machineKeyReady {
		return machineKeyV2, nil
	}
	key, err := deriveMachineKeyV2(core.DeviceFingerprint())
	if err != nil {
		return [32]byte{}, err
	}
	machineKeyV2, machineKeyReady = key, true
	return machineKeyV2, nil
}

func deriveMachineKeyV2(fingerprint string) ([32]byte, error) {
	salt, err := loadOrCreateInstallSalt()
	if err != nil {
		return [32]byte{}, err
	}
	prk := hkdfExtract(salt[:], []byte(fingerprint))
	okm := hkdfExpand(prk, []byte(hkdfInfo), 32)
	var out [32]byte
	copy(out[:], okm)
	return out, nil
}

// HKDF-SHA256 Extract：PRK = HMAC(salt, IKM)
func hkdfExtract(salt, ikm []byte) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

// HKDF-SHA256 Expand：生成 length 字节输出密钥材料
func hkdfExpand(prk, info []byte, length int) []byte {
	var out []byte
	var prev []byte
	counter := byte(1)
	for len(out) < length {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{counter})
		prev = mac.Sum(nil)
		out = append(out, prev...)
		counter++
	}
	return out[:length]
}

// EncryptForStorage 使用机器派生密钥加密内部敏感状态，供可信后端子包复用。
func EncryptForStorage(plain string) (string, error) {
	return encryptValue(plain)
}

// DecryptFromStorage 解密由 EncryptForStorage 写入的内部敏感状态。
func DecryptFromStorage(value string) (string, error) {
	return decryptValue(value)
}

func encryptValue(plain string) (string, error) {
	key, err := machineKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", errors.New("AES key error: " + err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("GCM init error: " + err.Error())
	}
	nonce := make([]byte, gcm.NonceSize()) // 12
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("nonce error: " + err.Error())
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plain), nil)

	combined := make([]byte, 0, len(nonce)+len(ciphertext))
	combined = append(combined, nonce...)
	combined = append(combined, ciphertext...)

	b64 := base64.StdEncoding.EncodeToString(combined)
	return encPrefixV2 + b64, nil
}

func decryptValue(value string) (string, error) {
	current := value
	for i := 0; i < maxDecryptLayers; i++ {
		if !isEncryptedValue(current) {
			return current, nil
		}
		next, err := decryptSingleLayer(current)
		if err != nil {
			return "", err
		}
		current = next
	}
	if isEncryptedValue(current) {
		return "", errors.New("decrypt failed: nested ciphertext exceeds max depth")
	}
	return current, nil
}

func decryptSingleLayer(value string) (string, error) {
	b64 := strings.TrimPrefix(value, encPrefixV2)
	combined, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", errors.New("base64 decode error: " + err.Error())
	}
	if len(combined) < 13 {
		return "", errors.New("invalid ciphertext: too short")
	}
	key, err := machineKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", errors.New("AES key error: " + err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.New("GCM init error: " + err.Error())
	}
	ns := gcm.NonceSize()
	if len(combined) < ns {
		return "", errors.New("invalid ciphertext: too short")
	}
	nonce, ciphertext := combined[:ns], combined[ns:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("decrypt failed: wrong key or corrupted data")
	}
	return string(plaintext), nil
}

// DecryptOptional 透明解密一个可选字段：若为密文则解密，否则原样返回。
// 供其它子包（如 db）在建立连接前解密密码 / token 使用。
func DecryptOptional(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	if !isEncryptedValue(*v) {
		return v, nil
	}
	plain, err := decryptValue(*v)
	if err != nil {
		return nil, err
	}
	return &plain, nil
}

// ---- salt 持久化 ----

func loadOrCreateInstallSalt() ([saltLen]byte, error) {
	var salt [saltLen]byte
	path := saltPath()
	if path == "" {
		return salt, errors.New("加密密钥目录不可用")
	}
	if loaded, err := readSalt(path); err == nil {
		return loaded, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return salt, err
	}

	// 首次启动：生成并持久化
	if _, err := rand.Read(salt[:]); err != nil {
		return salt, fmt.Errorf("生成加密盐值: %w", err)
	}
	if err := writeSaltAtomically(path, salt); err != nil {
		return [saltLen]byte{}, err
	}
	return salt, nil
}

func readSalt(path string) ([saltLen]byte, error) {
	var salt [saltLen]byte
	data, err := os.ReadFile(path)
	if err != nil {
		return salt, err
	}
	if len(data) != saltLen {
		return salt, fmt.Errorf("加密盐值长度异常: %s", path)
	}
	copy(salt[:], data)
	return salt, nil
}

func writeSaltAtomically(path string, salt [saltLen]byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建加密密钥目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".key_salt-*.tmp")
	if err != nil {
		return fmt.Errorf("创建加密盐值临时文件: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置加密盐值权限: %w", err)
	}
	if _, err := tmp.Write(salt[:]); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入加密盐值: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步加密盐值: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭加密盐值: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换加密盐值: %w", err)
	}
	return nil
}

func saltPath() string {
	dir := userDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "com.mincode.desktop", ".key_salt")
}
