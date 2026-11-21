package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// contributionFixtureRoot is the corpus shared with the cloud marketplace and
// the Node contract test (tools/verify/fixtures/contributions).
var contributionFixtureRoot = filepath.Join("..", "..", "tools", "verify", "fixtures", "contributions")

type contributionFixtureCase struct {
	Points     map[string]int `json:"points"`
	RejectedBy []string       `json:"rejectedBy"`
	Contains   string         `json:"contains"`
}

func loadContributionFixtures(t *testing.T) map[string]contributionFixtureCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contributionFixtureRoot, "expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expectations struct {
		Cases map[string]contributionFixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &expectations); err != nil {
		t.Fatal(err)
	}
	return expectations.Cases
}

// TestContributionFixtures checks the host side of the shared corpus: valid
// cases pass strict install inspection; invalid cases are rejected strictly
// and, when marked host-lenient-issue, still load but report an issue.
func TestContributionFixtures(t *testing.T) {
	cases := loadContributionFixtures(t)
	if len(cases) == 0 {
		t.Fatal("no contribution fixtures")
	}
	for name, expected := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(contributionFixtureRoot, filepath.FromSlash(name))
			strict, strictErr := inspectInstallDirectory(dir)
			if strings.HasPrefix(name, "valid/") {
				if strictErr != nil {
					t.Fatalf("strict inspection rejected a valid case: %v", strictErr)
				}
				for point, count := range expected.Points {
					resolved := strict.Manifest.contribution(point)
					if resolved == nil || len(resolved.Entries) != count {
						t.Fatalf("%s entries = %+v, want %d", point, resolved, count)
					}
				}
				return
			}
			if slices.Contains(expected.RejectedBy, "host-strict") {
				if strictErr == nil {
					t.Fatal("strict inspection accepted an invalid case")
				}
				if expected.Contains != "" && !strings.Contains(strictErr.Error(), expected.Contains) {
					t.Fatalf("strict error %q does not contain %q", strictErr, expected.Contains)
				}
			}
			if slices.Contains(expected.RejectedBy, "host-lenient-issue") {
				lenient, err := inspectPackageDirectory(dir)
				if err != nil {
					t.Fatalf("lenient inspection failed instead of reporting an issue: %v", err)
				}
				if len(lenient.Manifest.allContributionIssues()) == 0 {
					t.Fatal("lenient inspection reported no contribution issue")
				}
			}
		})
	}
}
