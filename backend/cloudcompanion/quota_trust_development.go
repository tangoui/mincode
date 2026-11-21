//go:build !production

package cloudcompanion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// developmentQuotaTrust lets development builds verify snapshots from a local
// cloud whose signing key the developer generated. Production builds never
// compile this file and only trust the link-time key.
func developmentQuotaTrust() (keyID, publicKeyBase64 string) {
	path := strings.TrimSpace(os.Getenv("MINCODE_DEV_QUOTA_TRUST_FILE"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ""
		}
		path = filepath.Join(home, ".mincode-cloud-dev", "quota", "trust.json")
	}
	info, err := os.Lstat(path)
	// A trust root that other local users can rewrite is not a trust root.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > 16*1024 {
		return "", ""
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	raw, err := core.ReadAllLimited(file, 16*1024)
	if err != nil {
		return "", ""
	}
	var trust struct {
		KeyID           string `json:"keyId"`
		PublicKeyBase64 string `json:"publicKeyBase64"`
	}
	if json.Unmarshal(raw, &trust) != nil {
		return "", ""
	}
	return trust.KeyID, trust.PublicKeyBase64
}
