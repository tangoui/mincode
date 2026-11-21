//go:build !production

package core

import "testing"

func TestDevelopmentCloudDefaultMatchesPublicAPI(t *testing.T) {
	if defaultMinCodeCloudBaseURL != DefaultMinCodeAPIBaseURL {
		t.Fatalf("development cloud default = %q", defaultMinCodeCloudBaseURL)
	}
}
