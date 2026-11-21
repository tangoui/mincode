package main

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"strings"

	"mincode/app/backend/core"
)

//go:embed tan.json
var desktopProjectConfig []byte

func init() {
	core.SetDesktopCloudDefault(desktopCloudOrigin(desktopProjectConfig))
}

// Only HTTPS origins are accepted from the bundled configuration. For local
// development, the existing environment override can select a loopback server.
func desktopCloudOrigin(data []byte) string {
	var config struct {
		Cloud struct {
			APIURL string `json:"apiUrl"`
		} `json:"cloud"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(config.Cloud.APIURL))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || core.IsCloudMetadataHost(u.Hostname()) ||
		u.User != nil || u.Opaque != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	return "https://" + u.Host
}
