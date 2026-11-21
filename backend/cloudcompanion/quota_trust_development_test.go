//go:build !production

package cloudcompanion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentQuotaTrustFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	t.Setenv("MINCODE_DEV_QUOTA_TRUST_FILE", path)
	if id, key := developmentQuotaTrust(); id != "" || key != "" {
		t.Fatal("missing trust file produced a trust root")
	}
	if err := os.WriteFile(path, []byte(`{"keyId":"local-dev","publicKeyBase64":"cHVibGlj"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if id, key := developmentQuotaTrust(); id != "local-dev" || key != "cHVibGlj" {
		t.Fatalf("trust = %q %q", id, key)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if id, _ := developmentQuotaTrust(); id != "" {
		t.Fatal("group/world-writable trust file accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINCODE_DEV_QUOTA_TRUST_FILE", link)
	if id, _ := developmentQuotaTrust(); id != "" {
		t.Fatal("symlinked trust file accepted")
	}

	previousID, previousKey := QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64
	t.Cleanup(func() { QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = previousID, previousKey })
	t.Setenv("MINCODE_DEV_QUOTA_TRUST_FILE", path)
	QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = "release", "not-base64"
	if _, err := pinnedQuotaTrust(); err == nil {
		t.Fatal("an invalid link-time key must not fall back to the development file")
	}
}
