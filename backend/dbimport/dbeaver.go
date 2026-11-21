package dbimport

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// dbeaverCredentialsKey is DBeaver's built-in default key for
// credentials-config.json (publicly documented; it protects against casual
// reading only, not a secret of any user).
const dbeaverCredentialsKey = "babb4a9f774ab853c96c2d653dfe544a"

// flexString accepts JSON strings, numbers and booleans.
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*f = flexString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*f = flexString(number.String())
		return nil
	}
	var flag bool
	if err := json.Unmarshal(data, &flag); err == nil {
		*f = flexString(strconv.FormatBool(flag))
		return nil
	}
	*f = ""
	return nil
}

type dbeaverHandler struct {
	Type         string                `json:"type"`
	Enabled      bool                  `json:"enabled"`
	SavePassword bool                  `json:"save-password"`
	User         flexString            `json:"user"`
	Password     flexString            `json:"password"`
	Properties   map[string]flexString `json:"properties"`
}

type dbeaverConnection struct {
	Provider      string `json:"provider"`
	Driver        string `json:"driver"`
	Name          string `json:"name"`
	Folder        string `json:"folder"`
	SavePassword  bool   `json:"save-password"`
	Configuration struct {
		Host       flexString                `json:"host"`
		Port       flexString                `json:"port"`
		Server     flexString                `json:"server"`
		Database   flexString                `json:"database"`
		URL        flexString                `json:"url"`
		User       flexString                `json:"user"`
		Password   flexString                `json:"password"`
		AuthModel  string                    `json:"auth-model"`
		Handlers   map[string]dbeaverHandler `json:"handlers"`
		Properties map[string]flexString     `json:"properties"`
	} `json:"configuration"`
}

type dbeaverDataSources struct {
	Connections map[string]dbeaverConnection `json:"connections"`
}

type dbeaverSecret struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// parseDBeaverPath accepts data-sources*.json, the .dbeaver folder, a project
// folder (containing .dbeaver) or a workspace folder (containing General).
func parseDBeaverPath(path string) (parseResult, error) {
	dir := path
	if isFile(path) {
		dir = filepath.Dir(path)
	} else {
		for _, candidate := range []string{path, filepath.Join(path, ".dbeaver"), filepath.Join(path, "General", ".dbeaver")} {
			if len(globBounded(candidate, "data-sources*.json")) > 0 {
				dir = candidate
				break
			}
		}
	}
	files := globBounded(dir, "data-sources*.json")
	if isFile(path) {
		files = []string{path}
	}
	if len(files) == 0 {
		return parseResult{}, errCode("not_found", nil)
	}
	var result parseResult
	credentialsPath := filepath.Join(dir, "credentials-config.json")
	secrets, credentialWarning := readDBeaverCredentials(credentialsPath)
	if credentialWarning != "" {
		result.warn(credentialWarning)
	}
	parsedAny := false
	expectsCredentials := false
	for _, file := range files {
		data, err := readBounded(file)
		if err != nil {
			return parseResult{}, err
		}
		var sources dbeaverDataSources
		if err := json.Unmarshal(data, &sources); err != nil {
			if len(files) == 1 {
				return parseResult{}, errCode("malformed", nil)
			}
			result.warn(WarnSkippedEntries)
			continue
		}
		parsedAny = true
		ids := make([]string, 0, len(sources.Connections))
		for id := range sources.Connections {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			source := sources.Connections[id]
			candidate := dbeaverCandidate(id, source, secrets[id])
			if source.SavePassword && source.Configuration.Password == "" && !fileTypes[candidate.DbType] {
				expectsCredentials = true
			}
			result.add(candidate)
		}
	}
	if !parsedAny {
		return parseResult{}, errCode("malformed", nil)
	}
	if expectsCredentials && !isFile(credentialsPath) {
		result.warn(WarnCredentialsMissing)
	}
	return result, nil
}

