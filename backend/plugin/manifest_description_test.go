package plugin

import (
	"strings"
	"testing"
)

func TestManifestDescriptionLimitIsInBytes(t *testing.T) {
	// 666 three-byte characters plus two ASCII bytes is exactly the limit.
	atLimit := strings.Repeat("描", 666) + "ok"
	if len(atLimit) != maxPluginDescriptionBytes {
		t.Fatalf("fixture is %d bytes", len(atLimit))
	}
	if _, err := readManifest(writeTestManifest(t, func(m *Manifest) { m.Description = atLimit })); err != nil {
		t.Fatalf("description at the limit rejected: %v", err)
	}
	_, err := readManifest(writeTestManifest(t, func(m *Manifest) { m.Description = atLimit + "x" }))
	if err == nil || !strings.Contains(err.Error(), "description or author is too long") {
		t.Fatalf("description over the limit = %v", err)
	}
}
