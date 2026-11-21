package plugin

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Surface context and content of the newer service surfaces (design
// "plugin services surfaces", rulings 3, 4, 15, 17, 19).
//
// The frontend always builds a surface's full surfaceContext; the host
// decodes it against a strict per-surface whitelist (an unknown field is an
// invalid request) and, once the provider is chosen, drops what the provider
// may not see: remote identifiers (sftpFile remote, httpResponse request)
// need remote.context, database names (dbResult database) need
// database.schema. A missing capability never refuses the call; the content
// is still handed over without them. httpResponse headers are filtered here
// again (the frontend filters too, with the request's own secrets, which the
// host never sees). Only the four newer surfaces carry a context; the older
// surfaces and plugin callers never do.
//
// Content (dbResult, httpResponse) arrives as text; the host names the file
// (result.json, response.<ext> from the content type) and refuses binary
// content types, so the frontend never picks a file name.

var (
	serviceDBResultMaxBytes          = serviceSpec.Limit("dbResultMaxBytes")
	serviceHTTPResponseMaxBytes      = serviceSpec.Limit("httpResponseMaxBytes")
	serviceHTTPResponseHeadersMax    = int(serviceSpec.Limit("httpResponseHeadersMax"))
	serviceHTTPHeaderValueMaxBytes   = int(serviceSpec.Limit("httpHeaderValueMaxBytes"))
	serviceRemoteContextPathMaxBytes = int(serviceSpec.Limit("remoteContextPathMaxBytes"))
	serviceHTTPResponseTextTypes     = serviceSpec.List("httpResponseTextTypes")

	// serviceSensitiveHeaderNames are response headers that are always
	// withheld: the code generators' sensitive header names (one contract
	// list for both) plus set-cookie2.
	serviceSensitiveHeaderNames = append(httpCodeGeneratorSpec.List("sensitiveHeaderNames"), "set-cookie2")
	// serviceSensitiveHeaderFragments withhold a header whose lower-cased
	// name contains any of them (www-authenticate, x-session-id, ...).
	serviceSensitiveHeaderFragments = httpCodeGeneratorSpec.List("sensitiveKeyFragments")

	serviceTerminalKinds = []string{"local", "ssh", "docker"}
)

const (
	// serviceHTTPContentTypeMaxBytes bounds content.contentType.
	serviceHTTPContentTypeMaxBytes = 256
	// serviceHTTPHeadersRedactedMax bounds the frontend's headersRedacted
	// count (a response has far fewer headers) so the host's own count
	// cannot overflow; serviceHTTPElapsedMsMax bounds elapsedMs (a day).
	serviceHTTPHeadersRedactedMax = 100000
	serviceHTTPElapsedMsMax       = 86400000
	// serviceSurfaceContextFieldMaxBytes bounds every other string field of
	// a surface context (names, hosts, status text); the whole context is
	// bounded by surfaceContextMaxBytes anyway.
	serviceSurfaceContextFieldMaxBytes = 1024
)

// Strictly decoded surface contexts, one per newer surface. Optional parts
// are pointers so that "absent" survives a round trip.

type sftpFileSurfaceContext struct {
	Remote        *sftpRemoteContext `json:"remote,omitempty"`
	RemoteOmitted bool               `json:"remoteOmitted,omitempty"`
}

type sftpRemoteContext struct {
	Connection sftpConnectionContext `json:"connection"`
	Path       string                `json:"path"`
}

// sftpConnectionContext is the actual transfer endpoint, as in the
// sftp/file/context menu's passContext.
type sftpConnectionContext struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

type dbResultSurfaceContext struct {
	Database *dbResultDatabaseContext `json:"database,omitempty"`
}

// dbResultDatabaseContext has the database/tree/context menu's fields.
type dbResultDatabaseContext struct {
	Connection dbResultConnectionContext `json:"connection"`
	Database   string                    `json:"database,omitempty"`
	Table      *dbResultTableContext     `json:"table,omitempty"`
}

type dbResultConnectionContext struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type dbResultTableContext struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type httpResponseSurfaceContext struct {
	Response *httpResponseContext `json:"response,omitempty"`
	Request  *httpRequestContext  `json:"request,omitempty"`
}

type httpResponseContext struct {
	Status           int        `json:"status"`
	StatusText       string     `json:"statusText"`
	ContentType      string     `json:"contentType"`
	ElapsedMs        float64    `json:"elapsedMs"`
	Headers          [][]string `json:"headers"`
	HeadersRedacted  int        `json:"headersRedacted"`
	HeadersTruncated bool       `json:"headersTruncated,omitempty"`
}

// httpRequestContext is the request line; url was sanitized by the frontend
// with sanitizeMenuRequestUrl (query, fragment and userinfo removed,
// credential-shaped segments replaced). The host only checks that no query or
// fragment is left.
type httpRequestContext struct {
	Method   string `json:"method"`
	URL      string `json:"url"`
	Redacted bool   `json:"redacted,omitempty"`
}

type terminalSelectionSurfaceContext struct {
	Terminal *terminalSurfaceContext `json:"terminal,omitempty"`
}

type terminalSurfaceContext struct {
	Kind string `json:"kind"`
}

func invalidSurfaceContext(surface, format string, args ...any) error {
	return newContributionError(contributionErrInvalidRequest, "surfaceContext of %s: "+format, append([]any{surface}, args...)...)
}

// encodeSurfaceContext encodes value without HTML escaping (so that "<" in a
// header does not grow sixfold).
func encodeSurfaceContext(value any) (json.RawMessage, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))), nil
}