func dbeaverCandidate(id string, source dbeaverConnection, secrets map[string]dbeaverSecret) Candidate {
	cfg := source.Configuration
	c := Candidate{
		Name:       source.Name,
		Folder:     source.Folder,
		SourceType: strings.Trim(source.Provider+" / "+source.Driver, " /"),
		Host:       string(cfg.Host),
		Port:       parsePort(string(cfg.Port)),
		Database:   string(cfg.Database),
		Username:   firstNonEmpty(secrets["#connection"].User, string(cfg.User)),
	}
	if c.Name == "" {
		c.Name = id
	}
	if source.SavePassword {
		c.setPassword(firstNonEmpty(secrets["#connection"].Password, string(cfg.Password)))
	}
	kind := resolveKind(source.Driver, source.Provider)
	if url := string(cfg.URL); url != "" {
		info, ok := parseJDBCURL(url)
		if kind == "" {
			kind = info.Kind
		}
		if ok {
			c.Host = firstNonEmpty(c.Host, info.Host)
			if c.Port == 0 {
				c.Port = info.Port
			}
			c.Database = firstNonEmpty(c.Database, info.Database)
			c.FilePath = info.FilePath
			c.Username = firstNonEmpty(c.Username, info.User)
			c.SSL = c.SSL || info.SSL
		} else if c.Host == "" && c.Database == "" {
			c.warn(WarnURLUnparsed)
		}
	}
	for _, key := range []string{"ssl", "useSSL", "sslmode"} {
		if value, ok := cfg.Properties[key]; ok && (isTruthy(string(value)) || sslFromParams(map[string]string{"sslmode": string(value)})) {
			c.SSL = true
		}
	}
	handlerIDs := make([]string, 0, len(cfg.Handlers))
	for handlerID := range cfg.Handlers {
		handlerIDs = append(handlerIDs, handlerID)
	}
	sort.Strings(handlerIDs)
	var sshTunnel, proxy *Tunnel
	for _, handlerID := range handlerIDs {
		handler := cfg.Handlers[handlerID]
		if !handler.Enabled {
			continue
		}
		lowerID := strings.ToLower(handlerID)
		switch {
		case strings.Contains(lowerID, "ssl"):
			c.SSL = true
		case lowerID == "ssh_tunnel" || strings.EqualFold(handler.Type, "TUNNEL"):
			if sshTunnel != nil {
				c.warn(WarnTunnelUnsupported)
				continue
			}
			sshTunnel = dbeaverSSHTunnel(handler, secrets["network/"+handlerID])
		case lowerID == "proxy" || strings.EqualFold(handler.Type, "PROXY"):
			if proxy != nil {
				c.warn(WarnTunnelUnsupported)
				continue
			}
			proxy = dbeaverProxy(handler, secrets["network/"+handlerID])
		}
	}
	// A connection keeps one tunnel; SSH wins over a proxy.
	switch {
	case sshTunnel != nil:
		c.Tunnel = sshTunnel
		if proxy != nil {
			c.warn(WarnTunnelPartial)
		}
	case proxy != nil:
		c.Tunnel = proxy
	}
	c.applyKind(kind)
	if !c.HasPassword && !fileTypes[c.DbType] {
		// Without a decrypted #connection entry the password may live
		// elsewhere (secure storage) or be missing, so the user is asked.
		_, saved := secrets["#connection"]
		c.PasswordUnavailable = !source.SavePassword || !saved
	}
	return c
}

func dbeaverSSHTunnel(handler dbeaverHandler, secret dbeaverSecret) *Tunnel {
	props := handler.Properties
	tunnel := &Tunnel{
		Type:     "ssh",
		Host:     string(props["host"]),
		Port:     parsePort(string(props["port"])),
		Username: firstNonEmpty(secret.User, string(handler.User), string(props["user"])),
		KeyPath:  firstNonEmpty(string(props["keyPath"]), string(props["key-path"])),
		AuthType: "password",
	}
	switch strings.ToUpper(string(props["authType"])) {
	case "PUBLIC_KEY", "KEY", "AGENT":
		tunnel.AuthType = "key"
	}
	if tunnel.KeyPath != "" {
		tunnel.AuthType = "key"
	}
	if handler.SavePassword {
		value := firstNonEmpty(secret.Password, string(handler.Password))
		if tunnel.AuthType == "key" {
			tunnel.passphrase = value
		} else {
			tunnel.password = value
		}
	}
	return tunnel
}

func dbeaverProxy(handler dbeaverHandler, secret dbeaverSecret) *Tunnel {
	props := handler.Properties
	tunnel := &Tunnel{
		Type:     "socks5",
		Host:     firstNonEmpty(string(props["socks-host"]), string(props["host"])),
		Port:     parsePort(firstNonEmpty(string(props["socks-port"]), string(props["port"]))),
		Username: firstNonEmpty(secret.User, string(handler.User), string(props["socks-user"])),
	}
	if handler.SavePassword {
		tunnel.password = firstNonEmpty(secret.Password, string(handler.Password))
	}
	return tunnel
}

// readDBeaverCredentials decrypts credentials-config.json. A missing file
// yields no secrets (the caller warns when a connection expected some); an
// unreadable one yields a warning code.
func readDBeaverCredentials(path string) (map[string]map[string]dbeaverSecret, string) {
	if !isFile(path) {
		return nil, ""
	}
	data, err := readBounded(path)
	if err != nil {
		return nil, WarnCredentialsInvalid
	}
	plain, err := decryptDBeaverCredentials(data)
	if err != nil {
		return nil, WarnCredentialsInvalid
	}
	var secrets map[string]map[string]dbeaverSecret
	if err := json.Unmarshal(plain, &secrets); err != nil {
		return nil, WarnCredentialsInvalid
	}
	return secrets, ""
}

func decryptDBeaverCredentials(data []byte) ([]byte, error) {
	key, _ := hex.DecodeString(dbeaverCredentialsKey)
	if len(data) < 2*aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("credentials: invalid length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv, body := data[:aes.BlockSize], data[aes.BlockSize:]
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, body)
	return pkcs7Unpad(plain, aes.BlockSize)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("padding: invalid length")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("padding: invalid")
	}
	if !bytes.Equal(data[len(data)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, fmt.Errorf("padding: invalid")
	}
	return data[:len(data)-pad], nil
}
