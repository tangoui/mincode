package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
)

const validPage = `{"code":1,"data":{"items":[{"product_key":"test.plugin","name":"Test","slug":"test-plugin","description":"Demo","homepage":"https://example.org/","category":"development","tags":["tool"],"updated_at":1750000000}],"page":1,"limit":20}}`

func TestListLocalOverrideAndContract(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "https://wrong.example")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != pluginsPath || r.URL.RawQuery != "category_set=2&host_version="+misc.AppVersion+"&limit=20&page=1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		fmt.Fprint(w, validPage)
	}))
	defer server.Close()
	setMarketServer(t, server.URL)
	encoded, err := core.Dispatch("market_plugins_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginPage
	if err := json.Unmarshal([]byte(encoded), &result); err != nil || result.Page != 1 || result.Limit != 20 || len(result.Items) != 1 || result.Items[0].ProductKey != "test.plugin" {
		t.Fatalf("invalid result: %s, %v", encoded, err)
	}
	if result.Items[0].Author != nil || result.Items[0].LatestPublishedVersion != nil {
		t.Fatalf("legacy catalog metadata should be null: %s", encoded)
	}
}

func TestListNullableDisplayMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Replace(validPage, `"updated_at":1750000000`, `"author":"Alice","latest_published_version":"1.2.3-beta.1","updated_at":1750000000`, 1))
	}))
	defer server.Close()
	setMarketServer(t, server.URL)
	encoded, err := core.Dispatch("market_plugins_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginPage
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	item := result.Items[0]
	if item.Author == nil || *item.Author != "Alice" || item.LatestPublishedVersion == nil || *item.LatestPublishedVersion != "1.2.3-beta.1" {
		t.Fatalf("metadata not passed through: %s", encoded)
	}
}

func TestListIconIsDisplayOnlyAndValidated(t *testing.T) {
	for _, icon := range []string{"", "box", "connection", "data-analysis", "document", "folder", "monitor", "promotion", "setting", "terminal", "tickets", "edit-pen", "zoom-out", "unknown", "Box", "<svg/>"} {
		t.Run(icon, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, strings.Replace(validPage, `"updated_at":1750000000`, `"icon":`+strconv.Quote(icon)+`,"updated_at":1750000000`, 1))
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			encoded, err := core.Dispatch("market_plugins_list", `{}`)
			if err != nil {
				t.Fatal(err)
			}
			var result pluginPage
			if err := json.Unmarshal([]byte(encoded), &result); err != nil {
				t.Fatal(err)
			}
			if icon == "unknown" || icon == "Box" || icon == "<svg/>" {
				if len(result.Items) != 0 {
					t.Fatalf("kept entry with invalid icon: %s", encoded)
				}
				return
			}
			if len(result.Items) != 1 || result.Items[0].Icon != icon {
				t.Fatalf("icon %q: %s, %v", icon, encoded, err)
			}
		})
	}
}

func TestListSixteenDisplayOnlyItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		items := make([]json.RawMessage, 16)
		for i := range items {
			items[i] = json.RawMessage(`{"product_key":"test.plugin","name":"Test","slug":"test-plugin","description":"Demo","homepage":"","category":"other","tags":[],"updated_at":1}`)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 1, "data": map[string]any{"items": items, "page": 1, "limit": 20}})
	}))
	defer server.Close()
	setMarketServer(t, server.URL)
	encoded, err := core.Dispatch("market_plugins_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginPage
	if err := json.Unmarshal([]byte(encoded), &result); err != nil || len(result.Items) != 16 {
		t.Fatalf("invalid result: %s, %v", encoded, err)
	}
	if strings.Contains(encoded, "artifact") || strings.Contains(encoded, "install") {
		t.Fatalf("list leaked installation metadata: %s", encoded)
	}
}

// setMarketServer points the shared cloud resolver at a test server via the market override.
func setMarketServer(t *testing.T, value string) {
	t.Helper()
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "")
	t.Setenv("MINCODE_MARKET_SERVER_URL", value)
}

