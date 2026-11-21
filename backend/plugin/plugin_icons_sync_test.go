package plugin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// tools/maintenance/sync-plugin-icons.mjs generates every copy of api.PluginIcons
// for code that cannot import it; each copy must name exactly the host's icons.
func TestPluginIconCopiesMatchHost(t *testing.T) {
	want := make([]string, 0, len(pluginViewIcons))
	for icon := range pluginViewIcons {
		want = append(want, icon)
	}
	slices.Sort(want)

	quoted := regexp.MustCompile(`["']([a-z][a-z-]*)["']`)
	names := func(literal string) []string {
		var got []string
		for _, match := range quoted.FindAllStringSubmatch(literal, -1) {
			got = append(got, match[1])
		}
		slices.Sort(got)
		return got
	}
	check := func(path string, literals *regexp.Regexp, optional bool) {
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		if optional && errors.Is(err, fs.ErrNotExist) {
			t.Logf("%s not checked out; skipped", path)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		found := literals.FindAllString(string(data), -1)
		if len(found) == 0 {
			t.Fatalf("%s: no icon list found", path)
		}
		for _, literal := range found {
			if got := names(literal); !slices.Equal(got, want) {
				t.Errorf("%s: icons %v, host pluginViewIcons %v", path, got, want)
			}
		}
	}

	check("tools/mincode-cli/plugin_icons.go", regexp.MustCompile(`(?s)var pluginIcons = map\[string\]struct\{\}\{.*?\n\}`), false)
	check("vue-app/src/domain/plugin-icons.ts", regexp.MustCompile(`(?s)export const PLUGIN_ICONS: readonly string\[\] = \[.*?\n\]`), false)
	// cloud/ is a separate repository; its generated lists must match too.
	check("cloud/web/src/utils/pluginIcons.ts", regexp.MustCompile(`(?s)export const PLUGIN_ICONS: readonly string\[\] = \[.*?\n\]`), true)
	check("cloud/app/common/service/Mincode/PluginIcons.php", regexp.MustCompile(`(?s)public const NAMES = \[.*?\n    \]`), true)
	for _, path := range []string{
		"cloud/app/common/service/Mincode/MarketplaceArtifact.php",
		"cloud/app/common/service/Mincode/MarketplaceWorkflow.php",
		"cloud/app/api/controller/Mincode/Marketplace.php",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "PluginIcons::NAMES") || strings.Contains(string(data), "'data-analysis'") {
			t.Errorf("%s must validate icons with PluginIcons::NAMES instead of an inline list", path)
		}
	}
}
