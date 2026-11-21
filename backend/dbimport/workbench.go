package dbimport

import (
	"path/filepath"
	"strings"
)

// parseWorkbenchPath reads MySQL Workbench connections.xml (the file or the
// folder holding it). Workbench stores passwords in the OS keychain /
// its own vault, so they are never importable.
func parseWorkbenchPath(path string) (parseResult, error) {
	file := path
	if !isFile(path) {
		file = filepath.Join(path, "connections.xml")
	}
	data, err := readBounded(file)
	if err != nil {
		return parseResult{}, err
	}
	root, err := parseXMLTree(data)
	if err != nil {
		return parseResult{}, errCode("malformed", nil)
	}
	if len(root.Children) == 0 || root.Children[0].Name != "data" {
		return parseResult{}, errCode("malformed", nil)
	}
	var result parseResult
	root.walk(func(node *xmlNode) {
		if node.Name == "value" && node.attr("struct-name") == "db.mgmt.Connection" {
			result.add(workbenchCandidate(node))
		}
	})
	return result, nil
}

// workbenchValues flattens the keyed children of a GRT object/dict.
func workbenchValues(node *xmlNode) map[string]string {
	values := map[string]string{}
	if node == nil {
		return values
	}
	for _, child := range node.Children {
		if key := child.attr("key"); key != "" {
			values[key] = strings.TrimSpace(child.Text)
		}
	}
	return values
}

func workbenchCandidate(node *xmlNode) Candidate {
	top := workbenchValues(node)
	var params map[string]string
	for _, child := range node.Children {
		if child.attr("key") == "parameterValues" {
			params = workbenchValues(child)
		}
	}
	if params == nil {
		params = map[string]string{}
	}
	driver := strings.ToLower(top["driver"])
	c := Candidate{
		Name:                top["name"],
		SourceType:          "MySQL Workbench",
		Host:                params["hostName"],
		Port:                parsePort(params["port"]),
		Database:            params["schema"],
		Username:            params["userName"],
		SSL:                 params["useSSL"] != "" && params["useSSL"] != "0",
		PasswordUnavailable: true,
	}
	if strings.Contains(driver, "socket") {
		c.warn(WarnSocketUnsupported)
	}
	if strings.Contains(driver, "sshtun") {
		host, port := splitHostPort(params["sshHost"])
		if host == "" {
			host = params["sshHost"]
		}
		tunnel := &Tunnel{
			Type:     "ssh",
			Host:     host,
			Port:     port,
			Username: params["sshUserName"],
			KeyPath:  params["sshKeyFile"],
			AuthType: "password",
		}
		if tunnel.KeyPath != "" {
			tunnel.AuthType = "key"
		}
		c.Tunnel = tunnel
	}
	c.applyKind("mysql")
	return c
}
