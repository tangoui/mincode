//go:build production

package core

import "testing"

func TestProductionCloudDefaultMatchesPublicAPI(t *testing.T) {
	if defaultMinCodeCloudBaseURL != DefaultMinCodeAPIBaseURL {
		t.Fatalf("production cloud default = %q, want %q", defaultMinCodeCloudBaseURL, DefaultMinCodeAPIBaseURL)
	}
}
