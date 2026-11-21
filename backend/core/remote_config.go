package core

import (
	"os"
	"strings"
	"sync/atomic"
)

// DefaultMinCodeAPIBaseURL is the production API origin. It intentionally has
// no port because the public service is exposed through the HTTPS default port.
const DefaultMinCodeAPIBaseURL = "https://api.mincodeai.cn"

// MinCodeAPIFallbackBaseURLs lists additional public API origins tried after the
// production origin. Keep this empty until a fallback has a verified, current TLS
// certificate; explicit configuration (argument/environment) pins a single origin.
var MinCodeAPIFallbackBaseURLs []string

// ResolveMinCodeAPIServers resolves the ordered MinCode public API request
// pool: an explicit command argument, service-specific environment variables,
// or the shared MINCODE_API_BASE_URL each pin exactly one origin (a development
// override must not fail over to production); otherwise the production default
// is followed by the fallback pool.
//
// A development process may set a service-specific value such as
// http://127.0.0.1:8787. Production values must use the public HTTPS origin and
// should not include an implementation port.
func ResolveMinCodeAPIServers(configured string, serviceEnvironmentNames ...string) []string {
	if value, ok := explicitMinCodeServer(configured, serviceEnvironmentNames...); ok {
		return []string{value}
	}
	return append([]string{DefaultMinCodeAPIBaseURL}, MinCodeAPIFallbackBaseURLs...)
}

// ResolveMinCodeAPIServer returns the primary origin of the request pool.
func ResolveMinCodeAPIServer(configured string, serviceEnvironmentNames ...string) string {
	return ResolveMinCodeAPIServers(configured, serviceEnvironmentNames...)[0]
}

// MinCodeCloudServerEnvironmentNames are the service-specific overrides of the
// member cloud origin, checked in order before the shared MINCODE_API_BASE_URL.
// MINCODE_MARKET_SERVER_URL is kept for existing development setups; it now
// moves the account session too, so the two can never disagree.
var MinCodeCloudServerEnvironmentNames = []string{"MINCODE_CLOUD_SERVER_URL", "MINCODE_MARKET_SERVER_URL"}

var desktopCloudDefault atomic.Pointer[string]

// SetDesktopCloudDefault sets the validated application configuration default.
// Explicit arguments and environment overrides retain higher precedence.
func SetDesktopCloudDefault(origin string) {
	desktopCloudDefault.Store(&origin)
}

// ResolveMinCodeCloudServer is the single resolver for the member cloud origin:
// the account session (login, profile, entitlement, devices) and the plugin
// marketplace (catalog, releases, artifact downloads) both use it, so the
// marketplace origin always equals the signed-in session origin.
//
// Precedence: explicit argument, MINCODE_CLOUD_SERVER_URL,
// MINCODE_MARKET_SERVER_URL, MINCODE_API_BASE_URL, then tan.json and finally the built-in default.
func ResolveMinCodeCloudServer(configured string) string {
	if value, ok := explicitMinCodeServer(configured, MinCodeCloudServerEnvironmentNames...); ok {
		return value
	}
	if origin := desktopCloudDefault.Load(); origin != nil && *origin != "" {
		return *origin
	}
	return defaultMinCodeCloudBaseURL
}

func explicitMinCodeServer(configured string, serviceEnvironmentNames ...string) (string, bool) {
	if value := strings.TrimSpace(configured); value != "" {
		return value, true
	}
	for _, name := range serviceEnvironmentNames {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, true
		}
	}
	if value := strings.TrimSpace(os.Getenv("MINCODE_API_BASE_URL")); value != "" {
		return value, true
	}
	return "", false
}
