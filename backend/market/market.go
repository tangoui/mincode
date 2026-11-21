// Package market provides read-only discovery of listed cloud products.
// Catalog metadata is display-only and must never authorize downloads or installs.
package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
	"mincode/app/plugins/api"
)

const (
	pluginsPath = "/api/mincode/marketplace/plugins"
	maxResponse = 1 << 20
	maxPage     = 12
	maxLimit    = 50
)

// marketHTTPClient is shared by the catalog and release requests so paging and
// install previews reuse connections; idle ones expire on their own instead of
// being closed after every request. Do not inherit proxy credentials or a
// cookie jar; redirects are never followed.
var marketHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var pluginKeyPattern = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._-]{1,127}$`)
var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
var authorEmailPattern = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
var authorPhonePattern = regexp.MustCompile(`\+?\d[\d\s().-]{7,}\d`)

// Plugin is untrusted, display-only catalog metadata. No artifact URL or credentials are exposed.
type Plugin struct {
	ProductKey  string   `json:"product_key"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage"`
	Icon        string   `json:"icon"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Author      *string  `json:"author"`
	// CoverURL is an optional https image shown on the catalog card. The cloud
	// already returns it; malformed values are dropped rather than failing the row.
	CoverURL               string  `json:"cover_url"`
	LatestPublishedVersion *string `json:"latest_published_version"`
	UpdatedAt              int64   `json:"updated_at"`
	// RequiresPaidHost marks plugins reserved for members with an annual or perpetual MinCode plan.
	RequiresPaidHost bool `json:"requires_paid_host"`
	// MinHostVersion (inclusive) and MaxHostVersionExclusive bound the MinCode versions the latest
	// release supports; empty means no bound.
	MinHostVersion          string `json:"min_host_version"`
	MaxHostVersionExclusive string `json:"max_host_version_exclusive"`
	// HostCompatibility describes the latest release for this MinCode: "compatible", "host_too_old",
	// "host_too_new", or "" when there is no published release.
	HostCompatibility string `json:"host_compatibility"`
	// CompatibleVersion is the newest release this MinCode can run (it may be older than the latest),
	// or nil when none fits. Display only: installing re-selects and re-checks the release.
	CompatibleVersion *string `json:"compatible_version"`
	// DownloadCount and LikeCount are the cloud's popularity figures, display only.
	DownloadCount int64 `json:"download_count"`
	LikeCount     int64 `json:"like_count"`
}

type pluginPage struct {
	Items []Plugin `json:"items"`
	Page  int      `json:"page"`
	Limit int      `json:"limit"`
}

type listArgs struct {
	Page  *int `json:"page"`
	Limit *int `json:"limit"`
}

func init() {
	core.Register("market_plugins_list", listHandler)
}

func listHandler(_ string, argsJSON string) (string, error) {
	var args listArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	page, limit := 1, 20
	if args.Page != nil {
		page = *args.Page
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if page < 1 || page > maxPage || limit < 1 || limit > maxLimit {
		return "", fmt.Errorf("market list: page must be 1-%d and limit 1-%d", maxPage, maxLimit)
	}
	base, err := serverURL()
	if err != nil {
		return "", fmt.Errorf("market list: %w", err)
	}
	endpoint := *base
	endpoint.Path = pluginsPath
	// category_set=2 asks for marketplace categories added after the original eight; without it the
	// cloud maps them onto the closest original category for older desktops.
	query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}, "category_set": {"2"}}
	// host_version lets the cloud report the newest release this MinCode can install.
	if validHostVersion(misc.AppVersion) {
		query.Set("host_version", misc.AppVersion)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("market list: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := marketHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("market list: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("market list: HTTP status %d", resp.StatusCode)
	}
	body, err := core.ReadAllLimited(resp.Body, maxResponse)
	if err != nil {
		return "", fmt.Errorf("market list: read response: %w", err)
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("market list: invalid response: %w", err)
	}
	if envelope.Code == nil || *envelope.Code != 1 {
		return "", errors.New("market list: server returned an unsuccessful code")
	}
	var raw struct {
		Items []json.RawMessage `json:"items"`
		Page  *int              `json:"page"`
		Limit *int              `json:"limit"`
	}
	if err := json.Unmarshal(envelope.Data, &raw); err != nil {
		return "", fmt.Errorf("market list: invalid catalog: %w", err)
	}
	if raw.Items == nil || raw.Page == nil || raw.Limit == nil || *raw.Page != page || *raw.Limit != limit || len(raw.Items) > limit {
		return "", errors.New("market list: invalid catalog pagination")
	}
	result := pluginPage{Items: make([]Plugin, 0, len(raw.Items)), Page: *raw.Page, Limit: *raw.Limit}
	dropped := 0
	for _, item := range raw.Items {
		// One malformed listing must not hide the rest of the page: each entry is still
		// validated strictly, and an invalid one is dropped instead of failing the page.
		plugin, err := parsePlugin(item, misc.AppVersion)
		if err != nil {
			dropped++
			continue
		}
		result.Items = append(result.Items, plugin)
	}
	if dropped > 0 {
		log.Printf("market list: dropped %d invalid plugin entries on page %d", dropped, page)
	}
	return core.EncodeResult(result)
}

type rawPlugin struct {
	ProductKey             *string   `json:"product_key"`
	Name                   *string   `json:"name"`
	Slug                   *string   `json:"slug"`
	Description            *string   `json:"description"`
	Homepage               *string   `json:"homepage"`
	Icon                   *string   `json:"icon"`
	Category               *string   `json:"category"`
	Tags                   *[]string `json:"tags"`
	Author                 *string   `json:"author"`
	CoverURL               *string   `json:"cover_url"`
	LatestPublishedVersion *string   `json:"latest_published_version"`
	UpdatedAt              *int64    `json:"updated_at"`
	RequiresPaidHost       *bool     `json:"requires_paid_host"`
	MinHostVersion         *string   `json:"min_host_version"`
	MaxHostVersion         *string   `json:"max_host_version_exclusive"`
	// Absent from clouds that predate it (or when no host version was sent); null when nothing fits.
	CompatibleVersion *json.RawMessage `json:"compatible_version"`
	// Raw so that a malformed count degrades to zero instead of rejecting the entry.
	DownloadCount json.RawMessage `json:"download_count"`
	LikeCount     json.RawMessage `json:"like_count"`
}

// parsePlugin strictly validates one untrusted catalog entry and rates its latest release for hostVersion.
func parsePlugin(encoded json.RawMessage, hostVersion string) (Plugin, error) {
	var item rawPlugin
	if err := json.Unmarshal(encoded, &item); err != nil {
		return Plugin{}, errors.New("market list: invalid plugin metadata")
	}
	if item.ProductKey == nil || !pluginKeyPattern.MatchString(*item.ProductKey) || item.Name == nil || !boundedText(*item.Name, 160, false) ||
		item.Slug == nil || !slugPattern.MatchString(*item.Slug) || item.Category == nil || !validCategory(*item.Category) ||
		item.Homepage == nil || item.Tags == nil || len(*item.Tags) > 8 || item.UpdatedAt == nil || *item.UpdatedAt < 0 {
		return Plugin{}, errors.New("market list: invalid plugin metadata")
	}
	if item.Icon != nil && !validIcon(*item.Icon) {
		return Plugin{}, errors.New("market list: invalid plugin metadata")
	}
	plugin := Plugin{ProductKey: *item.ProductKey, Name: *item.Name, Slug: *item.Slug, Category: *item.Category, Tags: *item.Tags, UpdatedAt: *item.UpdatedAt}
	if item.Icon != nil {
		plugin.Icon = *item.Icon
	}
	if item.RequiresPaidHost != nil {
		plugin.RequiresPaidHost = *item.RequiresPaidHost
	}
	if item.Description != nil {
		plugin.Description = *item.Description
	}
	if item.Author != nil {
		if !boundedText(*item.Author, 120, false) || strings.IndexFunc(*item.Author, unicode.IsControl) >= 0 || authorEmailPattern.MatchString(*item.Author) || authorPhonePattern.MatchString(*item.Author) {
			return Plugin{}, errors.New("market list: invalid plugin author")
		}
		plugin.Author = item.Author
	}
	if item.LatestPublishedVersion != nil {
		if len(*item.LatestPublishedVersion) > 64 || !versionPattern.MatchString(*item.LatestPublishedVersion) {
			return Plugin{}, errors.New("market list: invalid plugin version")
		}
		plugin.LatestPublishedVersion = item.LatestPublishedVersion
	}
	// A cover is decoration: a bad one is dropped, so a typo in the admin form
	// cannot hide an otherwise installable plugin from the desktop catalog.
	if item.CoverURL != nil && *item.CoverURL != "" && boundedText(*item.CoverURL, 500, false) {
		if cover, err := url.Parse(*item.CoverURL); err == nil && cover.Scheme == "https" && cover.Hostname() != "" &&
			cover.User == nil && cover.Opaque == "" && cover.Fragment == "" {
			plugin.CoverURL = *item.CoverURL
		}
	}
	plugin.DownloadCount, plugin.LikeCount = displayCount(item.DownloadCount), displayCount(item.LikeCount)
	// Host bounds are advisory display data: a malformed bound reads as "no bound" rather than hiding the plugin.
	if item.MinHostVersion != nil && validHostVersion(*item.MinHostVersion) {
		plugin.MinHostVersion = *item.MinHostVersion
	}
	if item.MaxHostVersion != nil && validHostVersion(*item.MaxHostVersion) {
		plugin.MaxHostVersionExclusive = *item.MaxHostVersion
	}
	if plugin.LatestPublishedVersion != nil {
		plugin.HostCompatibility = hostCompatibility(hostVersion, plugin.MinHostVersion, plugin.MaxHostVersionExclusive)
		switch {
		case item.CompatibleVersion != nil:
			var fit *string
			if json.Unmarshal(*item.CompatibleVersion, &fit) == nil && (fit == nil || validHostVersion(*fit)) {
				plugin.CompatibleVersion = fit
			} else if plugin.HostCompatibility == "compatible" {
				plugin.CompatibleVersion = plugin.LatestPublishedVersion
			}
		case plugin.HostCompatibility == "compatible":
			// Older clouds do not report older compatible releases; offer the latest when it fits.
			plugin.CompatibleVersion = plugin.LatestPublishedVersion
		}
	}
	if !boundedText(plugin.Description, 4096, true) || !boundedText(*item.Homepage, 500, true) {
		return Plugin{}, errors.New("market list: plugin text exceeds limit")
	}
	plugin.Homepage = *item.Homepage
	if plugin.Homepage != "" {
		link, err := url.Parse(plugin.Homepage)
		if err != nil || (link.Scheme != "https" && link.Scheme != "http") || link.Hostname() == "" || link.User != nil || link.Opaque != "" {
			return Plugin{}, errors.New("market list: invalid plugin homepage")
		}
	}
	for _, tag := range plugin.Tags {
		if !boundedText(tag, 96, false) || utf8.RuneCountInString(tag) > 24 {
			return Plugin{}, errors.New("market list: invalid plugin tag")
		}
	}
	return plugin, nil
}

// displayCount reads a popularity count; anything but a non-negative integer reads as zero.
func displayCount(raw json.RawMessage) int64 {
	var count int64
	if len(raw) == 0 || json.Unmarshal(raw, &count) != nil || count < 0 {
		return 0
	}
	return count
}

func boundedText(value string, max int, allowEmpty bool) bool {
	return (allowEmpty || strings.TrimSpace(value) != "") && len(value) <= max && utf8.ValidString(value)
}

func validIcon(icon string) bool {
	_, ok := api.PluginIcons[icon]
	return ok || icon == ""
}

// marketCategories are marketplace display categories: the manifest categories (api.Categories) plus
// the marketplace-only ones. Must match the cloud MarketplaceWorkflow::CATEGORIES and catalog.ts.
var marketCategories = []string{"database", "spreadsheet", "web", "api", "network", "messaging", "development", "devops",
	"editor", "ai", "document", "productivity", "language", "theme", "entertainment", "other"}

func validCategory(category string) bool {
	for _, known := range marketCategories {
		if category == known {
			return true
		}
	}
	return false
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

func serverURL() (*url.URL, error) {
	// The same resolver as the cloud account session: the download origin check in
	// cloudcompanion.DownloadMarketArtifact requires both to agree.
	base := core.ResolveMinCodeCloudServer("")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || core.IsCloudMetadataHost(parsed.Hostname()) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimRight(parsed.Path, "/") != "" || parsed.Opaque != "" {
		return nil, errors.New("invalid market server origin")
	}
	if parsed.Scheme != "https" {
		// Plain HTTP is allowed only for local development; never send credentials.
		localHost := loopbackHost(parsed.Hostname())
		if parsed.Scheme != "http" || !localHost {
			return nil, errors.New("market server requires HTTPS (HTTP is limited to loopback development servers)")
		}
	}
	parsed.Path = ""
	return parsed, nil
}

// validHostVersion accepts unprefixed semantic versions, as plugin manifests and the cloud use them.
func validHostVersion(version string) bool {
	return len(version) <= 64 && versionPattern.MatchString(version) && semver.IsValid("v"+version)
}

// hostCompatibility rates hostVersion against a release range (minimum inclusive, maximum exclusive).
func hostCompatibility(hostVersion, minVersion, maxVersion string) string {
	if !validHostVersion(hostVersion) {
		return "compatible"
	}
	if minVersion != "" && semver.Compare("v"+hostVersion, "v"+minVersion) < 0 {
		return "host_too_old"
	}
	if maxVersion != "" && semver.Compare("v"+hostVersion, "v"+maxVersion) >= 0 {
		return "host_too_new"
	}
	return "compatible"
}