func TestServerURL(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "")
	setMarketServer(t, "")
	origin, err := serverURL()
	if err != nil || origin.String() != strings.TrimRight(core.ResolveMinCodeCloudServer(""), "/") {
		t.Fatalf("default = %v, %v", origin, err)
	}
	// The market shares the account session's resolver, so the global API override applies too.
	t.Setenv("MINCODE_API_BASE_URL", "https://api.example.org/")
	if origin, err := serverURL(); err != nil || origin.String() != "https://api.example.org" {
		t.Fatalf("api base override = %v, %v", origin, err)
	}
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "https://cloud.example.org")
	if origin, err := serverURL(); err != nil || origin.String() != "https://cloud.example.org" {
		t.Fatalf("cloud override = %v, %v", origin, err)
	}
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "")

	for _, value := range []string{"http://example.com", "http://mincode.funadmin.com", "http://other.mincode.funadmin.com", "http://mincode.funadmin.com:8080", "http://mincode.funadmin.com.evil.example", "file:///tmp", "https://user:password@example.org", "https://example.org/path", "https://example.org?x=1", "https://example.org#frag", "http://169.254.169.254", "http://127.0.0.2@localhost", "https://example.org:bad"} {
		t.Run(value, func(t *testing.T) {
			setMarketServer(t, value)
			if _, err := serverURL(); err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
	setMarketServer(t, "http://[::1]:8080")
	if _, err := serverURL(); err != nil {
		t.Fatalf("IPv6 loopback rejected: %v", err)
	}
	setMarketServer(t, "https://example.org/")
	if origin, err := serverURL(); err != nil || origin.String() != "https://example.org" {
		t.Fatalf("trailing slash origin = %v, %v", origin, err)
	}
	setMarketServer(t, "https://169.254.169.254")
	if _, err := serverURL(); err == nil {
		t.Fatal("accepted metadata endpoint")
	}
}

func TestListRejectsInvalidArgumentsBeforeNetwork(t *testing.T) {
	for _, args := range []string{`{"page":-1}`, `{"page":0}`, `{"page":13}`, `{"limit":0}`, `{"limit":51}`, `{"limit":-1}`, `{"page":"oops"}`} {
		if _, err := core.Dispatch("market_plugins_list", args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func TestListRejectsRedirectAndErrors(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
	}{
		"redirect":         {http.StatusFound, `<a href="/api/mincode/marketplace/plugins">redirect</a>`},
		"http failure":     {http.StatusBadGateway, validPage},
		"envelope failure": {http.StatusOK, `{"code":0,"msg":"private failure","data":{}}`},
		"missing code":     {http.StatusOK, `{"data":{}}`},
		"invalid json":     {http.StatusOK, `oops`},
		"oversized":        {http.StatusOK, strings.Repeat("x", maxResponse+1)},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if name == "redirect" {
					w.Header().Set("Location", "/escaped")
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			if _, err := core.Dispatch("market_plugins_list", `{}`); err == nil {
				t.Fatal("accepted failed response")
			}
			if requests != 1 {
				t.Fatalf("followed redirect or retried: %d requests", requests)
			}
		})
	}
}

func TestListRejectsInvalidCatalogPage(t *testing.T) {
	for name, data := range map[string]string{
		"missing items": `{"page":1,"limit":20}`,
		"items object":  `{"items":{},"page":1,"limit":20}`,
		"wrong page":    `{"items":[],"page":2,"limit":20}`,
		"too many":      `{"items":[{},{}],"page":1,"limit":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"code":1,"data":%s}`, data)
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			if _, err := core.Dispatch("market_plugins_list", `{}`); err == nil {
				t.Fatal("accepted invalid catalog page")
			}
		})
	}
}