// contextText checks a string field: valid UTF-8, no control (Cc) or
// format (Cf: bidi overrides, zero-width) characters, at most maxBytes.
func contextText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsFunc(value, contextRuneRefused)
}

// contextRuneRefused: control (Cc), format (Cf: bidi overrides, zero-width)
// and the line / paragraph separators U+2028 / U+2029.
func contextRuneRefused(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' '
}

var (
	// contextSpecialScheme matches a WHATWG special scheme and every "/" or
	// "\" after it: for these the authority starts after any mix of slashes
	// and backslashes, also none (https:/u:p@h, https:\\u:p@h), as in
	// sanitizeMenuRequestUrl.
	contextSpecialScheme = regexp.MustCompile(`(?i)^(?:https?|wss?|ftp|file):[/\\]*`)
	// contextURLScheme matches another scheme followed by "//".
	contextURLScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
)

// requestURLHasUserinfo reports whether an address carries userinfo:
// url.Parse finds a User, or its authority holds "@" in any form (also
// %40). The authority starts after a special scheme and any mix of "/" and
// "\" (WHATWG), after another scheme's "//", after leading slashes, or at
// the start of a scheme-less address that does not begin with "/"; it ends
// at the next / \ ? or #. The frontend's sanitizeMenuRequestUrl removes
// userinfo; this is the host's backstop and refuses rather than repairs.
// Templated addresses ({{baseUrl}}, {redacted}) may not parse and are
// checked by authority only. Response headers use it too.
func requestURLHasUserinfo(raw string) bool {
	if parsed, err := url.Parse(raw); err == nil && parsed.User != nil {
		return true
	}
	rest := raw
	switch {
	case contextSpecialScheme.MatchString(raw):
		rest = raw[len(contextSpecialScheme.FindString(raw)):]
	case contextURLScheme.MatchString(raw):
		rest = raw[strings.Index(raw, "://")+3:]
	case strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, `/\`) || strings.HasPrefix(raw, `\/`):
		rest = strings.TrimLeft(raw, `/\`)
	case strings.HasPrefix(raw, "/"):
		return false
	}
	authority := rest
	if end := strings.IndexAny(rest, "/?#\\"); end >= 0 {
		authority = rest[:end]
	}
	return strings.Contains(authority, "@") || strings.Contains(strings.ToLower(authority), "%40")
}

// gateSurfaceContext strictly decodes a surface's context, drops what the
// provider's capabilities do not allow, filters httpResponse headers again
// and checks the result against surfaceContextMaxBytes. An empty result (no
// context given, or nothing left) is nil: the envelope then has no
// surfaceContext. The older surfaces take none.
func gateSurfaceContext(surface string, raw json.RawMessage, capabilities []api.Capability) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		if surface == serviceSurfaceHTTPResponse {
			// The response line is always handed over.
			return nil, invalidSurfaceContext(surface, "response is required")
		}
		return nil, nil
	}
	if !serviceSurfaceTakesContext(surface) {
		return nil, newContributionError(contributionErrInvalidRequest, "surface %s takes no surfaceContext", surface)
	}
	if len(trimmed) > serviceSurfaceContextMaxBytes || trimmed[0] != '{' {
		return nil, invalidSurfaceContext(surface, "must be an object of at most %d bytes", serviceSurfaceContextMaxBytes)
	}
	remote := slices.Contains(capabilities, api.CapabilityRemoteContext)
	schema := slices.Contains(capabilities, api.CapabilityDatabaseSchema)

	var gated any
	switch surface {
	case serviceSurfaceSFTPFile:
		var value sftpFileSurfaceContext
		if err := decodeStrictJSONValue(trimmed, &value, true, "surfaceContext"); err != nil {
			return nil, invalidSurfaceContext(surface, "%v", err)
		}
		if err := value.validate(surface); err != nil {
			return nil, err
		}
		if !remote {
			value = sftpFileSurfaceContext{}
		}
		if value.Remote == nil && !value.RemoteOmitted {
			return nil, nil
		}
		gated = value
	case serviceSurfaceDBResult:
		var value dbResultSurfaceContext
		if err := decodeStrictJSONValue(trimmed, &value, true, "surfaceContext"); err != nil {
			return nil, invalidSurfaceContext(surface, "%v", err)
		}
		if err := value.validate(surface); err != nil {
			return nil, err
		}
		if !schema || value.Database == nil {
			return nil, nil
		}
		gated = value
	case serviceSurfaceHTTPResponse:
		var value httpResponseSurfaceContext
		if err := decodeStrictJSONValue(trimmed, &value, true, "surfaceContext"); err != nil {
			return nil, invalidSurfaceContext(surface, "%v", err)
		}
		if err := value.validate(surface); err != nil {
			return nil, err
		}
		if !remote {
			value.Request = nil
		}
		value.Response.filterHeaders()
		return encodeHTTPResponseContext(surface, value)
	case serviceSurfaceTerminalSelection:
		var value terminalSelectionSurfaceContext
		if err := decodeStrictJSONValue(trimmed, &value, true, "surfaceContext"); err != nil {
			return nil, invalidSurfaceContext(surface, "%v", err)
		}
		if value.Terminal == nil {
			return nil, nil
		}
		if !slices.Contains(serviceTerminalKinds, value.Terminal.Kind) {
			return nil, invalidSurfaceContext(surface, "terminal.kind must be one of %s", strings.Join(serviceTerminalKinds, ", "))
		}
		gated = value
	}
	encoded, err := encodeSurfaceContext(gated)
	if err != nil {
		return nil, invalidSurfaceContext(surface, "%v", err)
	}
	if len(encoded) > serviceSurfaceContextMaxBytes {
		return nil, invalidSurfaceContext(surface, "must be at most %d bytes", serviceSurfaceContextMaxBytes)
	}
	return encoded, nil
}

// serviceSurfaceTakesContext reports whether surface is one of the newer
// surfaces, the only ones with a surfaceContext.
func serviceSurfaceTakesContext(surface string) bool {
	switch surface {
	case serviceSurfaceSFTPFile, serviceSurfaceDBResult, serviceSurfaceHTTPResponse, serviceSurfaceTerminalSelection:
		return true
	}
	return false
}

// surfaceContextCapabilities are the capabilities that decide what of the
// surface's context a provider gets.
func surfaceContextCapabilities(surface string) []api.Capability {
	switch surface {
	case serviceSurfaceSFTPFile, serviceSurfaceHTTPResponse:
		return []api.Capability{api.CapabilityRemoteContext}
	case serviceSurfaceDBResult:
		return []api.Capability{api.CapabilityDatabaseSchema}
	}
	return nil
}

func (value sftpFileSurfaceContext) validate(surface string) error {
	if value.Remote == nil {
		return nil
	}
	if value.RemoteOmitted {
		return invalidSurfaceContext(surface, "remote and remoteOmitted exclude each other")
	}
	connection := value.Remote.Connection
	for _, field := range []string{connection.ID, connection.Name, connection.Protocol, connection.Host, connection.Username} {
		if !contextText(field, serviceSurfaceContextFieldMaxBytes) {
			return invalidSurfaceContext(surface, "remote.connection has an invalid field")
		}
	}
	if connection.Port < 0 || connection.Port > 65535 {
		return invalidSurfaceContext(surface, "remote.connection.port must be 0-65535")
	}
	remotePath := value.Remote.Path
	if len(remotePath) > serviceRemoteContextPathMaxBytes {
		return invalidSurfaceContext(surface, "remote.path is over %d bytes (send remoteOmitted instead)", serviceRemoteContextPathMaxBytes)
	}
	if !strings.HasPrefix(remotePath, "/") || !contextText(remotePath, serviceRemoteContextPathMaxBytes) {
		return invalidSurfaceContext(surface, "remote.path must be an absolute remote path")
	}
	return nil
}

func (value dbResultSurfaceContext) validate(surface string) error {
	database := value.Database
	if database == nil {
		return nil
	}
	fields := []string{database.Connection.ID, database.Connection.Name, database.Connection.Kind, database.Database}
	if database.Table != nil {
		fields = append(fields, database.Table.Name, database.Table.Kind)
		if database.Table.Name == "" {
			return invalidSurfaceContext(surface, "database.table.name is required")
		}
	}
	for _, field := range fields {
		if !contextText(field, serviceSurfaceContextFieldMaxBytes) {
			return invalidSurfaceContext(surface, "database has an invalid field")
		}
	}
	return nil
}

func (value httpResponseSurfaceContext) validate(surface string) error {
	if value.Response == nil {
		return invalidSurfaceContext(surface, "response is required")
	}
	if response := value.Response; response != nil {
		if response.Status < 0 || response.Status > 999 || response.ElapsedMs < 0 || response.ElapsedMs > serviceHTTPElapsedMsMax ||
			response.HeadersRedacted < 0 || response.HeadersRedacted > serviceHTTPHeadersRedactedMax {
			return invalidSurfaceContext(surface, "response has an invalid number")
		}
		if !contextText(response.StatusText, serviceSurfaceContextFieldMaxBytes) || !contextText(response.ContentType, serviceHTTPContentTypeMaxBytes) {
			return invalidSurfaceContext(surface, "response has an invalid field")
		}
		for _, header := range response.Headers {
			// A value may hold a tab (obs-text aside, RFC 9110 allows HTAB).
			if len(header) != 2 || header[0] == "" || !contextText(header[0], serviceSurfaceContextFieldMaxBytes) ||
				!utf8.ValidString(header[1]) || strings.ContainsFunc(header[1], func(r rune) bool { return r != '\t' && contextRuneRefused(r) }) {
				return invalidSurfaceContext(surface, "response.headers must be [name, value] pairs")
			}
		}
	}
	if request := value.Request; request != nil {
		if request.Method == "" || !contextText(request.Method, 32) {
			return invalidSurfaceContext(surface, "request.method is invalid")
		}
		// Surrounding whitespace is refused: WHATWG trims it and would read
		// " https://u:p@h" as userinfo where url.Parse fails; the frontend
		// already trims.
		if !contextText(request.URL, serviceRemoteContextPathMaxBytes) || strings.TrimFunc(request.URL, unicode.IsSpace) != request.URL ||
			strings.ContainsAny(request.URL, "?#") || requestURLHasUserinfo(request.URL) {
			return invalidSurfaceContext(surface, "request.url must be a sanitized address (no userinfo, query or fragment) of at most %d bytes", serviceRemoteContextPathMaxBytes)
		}
	}
	return nil
}

// serviceHeaderSensitive reports whether a response header is withheld by
// name: a contract sensitive header name, set-cookie2, or a name containing
// a sensitive key fragment (case-insensitive).
func serviceHeaderSensitive(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if slices.Contains(serviceSensitiveHeaderNames, lower) {
		return true
	}
	for _, fragment := range serviceSensitiveHeaderFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// serviceURLHeaders are response headers whose value is (or holds) a URL;
// their query and fragment can carry OAuth codes or signed-URL tokens.
var serviceURLHeaders = []string{"location", "content-location", "link", "refresh", "origin", "referer"}

// headerURLPattern finds the <URL> references of a Link value.
var headerURLPattern = regexp.MustCompile(`<([^>]*)>`)

// refreshURLPattern finds the url= part of a Refresh value.
var refreshURLPattern = regexp.MustCompile(`(?i)(\burl\s*=\s*)(['"]?)([^'"]*)(['"]?)\s*$`)

// refreshDelayPattern is a Refresh value without url=: a delay only.
// refreshDelayPrefixPattern is what may come before url=: a delay and its
// separator.
var (
	refreshDelayPattern       = regexp.MustCompile(`^\s*\d+(?:\.\d+)?\s*$`)
	refreshDelayPrefixPattern = regexp.MustCompile(`^\s*\d+(?:\.\d+)?[\s;,]*$`)
)

// stripHeaderURL removes the query and fragment of one URL in a header
// value. ok is false when it does not parse or carries userinfo (the header
// is then dropped); changed tells whether anything was removed.
func stripHeaderURL(raw string) (stripped string, changed, ok bool) {
	raw = strings.TrimSpace(raw)
	if _, err := url.Parse(raw); err != nil || requestURLHasUserinfo(raw) {
		return "", false, false
	}
	if end := strings.IndexAny(raw, "?#"); end >= 0 {
		return raw[:end], true, true
	}
	return raw, false, true
}

// sanitizeURLHeader strips the query and fragment from the URLs of a
// URL-valued header (Location, Content-Location, Origin, Referer: the whole
// value; Link: every <URL>; Refresh: the url= part). ok is false when a URL
// does not parse; changed tells whether anything was removed.
func sanitizeURLHeader(lowerName, value string) (sanitized string, changed, ok bool) {
	switch lowerName {
	case "link":
		matches := headerURLPattern.FindAllStringSubmatchIndex(value, -1)
		if len(matches) == 0 || strings.ContainsAny(stripLinkURLs(value), "?#") {
			// No <URL>, or a query or fragment outside one (an anchor
			// parameter, say).
			return "", false, false
		}
		var out strings.Builder
		last := 0
		for _, match := range matches {
			stripped, cut, valid := stripHeaderURL(value[match[2]:match[3]])
			if !valid {
				return "", false, false
			}
			changed = changed || cut
			out.WriteString(value[last:match[2]])
			out.WriteString(stripped)
			last = match[3]
		}
		out.WriteString(value[last:])
		return out.String(), changed, true
	case "refresh":
		// Only a delay may come before url= (or make up the whole value);
		// anything else (0?tok=...; url=) drops the header.
		match := refreshURLPattern.FindStringSubmatchIndex(value)
		if match == nil {
			if !refreshDelayPattern.MatchString(value) {
				return "", false, false
			}
			return value, false, true
		}
		if !refreshDelayPrefixPattern.MatchString(value[:match[2]]) {
			return "", false, false
		}
		stripped, cut, valid := stripHeaderURL(value[match[6]:match[7]])
		if !valid {
			return "", false, false
		}
		return value[:match[6]] + stripped + value[match[7]:], cut, true
	}
	return stripHeaderURL(value)
}

// stripLinkURLs removes the <...> references of a Link value.
func stripLinkURLs(value string) string {
	return headerURLPattern.ReplaceAllString(value, "")
}

// filterHeaders drops sensitive headers and headers whose value is over
// httpHeaderValueMaxBytes, and strips the query and fragment of URL-valued
// headers (dropping one whose URL does not parse). Every header dropped or
// stripped adds one to headersRedacted, on top of what the frontend already
// removed. It then keeps at most httpResponseHeadersMax (setting
// headersTruncated when it drops any).
func (response *httpResponseContext) filterHeaders() {
	kept := make([][]string, 0, len(response.Headers))
	for _, header := range response.Headers {
		if serviceHeaderSensitive(header[0]) || len(header[1]) > serviceHTTPHeaderValueMaxBytes {
			response.HeadersRedacted++
			continue
		}
		if lower := strings.ToLower(strings.TrimSpace(header[0])); slices.Contains(serviceURLHeaders, lower) {
			sanitized, changed, ok := sanitizeURLHeader(lower, header[1])
			if !ok {
				response.HeadersRedacted++
				continue
			}
			if changed {
				response.HeadersRedacted++
				header = []string{header[0], sanitized}
			}
		}
		kept = append(kept, header)
	}
	if len(kept) > serviceHTTPResponseHeadersMax {
		kept = kept[:serviceHTTPResponseHeadersMax]
		response.HeadersTruncated = true
	}
	response.Headers = kept
}

// encodeHTTPResponseContext encodes an httpResponse context, dropping
// headers from the end (headersTruncated) until it fits
// surfaceContextMaxBytes.
func encodeHTTPResponseContext(surface string, value httpResponseSurfaceContext) (json.RawMessage, error) {
	for {
		encoded, err := encodeSurfaceContext(value)
		if err != nil {
			return nil, invalidSurfaceContext(surface, "%v", err)
		}
		if len(encoded) <= serviceSurfaceContextMaxBytes {
			return encoded, nil
		}
		if len(value.Response.Headers) == 0 {
			return nil, invalidSurfaceContext(surface, "must be at most %d bytes", serviceSurfaceContextMaxBytes)
		}
		value.Response.Headers = value.Response.Headers[:len(value.Response.Headers)-1]
		value.Response.HeadersTruncated = true
	}
}

// serviceMediaType is the lower-cased type/subtype of a Content-Type value;
// an empty value is text/plain. ok is false for a malformed value.
func serviceMediaType(contentType string) (string, bool) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "text/plain", true
	}
	if len(contentType) > serviceHTTPContentTypeMaxBytes || strings.ContainsFunc(contentType, unicode.IsControl) {
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Parameters the standard parser refuses do not change the type.
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	}
	kind, subtype, found := strings.Cut(mediaType, "/")
	if !found || kind == "" || subtype == "" || strings.ContainsAny(mediaType, " \t,") {
		return "", false
	}
	return mediaType, true
}

// serviceHTTPTextType reports whether mediaType is in the contract's
// httpResponseTextTypes ("text/*", "application/*+json" style patterns).
func serviceHTTPTextType(mediaType string) bool {
	for _, pattern := range serviceHTTPResponseTextTypes {
		if matched, err := path.Match(pattern, mediaType); err == nil && matched {
			return true
		}
	}
	return false
}

// httpResponseFileName is the file name an httpResponse content of
// contentType is handed over as: response plus an extension by type (json,
// xml, html, javascript / ecmascript, csv, yaml, markdown, ndjson, otherwise
// .txt). ok is false for a type outside httpResponseTextTypes (binary
// responses are not supported) or a malformed one.
func httpResponseFileName(contentType string) (name string, ok bool) {
	mediaType, ok := serviceMediaType(contentType)
	if !ok || !serviceHTTPTextType(mediaType) {
		return "", false
	}
	_, subtype, _ := strings.Cut(mediaType, "/")
	suffix := ""
	if index := strings.LastIndex(subtype, "+"); index >= 0 {
		suffix = subtype[index+1:]
	}
	extension := ".txt"
	switch {
	case subtype == "x-ndjson" || subtype == "ndjson":
		extension = ".ndjson"
	case subtype == "json" || suffix == "json":
		extension = ".json"
	case subtype == "xml" || suffix == "xml":
		extension = ".xml"
	case subtype == "html":
		extension = ".html"
	case subtype == "javascript" || subtype == "ecmascript" || subtype == "x-javascript":
		extension = ".js"
	case subtype == "csv":
		extension = ".csv"
	case subtype == "yaml" || subtype == "x-yaml":
		extension = ".yaml"
	case subtype == "markdown" || subtype == "x-markdown":
		extension = ".md"
	}
	return "response" + extension, true
}

// hostServiceContentName checks a content source against its surface's
// limit and returns the file name the host hands it over as.
func hostServiceContentName(surface string, content pluginServicesContent) (string, error) {
	switch surface {
	case serviceSurfaceDBResult:
		if int64(len(content.Text)) > serviceDBResultMaxBytes {
			return "", newContributionError(contributionErrTooLarge, "the result is %d bytes, at most %d are handed over", len(content.Text), serviceDBResultMaxBytes)
		}
		return serviceDBResultFileName, nil
	case serviceSurfaceHTTPResponse:
		if int64(len(content.Text)) > serviceHTTPResponseMaxBytes {
			return "", newContributionError(contributionErrTooLarge, "the response is %d bytes, at most %d are handed over", len(content.Text), serviceHTTPResponseMaxBytes)
		}
		name, ok := httpResponseFileName(content.ContentType)
		if !ok {
			return "", newContributionError(contributionErrInvalidRequest, "only text responses can be handed to a service")
		}
		return name, nil
	}
	return "", newContributionError(contributionErrInvalidRequest, "surface %s takes no content", surface)
}
