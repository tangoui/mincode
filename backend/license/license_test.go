package license

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRetiredLicenseCommandsNeverReachTheNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"code":200,"data":{"valid":true}}`))
	}))
	defer server.Close()
	t.Setenv("MINCODE_API_BASE_URL", server.URL)
	t.Setenv("MINCODE_LICENSE_SERVER_URL", server.URL)

	args, _ := json.Marshal(map[string]string{"licenseKey": "legacy-key", "email": "owner@example.com", "serverUrl": server.URL})
	for name, handler := range map[string]func(string, string) (string, error){
		"activate": licenseActivateHandler,
		"verify":   licenseVerifyHandler,
	} {
		result, err := handler("", string(args))
		if !errors.Is(err, ErrLegacyLicenseRetired) || result != "" {
			t.Fatalf("%s = %q, %v; want retired error", name, result, err)
		}
	}

	raw, err := licensePricingHandler("", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var pricing struct {
		Plans   []any `json:"plans"`
		Retired bool  `json:"retired"`
	}
	if err := json.Unmarshal([]byte(raw), &pricing); err != nil || pricing.Plans == nil || len(pricing.Plans) != 0 || !pricing.Retired {
		t.Fatalf("pricing = %s (%v); want empty retired catalog", raw, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("retired license commands sent %d requests", got)
	}
}