func TestListSkipsInvalidEntriesAndKeepsValidOnes(t *testing.T) {
	validItem := validPage[strings.Index(validPage, `"items":[`)+9 : strings.Index(validPage, `],"page"`)]
	withField := func(field string) string {
		return strings.Replace(validItem, `"updated_at":1750000000`, field+`,"updated_at":1750000000`, 1)
	}
	for name, bad := range map[string]string{
		"not an object":       `"oops"`,
		"invalid key":         `{"product_key":"../bad"}`,
		"missing name":        `{"product_key":"test.plugin"}`,
		"invalid category":    `{"product_key":"test.plugin","name":"Test","slug":"test","homepage":"","category":"unknown","tags":[],"updated_at":1}`,
		"invalid tags":        `{"product_key":"test.plugin","name":"Test","slug":"test","homepage":"","category":"other","tags":[123],"updated_at":1}`,
		"invalid timestamp":   `{"product_key":"test.plugin","name":"Test","slug":"test","homepage":"","category":"other","tags":[],"updated_at":"today"}`,
		"credential homepage": `{"product_key":"test.plugin","name":"Test","slug":"test","homepage":"https://user:pass@example.org","category":"other","tags":[],"updated_at":1}`,
		"invalid author":      withField(`"author":"\u000aBad"`),
		"invalid version":     withField(`"latest_published_version":"../../bad"`),
		"email author":        withField(`"author":"alice@example.org"`),
		"invalid icon":        withField(`"icon":"<svg/>"`),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"code":1,"data":{"items":[%s,%s],"page":1,"limit":20}}`, bad, validItem)
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			encoded, err := core.Dispatch("market_plugins_list", `{}`)
			if err != nil {
				t.Fatalf("one bad entry failed the page: %v", err)
			}
			var result pluginPage
			if err := json.Unmarshal([]byte(encoded), &result); err != nil || len(result.Items) != 1 || result.Items[0].ProductKey != "test.plugin" || result.Items[0].Slug != "test-plugin" {
				t.Fatalf("result = %s, %v; want only the valid entry", encoded, err)
			}
		})
	}
}

// The desktop asks for the extended category set; older desktops never send it and the
// cloud maps new categories back onto the eight they understand.
func TestListAcceptsExtendedMarketplaceCategories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category_set") != "2" {
			t.Errorf("extended categories not requested: %s", r.URL.RawQuery)
		}
		items := make([]string, 0, len(marketCategories))
		for i, category := range []string{"messaging", "devops", "editor", "ai", "document", "language", "theme", "entertainment"} {
			items = append(items, fmt.Sprintf(`{"product_key":"test.plugin%d","name":"Test","slug":"test-%d","homepage":"","category":%q,"tags":[],"updated_at":1}`, i, i, category))
		}
		fmt.Fprintf(w, `{"code":1,"data":{"items":[%s],"page":1,"limit":20}}`, strings.Join(items, ","))
	}))
	defer server.Close()
	setMarketServer(t, server.URL)
	encoded, err := core.Dispatch("market_plugins_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginPage
	if err := json.Unmarshal([]byte(encoded), &result); err != nil || len(result.Items) != 8 || result.Items[3].Category != "ai" {
		t.Fatalf("extended categories dropped: %s, %v", encoded, err)
	}
}

// The cover is decoration: an unusable URL must never remove an installable
// plugin from the catalog, but a valid https one must reach the desktop as-is.
func TestListCoverURL(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  string
	}{
		{"https cover passes through", `"cover_url":"https://cdn.example.org/covers/devtools.png","updated_at":1750000000`, "https://cdn.example.org/covers/devtools.png"},
		{"plain http is dropped", `"cover_url":"http://cdn.example.org/covers/devtools.png","updated_at":1750000000`, ""},
		{"data url is dropped", `"cover_url":"data:image/png;base64,AAAA","updated_at":1750000000`, ""},
		{"credentials in url are dropped", `"cover_url":"https://user:pass@cdn.example.org/a.png","updated_at":1750000000`, ""},
		{"overlong cover is dropped", `"cover_url":"https://cdn.example.org/` + strings.Repeat("a", 500) + `.png","updated_at":1750000000`, ""},
		{"missing cover is empty", `"updated_at":1750000000`, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, strings.Replace(validPage, `"updated_at":1750000000`, testCase.field, 1))
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			encoded, err := core.Dispatch("market_plugins_list", `{}`)
			if err != nil {
				t.Fatal(err)
			}
			var result pluginPage
			if err := json.Unmarshal([]byte(encoded), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != 1 {
				t.Fatalf("a bad cover must not drop the listing: %s", encoded)
			}
			if result.Items[0].CoverURL != testCase.want {
				t.Fatalf("cover = %q, want %q", result.Items[0].CoverURL, testCase.want)
			}
		})
	}
}

func TestListRatesHostCompatibility(t *testing.T) {
	previous := misc.AppVersion
	misc.AppVersion = "0.2.0"
	t.Cleanup(func() { misc.AppVersion = previous })
	entry := func(key, extra string) string {
		return `{"product_key":"` + key + `","name":"N","slug":"` + key + `","description":"","homepage":"","category":"development","tags":[],"updated_at":1,"latest_published_version":"2.0.0"` + extra + `}`
	}
	items := strings.Join([]string{
		entry("fits", `,"min_host_version":"0.1.0","compatible_version":"2.0.0"`),
		entry("older.fits", `,"min_host_version":"0.3.0","compatible_version":"1.4.0"`),
		entry("none.fits", `,"min_host_version":"0.3.0","compatible_version":null`),
		entry("too.new", `,"max_host_version_exclusive":"0.2.0","compatible_version":null`),
		entry("legacy.cloud", `,"min_host_version":"0.3.0"`),
		entry("bad.range", `,"min_host_version":"latest","max_host_version_exclusive":7`),
	}, ",")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("host_version") != "0.2.0" {
			t.Errorf("host version not sent: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"code":1,"data":{"items":[`+items+`],"page":1,"limit":20}}`)
	}))
	defer server.Close()
	setMarketServer(t, server.URL)
	encoded, err := core.Dispatch("market_plugins_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginPage
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range result.Items {
		fit := "<nil>"
		if item.CompatibleVersion != nil {
			fit = *item.CompatibleVersion
		}
		got[item.ProductKey] = item.HostCompatibility + " " + fit + " " + item.MinHostVersion + "-" + item.MaxHostVersionExclusive
	}
	want := map[string]string{
		"fits":         "compatible 2.0.0 0.1.0-",
		"older.fits":   "host_too_old 1.4.0 0.3.0-",
		"none.fits":    "host_too_old <nil> 0.3.0-",
		"too.new":      "host_too_new <nil> -0.2.0",
		"legacy.cloud": "host_too_old <nil> 0.3.0-",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	// A wrongly typed bound drops the entry (strict decoding); an unparsable string bound only reads as "no bound".
	if _, ok := got["bad.range"]; ok {
		t.Errorf("entry with a non-string bound kept: %q", got["bad.range"])
	}
}

// Download and like counts are display-only: they pass through when sane, and a
// missing, negative or non-integer value reads as zero rather than hiding the plugin.
func TestListPopularityCounts(t *testing.T) {
	cases := []struct {
		name             string
		field            string
		downloads, likes int64
	}{
		{"counts pass through", `"download_count":1234,"like_count":56,"updated_at":1750000000`, 1234, 56},
		{"missing counts are zero", `"updated_at":1750000000`, 0, 0},
		{"negative counts are zero", `"download_count":-3,"like_count":-1,"updated_at":1750000000`, 0, 0},
		{"non-integer counts are zero", `"download_count":"many","like_count":1.5,"updated_at":1750000000`, 0, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, strings.Replace(validPage, `"updated_at":1750000000`, testCase.field, 1))
			}))
			defer server.Close()
			setMarketServer(t, server.URL)
			encoded, err := core.Dispatch("market_plugins_list", `{}`)
			if err != nil {
				t.Fatal(err)
			}
			var result pluginPage
			if err := json.Unmarshal([]byte(encoded), &result); err != nil || len(result.Items) != 1 {
				t.Fatalf("counts must not drop the listing: %s, %v", encoded, err)
			}
			if got := result.Items[0]; got.DownloadCount != testCase.downloads || got.LikeCount != testCase.likes {
				t.Fatalf("counts = %d/%d, want %d/%d", got.DownloadCount, got.LikeCount, testCase.downloads, testCase.likes)
			}
		})
	}
}
