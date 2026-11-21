package dbimport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/blowfish"
)

// Navicat password keys are fixed, publicly documented constants of the
// Navicat client (not user secrets).
const (
	navicatAESKey       = "libcckeylibcckey"
	navicatAESIV        = "libcciv libcciv "
	navicatBlowfishSeed = "3DC5CA39"
)

type navicatConnection struct {
	Attrs []xml.Attr `xml:",any,attr"`
}

type navicatFile struct {
	XMLName     xml.Name            `xml:"Connections"`
	Version     string              `xml:"Ver,attr"`
	Connections []navicatConnection `xml:"Connection"`
}

func parseNavicatPath(path string) (parseResult, error) {
	data, err := readBounded(path)
	if err != nil {
		return parseResult{}, err
	}
	var file navicatFile
	if err := decodeXML(data, &file); err != nil {
		return parseResult{}, errCode("malformed", nil)
	}
	preferBlowfish := false
	if version, err := strconv.ParseFloat(strings.TrimSpace(file.Version), 64); err == nil && version < 1.2 {
		preferBlowfish = true
	}
	var result parseResult
	for _, entry := range file.Connections {
		attrs := make(map[string]string, len(entry.Attrs))
		for _, attr := range entry.Attrs {
			attrs[strings.ToLower(attr.Name.Local)] = attr.Value
		}
		result.add(navicatCandidate(attrs, preferBlowfish))
	}
	return result, nil
}

func navicatCandidate(attrs map[string]string, preferBlowfish bool) Candidate {
	connType := attrs["conntype"]
	c := Candidate{
		Name:       attrs["connectionname"],
		SourceType: connType,
		Host:       attrs["host"],
		Port:       parsePort(attrs["port"]),
		Database:   firstNonEmpty(attrs["database"], attrs["initialdatabase"], attrs["servicename"], attrs["sid"]),
		Username:   attrs["username"],
		FilePath:   firstNonEmpty(attrs["databasefilename"], attrs["databasefile"]),
		AuthSource: firstNonEmpty(attrs["authsource"], attrs["authenticationdatabase"], attrs["authdatabase"]),
		SSL:        isTruthy(attrs["ssl"]),
	}
	passwordKnown := false
	if raw := attrs["password"]; raw != "" && !strings.EqualFold(attrs["savepassword"], "false") {
		if plain, ok := decryptNavicatPassword(raw, preferBlowfish); ok {
			c.setPassword(plain)
			passwordKnown = true
		} else {
			c.warn(WarnPasswordUndecrypted)
		}
	}
	if isTruthy(attrs["ssh"]) {
		tunnel := &Tunnel{
			Type:     "ssh",
			Host:     attrs["ssh_host"],
			Port:     parsePort(attrs["ssh_port"]),
			Username: attrs["ssh_username"],
			AuthType: "password",
			KeyPath:  attrs["ssh_privatekey"],
		}
		if method := strings.ToUpper(attrs["ssh_authenmethod"]); strings.Contains(method, "KEY") || tunnel.KeyPath != "" {
			tunnel.AuthType = "key"
		}
		if raw := attrs["ssh_password"]; raw != "" && tunnel.AuthType == "password" && !strings.EqualFold(attrs["ssh_savepassword"], "false") {
			if plain, ok := decryptNavicatPassword(raw, preferBlowfish); ok {
				tunnel.password = plain
			} else {
				c.warn(WarnPasswordUndecrypted)
			}
		}
		if raw := attrs["ssh_passphrase"]; raw != "" && tunnel.AuthType == "key" && !strings.EqualFold(attrs["ssh_savepassphrase"], "false") {
			if plain, ok := decryptNavicatPassword(raw, preferBlowfish); ok {
				tunnel.passphrase = plain
			} else {
				c.warn(WarnPasswordUndecrypted)
			}
		}
		c.Tunnel = tunnel
	}
	if isTruthy(attrs["http"]) {
		c.warn(WarnTunnelUnsupported)
	}
	c.applyKind(resolveKind(connType))
	// Without a decrypted password a non-file connection asks for one; an
	// empty password is not distinguished from one that was not exported.
	c.PasswordUnavailable = !passwordKnown && !fileTypes[c.DbType]
	return c
}

// decryptNavicatPassword decodes a hex Navicat password, trying the Navicat
// 12+ AES scheme and the Navicat 11 Blowfish scheme (order by file version).
func decryptNavicatPassword(value string, preferBlowfish bool) (string, bool) {
	data, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(data) == 0 {
		return "", false
	}
	attempts := []func([]byte) ([]byte, error){navicatAESDecrypt, navicatBlowfishDecrypt}
	if preferBlowfish {
		attempts[0], attempts[1] = attempts[1], attempts[0]
	}
	for _, attempt := range attempts {
		plain, err := attempt(data)
		if err == nil && utf8.Valid(plain) {
			return string(plain), true
		}
	}
	return "", false
}

func navicatAESDecrypt(data []byte) ([]byte, error) {
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("navicat aes: invalid length")
	}
	block, err := aes.NewCipher([]byte(navicatAESKey))
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, []byte(navicatAESIV)).CryptBlocks(plain, data)
	return pkcs7Unpad(plain, aes.BlockSize)
}

func navicatBlowfishCipher() (*blowfish.Cipher, []byte, error) {
	key := sha1.Sum([]byte(navicatBlowfishSeed))
	block, err := blowfish.NewCipher(key[:])
	if err != nil {
		return nil, nil, err
	}
	iv := make([]byte, blowfish.BlockSize)
	for i := range iv {
		iv[i] = 0xFF
	}
	block.Encrypt(iv, iv)
	return block, iv, nil
}

func navicatBlowfishDecrypt(data []byte) ([]byte, error) {
	block, iv, err := navicatBlowfishCipher()
	if err != nil {
		return nil, err
	}
	vector := append([]byte(nil), iv...)
	out := make([]byte, 0, len(data))
	full := len(data) / blowfish.BlockSize * blowfish.BlockSize
	chunk := make([]byte, blowfish.BlockSize)
	for offset := 0; offset < full; offset += blowfish.BlockSize {
		encrypted := data[offset : offset+blowfish.BlockSize]
		block.Decrypt(chunk, encrypted)
		for i := range chunk {
			chunk[i] ^= vector[i]
			vector[i] ^= encrypted[i]
		}
		out = append(out, chunk...)
	}
	if rest := data[full:]; len(rest) > 0 {
		block.Encrypt(vector, vector)
		for i := range rest {
			out = append(out, rest[i]^vector[i])
		}
	}
	return out, nil
}
