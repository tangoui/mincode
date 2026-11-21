package dbimport

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const heidiSeparator = "<|||>"

// heidiNetTypes maps HeidiSQL's TNetType ordinal to a kind.
var heidiNetTypes = map[int]string{
	0: "mysql", 1: "mysql", 2: "mysql", // TCP/IP, named pipe, SSH tunnel
	3: "sqlserver", 4: "sqlserver", 5: "sqlserver", 6: "sqlserver", 7: "sqlserver",
	8: "postgres", 9: "postgres",
	10: "sqlite",
	11: "mysql",    // ProxySQL admin
	12: "", 13: "", // InterBase (unsupported)
	14: "firebird", 15: "firebird",
}

// parseHeidiPath reads a HeidiSQL "Export settings" text file (also the
// format of portable_settings.txt): lines of Key<|||>Type<|||>Value.
func parseHeidiPath(path string) (parseResult, error) {
	data, err := readBounded(path)
	if err != nil {
		return parseResult{}, err
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) || !bytes.Contains(data, []byte(heidiSeparator)) {
		return parseResult{}, errCode("malformed", nil)
	}
	sessions := map[string]map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), heidiSeparator, 3)
		if len(parts) != 3 || !strings.HasPrefix(parts[0], `Servers\`) {
			continue
		}
		fullKey := strings.TrimPrefix(parts[0], `Servers\`)
		slash := strings.LastIndex(fullKey, `\`)
		if slash <= 0 {
			continue
		}
		sessionPath, key := fullKey[:slash], fullKey[slash+1:]
		values := sessions[sessionPath]
		if values == nil {
			if len(sessions) >= maxCandidates*2 {
				continue
			}
			values = map[string]string{}
			sessions[sessionPath] = values
		}
		values[key] = strings.ReplaceAll(parts[2], "<{{{><}}}>", "\n")
	}
	paths := make([]string, 0, len(sessions))
	for sessionPath, values := range sessions {
		if values["Folder"] == "1" {
			continue
		}
		if _, hasHost := values["Host"]; !hasHost {
			if _, hasType := values["NetType"]; !hasType {
				continue
			}
		}
		paths = append(paths, sessionPath)
	}
	sort.Strings(paths)
	var result parseResult
	for _, sessionPath := range paths {
		result.add(heidiCandidate(sessionPath, sessions[sessionPath]))
	}
	return result, nil
}

func heidiCandidate(sessionPath string, values map[string]string) Candidate {
	segments := strings.Split(sessionPath, `\`)
	netType, _ := strconv.Atoi(values["NetType"])
	kind, known := heidiNetTypes[netType]
	if !known {
		kind = ""
	}
	c := Candidate{
		Name:       segments[len(segments)-1],
		Folder:     strings.Join(segments[:len(segments)-1], "/"),
		SourceType: "HeidiSQL NetType " + strconv.Itoa(netType),
		Host:       values["Hostname"],
		Port:       parsePort(values["Port"]),
		Username:   values["User"],
		SSL:        values["WantSSL"] == "1",
	}
	if c.Host == "" {
		c.Host = values["Host"]
	}
	if databases := values["Databases"]; databases != "" {
		c.Database = strings.TrimSpace(strings.Split(databases, ";")[0])
	}
	if kind == "sqlite" || netType == 13 || netType == 15 {
		// Local file sessions keep the file path(s) in Host, ";"-separated.
		c.FilePath = strings.TrimSpace(strings.Split(c.Host, ";")[0])
		c.Host = ""
		if kind == "firebird" {
			c.Database, c.FilePath = c.FilePath, ""
			c.Host = "localhost"
		}
	}
	if values["WindowsAuth"] == "1" {
		c.PasswordUnavailable = true
	}
	if raw := values["Password"]; raw != "" {
		if plain, ok := decodeHeidiPassword(raw); ok {
			c.setPassword(plain)
		} else {
			c.PasswordUnavailable = true
			c.warn(WarnPasswordUndecrypted)
		}
	}
	if sshHost := values["SSHtunnelHost"]; sshHost != "" && (netType == 2 || netType == 9 || values["SSHtunnelActive"] == "1") {
		tunnel := &Tunnel{
			Type:     "ssh",
			Host:     sshHost,
			Port:     parsePort(values["SSHtunnelHostPort"]),
			Username: values["SSHtunnelUser"],
			KeyPath:  values["SSHtunnelPrivateKey"],
			AuthType: "password",
		}
		if tunnel.KeyPath != "" {
			tunnel.AuthType = "key"
		}
		if raw := values["SSHtunnelPassword"]; raw != "" {
			if plain, ok := decodeHeidiPassword(raw); ok {
				if tunnel.AuthType == "key" {
					tunnel.passphrase = plain
				} else {
					tunnel.password = plain
				}
			} else {
				c.warn(WarnPasswordUndecrypted)
			}
		}
		c.Tunnel = tunnel
	}
	c.applyKind(kind)
	return c
}

// decodeHeidiPassword reverses HeidiSQL's obfuscation: hex pairs of
// (char + salt) followed by the single-digit salt.
func decodeHeidiPassword(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value)%2 == 0 {
		return "", false
	}
	salt, err := strconv.Atoi(value[len(value)-1:])
	if err != nil {
		return "", false
	}
	body := value[:len(value)-1]
	out := make([]byte, 0, len(body)/2)
	for i := 0; i < len(body); i += 2 {
		code, err := strconv.ParseUint(body[i:i+2], 16, 8)
		if err != nil || int(code) < salt {
			return "", false
		}
		out = append(out, byte(int(code)-salt))
	}
	if !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}
