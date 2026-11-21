package dbimport

import (
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir is swapped in tests.
var userHomeDir = os.UserHomeDir

// parseDataGripPath accepts dataSources.xml, the folder holding it (.idea or
// a JetBrains options folder) or a project folder containing .idea. The
// sibling dataSources.local.xml (user names, SSH/SSL flags) and sshConfigs.xml
// are read when present. JetBrains keeps passwords in the OS keychain or
// KeePass, so every row is marked password-unavailable.
func parseDataGripPath(path string) (parseResult, error) {
	file := path
	if !isFile(path) {
		file = ""
		for _, dir := range []string{path, filepath.Join(path, ".idea"), filepath.Join(path, "options")} {
			if isFile(filepath.Join(dir, "dataSources.xml")) {
				file = filepath.Join(dir, "dataSources.xml")
				break
			}
		}
		if file == "" {
			return parseResult{}, errCode("not_found", nil)
		}
	}
	data, err := readBounded(file)
	if err != nil {
		return parseResult{}, err
	}
	root, err := parseXMLTree(data)
	if err != nil {
		return parseResult{}, errCode("malformed", nil)
	}
	dir := filepath.Dir(file)
	local := map[string]*xmlNode{}
	var result parseResult
	if localPath := filepath.Join(dir, "dataSources.local.xml"); isFile(localPath) && filepath.Base(file) != "dataSources.local.xml" {
		if localData, err := readBounded(localPath); err == nil {
			if localRoot, err := parseXMLTree(localData); err == nil {
				indexDataSources(localRoot, local)
			} else {
				result.warn(WarnSkippedEntries)
			}
		}
	}
	sshConfigs := map[string]*xmlNode{}
	if sshPath := filepath.Join(dir, "sshConfigs.xml"); isFile(sshPath) {
		if sshData, err := readBounded(sshPath); err == nil {
			if sshRoot, err := parseXMLTree(sshData); err == nil {
				sshRoot.walk(func(node *xmlNode) {
					if (strings.EqualFold(node.Name, "sshConfig") || strings.EqualFold(node.Name, "ssh-config")) && node.attr("id") != "" {
						sshConfigs[node.attr("id")] = node
					}
				})
			}
		}
	}
	found := false
	root.walk(func(node *xmlNode) {
		if node.Name != "data-source" {
			return
		}
		found = true
		result.add(dataGripCandidate(node, local[dataSourceKey(node)], sshConfigs))
	})
	if !found && !strings.Contains(string(data), "DataSourceManager") && !strings.Contains(string(data), "dataSourceStorage") {
		return parseResult{}, errCode("malformed", nil)
	}
	return result, nil
}

func dataSourceKey(node *xmlNode) string {
	return firstNonEmpty(node.attr("uuid"), node.attr("name"))
}

func indexDataSources(root *xmlNode, index map[string]*xmlNode) {
	root.walk(func(node *xmlNode) {
		if node.Name == "data-source" {
			if key := dataSourceKey(node); key != "" {
				index[key] = node
			}
		}
	})
}

func dataGripCandidate(node, local *xmlNode, sshConfigs map[string]*xmlNode) Candidate {
	driverRef := node.childText("driver-ref")
	c := Candidate{
		Name:                node.attr("name"),
		Folder:              firstNonEmpty(node.attr("group"), node.attr("group-name"), node.childText("group")),
		SourceType:          firstNonEmpty(driverRef, node.childText("jdbc-driver")),
		Username:            node.childText("user-name"),
		PasswordUnavailable: true,
	}
	if local != nil {
		c.Username = firstNonEmpty(local.childText("user-name"), c.Username)
	}
	kind := resolveKind(driverRef)
	if url := node.childText("jdbc-url"); url != "" {
		info, ok := parseJDBCURL(url)
		if kind == "" {
			kind = info.Kind
		}
		if ok {
			c.Host, c.Port, c.Database, c.FilePath = info.Host, info.Port, info.Database, info.FilePath
			c.Username = firstNonEmpty(c.Username, info.User)
			c.SSL = info.SSL
			c.AuthSource = info.Params["authsource"]
			if info.Password != "" {
				c.setPassword(info.Password)
				c.PasswordUnavailable = false
			}
		} else {
			c.warn(WarnURLUnparsed)
		}
	}
	for _, holder := range []*xmlNode{node, local} {
		if holder == nil {
			continue
		}
		if ssl := holder.child("ssl-config"); ssl != nil && !strings.EqualFold(ssl.childText("enabled"), "false") {
			c.SSL = true
		}
		if ssh := holder.child("ssh-properties"); ssh != nil && isTruthy(ssh.childText("enabled")) {
			c.Tunnel = dataGripTunnel(ssh, sshConfigs)
		}
	}
	c.applyKind(kind)
	return c
}

func dataGripTunnel(ssh *xmlNode, sshConfigs map[string]*xmlNode) *Tunnel {
	tunnel := &Tunnel{Type: "ssh", AuthType: "password"}
	source := ssh
	if config := sshConfigs[ssh.childText("ssh-config-id")]; config != nil {
		tunnel.Host = config.attr("host")
		tunnel.Port = parsePort(config.attr("port"))
		tunnel.Username = firstNonEmpty(config.attr("username"), config.attr("user"))
		tunnel.KeyPath = expandJetBrainsPath(config.attr("keyPath"))
		if authType := strings.ToUpper(config.attr("authType")); authType == "KEY_PAIR" || authType == "OPEN_SSH" || tunnel.KeyPath != "" {
			tunnel.AuthType = "key"
		}
		return tunnel
	}
	// Legacy inline form (<host>, <port>, <user>, <auth-type>, <key-path>).
	tunnel.Host = source.childText("host")
	tunnel.Port = parsePort(source.childText("port"))
	tunnel.Username = firstNonEmpty(source.childText("user"), source.childText("user-name"))
	tunnel.KeyPath = expandJetBrainsPath(source.childText("key-path"))
	if strings.Contains(strings.ToUpper(source.childText("auth-type")), "KEY") || tunnel.KeyPath != "" {
		tunnel.AuthType = "key"
	}
	return tunnel
}

// expandJetBrainsPath resolves the $USER_HOME$ macro JetBrains stores in
// paths; other values, or an unknown home directory, are kept as they are.
func expandJetBrainsPath(value string) string {
	rest, ok := strings.CutPrefix(value, "$USER_HOME$")
	if !ok {
		return value
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return value
	}
	return filepath.Join(home, filepath.FromSlash(rest))
}
