package mcpauth

import (
	"encoding/json"
	"net/http"
	"testing"
)

// allowHTTP lets a test talk to plain-HTTP httptest servers; production code
// only ever accepts https.
func allowHTTP(t *testing.T) {
	t.Helper()
	previous := allowHTTPForTest
	allowHTTPForTest = true
	t.Cleanup(func() { allowHTTPForTest = previous })
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
