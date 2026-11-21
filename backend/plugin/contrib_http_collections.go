package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// HTTP collection templates (contributes.httpCollections) are read-only data:
// a {"path"} reference to a "mincode.http-collection" v1 JSON file. The user
// can browse a template, import a copy into their own collections (fresh ids,
// no link back to the plugin) or open one request as an unsaved draft tab.
// Nothing is ever sent automatically. Templates cannot carry credentials:
// the format has no auth fields, sensitive headers and keys must be empty or
// a single {{variable}}, URLs cannot contain userinfo, and values that look
// like credentials (JWTs, private keys, cloud/API tokens, Basic/Bearer
// values) reject the entry.

const httpCollectionPointKey = "httpCollections"

const (
	httpCollectionFormat  = "mincode.http-collection"
	httpCollectionVersion = 1
)

var (
	httpCollectionSpec             = mustContributionSpec(httpCollectionPointKey)
	httpCollectionTitleMaxRunes    = int(httpCollectionSpec.Limit("titleMaxRunes"))
	httpCollectionDescriptionRunes = int(httpCollectionSpec.Limit("descriptionMaxRunes"))
	httpCollectionLocalesMax       = int(httpCollectionSpec.Limit("localesMax"))
	httpCollectionRequestsMax      = int(httpCollectionSpec.Limit("requestsPerCollection"))
	httpCollectionPluginRequests   = int(httpCollectionSpec.Limit("requestsPerPlugin"))
	httpCollectionFoldersMax       = int(httpCollectionSpec.Limit("foldersMax"))
	httpCollectionDepthMax         = int(httpCollectionSpec.Limit("folderDepthMax"))
	httpCollectionEnvironmentsMax  = int(httpCollectionSpec.Limit("environmentsMax"))
	httpCollectionVarsMax          = int(httpCollectionSpec.Limit("varsPerEnvironment"))
	httpCollectionRowsMax          = int(httpCollectionSpec.Limit("rowsMax"))
	httpCollectionNameMaxRunes     = int(httpCollectionSpec.Limit("nameMaxRunes"))
	httpCollectionKeyMaxLength     = int(httpCollectionSpec.Limit("keyMaxLength"))
	httpCollectionValueMaxRunes    = int(httpCollectionSpec.Limit("valueMaxRunes"))
	httpCollectionURLMaxLength     = int(httpCollectionSpec.Limit("urlMaxLength"))
	httpCollectionBodyMaxBytes     = int(httpCollectionSpec.Limit("bodyMaxBytes"))
	httpCollectionDocMaxBytes      = int(httpCollectionSpec.Limit("documentationMaxBytes"))
	httpCollectionSnapshotBytes    = int(httpCollectionSpec.Limit("snapshotBytesPerPlugin"))
	httpCollectionMethods          = httpCollectionSpec.List("methods")
	httpCollectionBodyTypes        = httpCollectionSpec.List("bodyTypes")
	httpCollectionSensitiveHeaders = httpCollectionSpec.List("sensitiveHeaderNames")
	httpCollectionSensitiveKeys    = httpCollectionSpec.List("sensitiveKeyFragments")

	httpCollectionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	// httpCollectionPlaceholder is the only non-empty value a sensitive
	// header or key may have: exactly one {{identifier}}.
	httpCollectionPlaceholder = regexp.MustCompile(`^\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}$`)
	// httpCollectionLeadingVariable matches a URL that starts with a
	// {{variable}} (for example {{baseUrl}}/health).
	httpCollectionLeadingVariable = regexp.MustCompile(`^\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}`)
	// httpCollectionCredentialPatterns are credential shapes rejected in any
	// value, body or URL.
	httpCollectionCredentialPatterns = []*regexp.Regexp{
		regexp.MustCompile(`eyJ[\w-]+\.eyJ[\w-]+\.`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),
		regexp.MustCompile(`github_pat_`),
		regexp.MustCompile(`xox[baprs]-`),
		regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		regexp.MustCompile(`Basic [A-Za-z0-9+/=]{12,}`),
		regexp.MustCompile(`Bearer [A-Za-z0-9._~+/-]{16,}`),
	}
	// httpCollectionProseCredentialPatterns drop the Basic/Bearer shapes for
	// prose (documentation, descriptions) where "Basic authentication" is
	// ordinary text.
	httpCollectionProseCredentialPatterns = httpCollectionCredentialPatterns[:8]
)

// HttpCollectionRow is one params/headers/environment variable row.
type HttpCollectionRow struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// HttpCollectionRequest is one normalized template request. It has no auth,
// form-data, binary or file fields.
type HttpCollectionRequest struct {
	Name             string              `json:"name"`
	Method           string              `json:"method"`
	URL              string              `json:"url"`
	Params           []HttpCollectionRow `json:"params"`
	Headers          []HttpCollectionRow `json:"headers"`
	BodyType         string              `json:"bodyType"`
	Body             string              `json:"body"`
	GraphqlQuery     string              `json:"graphqlQuery,omitempty"`
	GraphqlVariables string              `json:"graphqlVariables,omitempty"`
	Documentation    string              `json:"documentation,omitempty"`
}

// HttpCollectionFolder is a nested folder of requests.
type HttpCollectionFolder struct {
	Name     string                  `json:"name"`
	Requests []HttpCollectionRequest `json:"requests"`
	Children []HttpCollectionFolder  `json:"children"`
}

// HttpCollectionEnvironment is a named variable set.
type HttpCollectionEnvironment struct {
	Name string              `json:"name"`
	Vars []HttpCollectionRow `json:"vars"`
}

// HttpCollectionTemplate is the normalized collection file.
type HttpCollectionTemplate struct {
	Name         string                      `json:"name"`
	Requests     []HttpCollectionRequest     `json:"requests"`
	Children     []HttpCollectionFolder      `json:"children"`
	Environments []HttpCollectionEnvironment `json:"environments"`
}

// HttpCollectionCounts summarises a template for previews.
type HttpCollectionCounts struct {
	Requests     int `json:"requests"`
	Folders      int `json:"folders"`
	Environments int `json:"environments"`
}

// HttpCollectionContribution is one validated contributes.httpCollections
// entry as sent to the frontend consumer.
type HttpCollectionContribution struct {
	ID                   string                 `json:"id"`
	Title                string                 `json:"title"`
	TitleLocalized       map[string]string      `json:"titleLocalized,omitempty"`
	Description          string                 `json:"description,omitempty"`
	DescriptionLocalized map[string]string      `json:"descriptionLocalized,omitempty"`
	Collection           HttpCollectionTemplate `json:"collection"`
	Counts               HttpCollectionCounts   `json:"counts"`
}

type httpCollectionEntryReference struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized"`
	Description          *string           `json:"description"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized"`
	Path                 string            `json:"path"`
}

type httpCollectionRowFile struct {
	Key         *string `json:"key"`
	Value       *string `json:"value"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

type httpCollectionRequestFile struct {
	Name             *string                 `json:"name"`
	Method           *string                 `json:"method"`
	URL              *string                 `json:"url"`
	Params           []httpCollectionRowFile `json:"params"`
	Headers          []httpCollectionRowFile `json:"headers"`
	BodyType         *string                 `json:"bodyType"`
	Body             *string                 `json:"body"`
	GraphqlQuery     *string                 `json:"graphqlQuery"`
	GraphqlVariables *string                 `json:"graphqlVariables"`
	Documentation    *string                 `json:"documentation"`
}

type httpCollectionFolderFile struct {
	Name     *string                     `json:"name"`
	Requests []httpCollectionRequestFile `json:"requests"`
	Children []httpCollectionFolderFile  `json:"children"`
}

type httpCollectionEnvironmentFile struct {
	Name *string                 `json:"name"`
	Vars []httpCollectionRowFile `json:"vars"`
}

type httpCollectionFile struct {
	Format       *string                         `json:"format"`
	Version      *int                            `json:"version"`
	Name         *string                         `json:"name"`
	Requests     []httpCollectionRequestFile     `json:"requests"`
	Children     []httpCollectionFolderFile      `json:"children"`
	Environments []httpCollectionEnvironmentFile `json:"environments"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              httpCollectionPointKey,
		Decode:           decodeHttpCollectionEntry,
		Finalize:         finalizeHttpCollectionEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate HTTP collection id %q", id) },
		IssuePrefix:      "invalid httpCollections contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped HTTP collection %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.httpCollections[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "HTTP collections",
		FileLabels: contributionFileLabels{Noun: "HTTP collection", Entry: "HTTP collection entry"},
	})
}

func decodeHttpCollectionEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference httpCollectionEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse HTTP collection entry: %w", err)
	}
	id := strings.TrimSpace(reference.ID)
	if !httpCollectionIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("HTTP collection id %q must match ^[a-z0-9][a-z0-9-]{0,47}$", reference.ID)
	}
	item := HttpCollectionContribution{ID: id}
	var err error
	if item.Title, err = validateContributionLine(reference.Title, httpCollectionTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("title %w", err)
	}
	if item.TitleLocalized, err = validateContributionLocalized(reference.TitleLocalized, httpCollectionTitleMaxRunes, httpCollectionLocalesMax); err != nil {
		return nil, id, fmt.Errorf("titleLocalized %w", err)
	}
	if reference.Description != nil {
		if item.Description, err = validateContributionLine(*reference.Description, httpCollectionDescriptionRunes, false); err != nil {
			return nil, id, fmt.Errorf("description %w", err)
		}
	}
	if item.DescriptionLocalized, err = validateContributionLocalized(reference.DescriptionLocalized, httpCollectionDescriptionRunes, httpCollectionLocalesMax); err != nil {
		return nil, id, fmt.Errorf("descriptionLocalized %w", err)
	}
	if strings.TrimSpace(reference.Path) == "" {
		return nil, id, errors.New(`"path" is required`)
	}
	data, err := ctx.readFile(reference.Path, "HTTP collection")
	if err != nil {
		return nil, id, err
	}
	collection, counts, err := parseHttpCollectionFile(data)
	if err != nil {
		return nil, id, fmt.Errorf("HTTP collection file %q: %w", strings.TrimSpace(reference.Path), err)
	}
	item.Collection = collection
	item.Counts = counts
	return item, id, nil
}

// finalizeHttpCollectionEntries enforces the per-plugin request count and
// snapshot size in entry order.
func finalizeHttpCollectionEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	requests, bytes := 0, 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		item := entry.Value.(HttpCollectionContribution)
		encoded, _ := json.Marshal(item)
		var message string
		switch {
		case requests+item.Counts.Requests > httpCollectionPluginRequests:
			message = fmt.Sprintf("plugin HTTP collections exceed %d requests in total", httpCollectionPluginRequests)
		case bytes+len(encoded) > httpCollectionSnapshotBytes:
			message = fmt.Sprintf("plugin HTTP collections exceed %d bytes in total", httpCollectionSnapshotBytes)
		}
		if message != "" {
			issues = append(issues, ContributionIssue{Point: httpCollectionPointKey, Entry: entry.Index, ID: entry.ID, Message: message})
			continue
		}
		requests += item.Counts.Requests
		bytes += len(encoded)
		kept = append(kept, entry)
	}
	return kept, issues
}

// parseHttpCollectionFile validates a mincode.http-collection v1 document.
// strictJSONWalk rejects duplicate keys, comments and trailing data first;
// the typed decode then rejects unknown fields (auth, form-data, files, ...).
func parseHttpCollectionFile(data []byte) (HttpCollectionTemplate, HttpCollectionCounts, error) {
	var counts HttpCollectionCounts
	canonical, err := strictJSONWalk(data, nil)
	if err != nil {
		return HttpCollectionTemplate{}, counts, err
	}
	var file httpCollectionFile
	if err := decodeStrictJSON(canonical, &file, true); err != nil {
		return HttpCollectionTemplate{}, counts, err
	}
	if file.Format == nil || *file.Format != httpCollectionFormat {
		return HttpCollectionTemplate{}, counts, fmt.Errorf("format must be %q", httpCollectionFormat)
	}
	if file.Version == nil || *file.Version != httpCollectionVersion {
		return HttpCollectionTemplate{}, counts, fmt.Errorf("version must be %d", httpCollectionVersion)
	}
	template := HttpCollectionTemplate{}
	if template.Name, err = httpCollectionName(file.Name, "name"); err != nil {
		return HttpCollectionTemplate{}, counts, err
	}
	walker := &httpCollectionWalker{counts: &counts}
	if template.Requests, err = walker.requests(file.Requests, "requests"); err != nil {
		return HttpCollectionTemplate{}, counts, err
	}
	if template.Children, err = walker.folders(file.Children, "children", 1); err != nil {
		return HttpCollectionTemplate{}, counts, err
	}
	if len(file.Environments) > httpCollectionEnvironmentsMax {
		return HttpCollectionTemplate{}, counts, fmt.Errorf("environments must have at most %d entries", httpCollectionEnvironmentsMax)
	}
	template.Environments = make([]HttpCollectionEnvironment, 0, len(file.Environments))
	for index, environment := range file.Environments {
		label := fmt.Sprintf("environments[%d]", index)
		name, err := httpCollectionName(environment.Name, label+".name")
		if err != nil {
			return HttpCollectionTemplate{}, counts, err
		}
		if len(environment.Vars) > httpCollectionVarsMax {
			return HttpCollectionTemplate{}, counts, fmt.Errorf("%s.vars must have at most %d entries", label, httpCollectionVarsMax)
		}
		vars, err := httpCollectionRows(environment.Vars, label+".vars", false)
		if err != nil {
			return HttpCollectionTemplate{}, counts, err
		}
		template.Environments = append(template.Environments, HttpCollectionEnvironment{Name: name, Vars: vars})
	}
	counts.Environments = len(template.Environments)
	return template, counts, nil
}

type httpCollectionWalker struct {
	counts *HttpCollectionCounts
}

func (walker *httpCollectionWalker) requests(values []httpCollectionRequestFile, label string) ([]HttpCollectionRequest, error) {
	result := make([]HttpCollectionRequest, 0, len(values))
	for index, value := range values {
		walker.counts.Requests++
		if walker.counts.Requests > httpCollectionRequestsMax {
			return nil, fmt.Errorf("a collection must have at most %d requests", httpCollectionRequestsMax)
		}
		request, err := httpCollectionRequest(value, fmt.Sprintf("%s[%d]", label, index))
		if err != nil {
			return nil, err
		}
		result = append(result, request)
	}
	return result, nil
}

func (walker *httpCollectionWalker) folders(values []httpCollectionFolderFile, label string, depth int) ([]HttpCollectionFolder, error) {
	if len(values) > 0 && depth > httpCollectionDepthMax {
		return nil, fmt.Errorf("%s: folders must be nested at most %d levels deep", label, httpCollectionDepthMax)
	}
	result := make([]HttpCollectionFolder, 0, len(values))
	for index, value := range values {
		walker.counts.Folders++
		if walker.counts.Folders > httpCollectionFoldersMax {
			return nil, fmt.Errorf("a collection must have at most %d folders", httpCollectionFoldersMax)
		}
		folderLabel := fmt.Sprintf("%s[%d]", label, index)
		name, err := httpCollectionName(value.Name, folderLabel+".name")
		if err != nil {
			return nil, err
		}
		requests, err := walker.requests(value.Requests, folderLabel+".requests")
		if err != nil {
			return nil, err
		}
		children, err := walker.folders(value.Children, folderLabel+".children", depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, HttpCollectionFolder{Name: name, Requests: requests, Children: children})
	}
	return result, nil
}

func httpCollectionName(value *string, label string) (string, error) {
	if value == nil {
		return "", fmt.Errorf("%s is required", label)
	}
	name, err := validateContributionLine(*value, httpCollectionNameMaxRunes, true)
	if err != nil {
		return "", fmt.Errorf("%s %w", label, err)
	}
	return name, nil
}

func httpCollectionRequest(value httpCollectionRequestFile, label string) (HttpCollectionRequest, error) {
	name, err := httpCollectionName(value.Name, label+".name")
	if err != nil {
		return HttpCollectionRequest{}, err
	}
	request := HttpCollectionRequest{Name: name, BodyType: "none"}
	if value.Method == nil {
		return request, fmt.Errorf("%s.method is required", label)
	}
	request.Method = strings.ToUpper(strings.TrimSpace(*value.Method))
	if !httpCollectionHas(httpCollectionMethods, request.Method) {
		return request, fmt.Errorf("%s.method must be one of %s", label, strings.Join(httpCollectionMethods, ", "))
	}
	if value.URL == nil {
		return request, fmt.Errorf("%s.url is required", label)
	}
	if request.URL, err = validateHttpCollectionURL(*value.URL); err != nil {
		return request, fmt.Errorf("%s.url %w", label, err)
	}
	if len(value.Params) > httpCollectionRowsMax || len(value.Headers) > httpCollectionRowsMax {
		return request, fmt.Errorf("%s: params and headers must have at most %d rows each", label, httpCollectionRowsMax)
	}
	if request.Params, err = httpCollectionRows(value.Params, label+".params", false); err != nil {
		return request, err
	}
	if request.Headers, err = httpCollectionRows(value.Headers, label+".headers", true); err != nil {
		return request, err
	}
	if value.BodyType != nil {
		request.BodyType = strings.TrimSpace(*value.BodyType)
		if !httpCollectionHas(httpCollectionBodyTypes, request.BodyType) {
			return request, fmt.Errorf("%s.bodyType must be one of %s", label, strings.Join(httpCollectionBodyTypes, ", "))
		}
	}
	texts := []struct {
		field  string
		source *string
		target *string
		limit  int
		prose  bool
	}{
		{"body", value.Body, &request.Body, httpCollectionBodyMaxBytes, false},
		{"graphqlQuery", value.GraphqlQuery, &request.GraphqlQuery, httpCollectionBodyMaxBytes, false},
		{"graphqlVariables", value.GraphqlVariables, &request.GraphqlVariables, httpCollectionBodyMaxBytes, false},
		{"documentation", value.Documentation, &request.Documentation, httpCollectionDocMaxBytes, true},
	}
	for _, text := range texts {
		if text.source == nil {
			continue
		}
		normalized, err := validateContributionMultiline(*text.source, 0, text.limit)
		if err != nil {
			return request, fmt.Errorf("%s.%s %w", label, text.field, err)
		}
		if err := checkHttpCollectionCredential(normalized, text.prose); err != nil {
			return request, fmt.Errorf("%s.%s %w", label, text.field, err)
		}
		*text.target = normalized
	}
	return request, nil
}

// httpCollectionRows validates params, headers or environment variables and
// applies the sensitive-value rule.
func httpCollectionRows(values []httpCollectionRowFile, label string, headers bool) ([]HttpCollectionRow, error) {
	result := make([]HttpCollectionRow, 0, len(values))
	for index, value := range values {
		rowLabel := fmt.Sprintf("%s[%d]", label, index)
		if value.Key == nil {
			return nil, fmt.Errorf("%s.key is required", rowLabel)
		}
		key, err := validateContributionLine(*value.Key, 0, true)
		if err != nil {
			return nil, fmt.Errorf("%s.key %w", rowLabel, err)
		}
		if len(key) > httpCollectionKeyMaxLength {
			return nil, fmt.Errorf("%s.key must be at most %d bytes", rowLabel, httpCollectionKeyMaxLength)
		}
		row := HttpCollectionRow{Key: key, Enabled: true}
		if value.Value != nil {
			row.Value = *value.Value
			if !utf8.ValidString(row.Value) {
				return nil, fmt.Errorf("%s.value must be valid UTF-8", rowLabel)
			}
			if utf8.RuneCountInString(row.Value) > httpCollectionValueMaxRunes {
				return nil, fmt.Errorf("%s.value must be at most %d characters", rowLabel, httpCollectionValueMaxRunes)
			}
			if err := checkContributionRunes(row.Value, false, false); err != nil {
				return nil, fmt.Errorf("%s.value %w", rowLabel, err)
			}
			if err := checkHttpCollectionCredential(row.Value, false); err != nil {
				return nil, fmt.Errorf("%s.value %w", rowLabel, err)
			}
		}
		if value.Description != nil {
			if row.Description, err = validateContributionLine(*value.Description, httpCollectionDescriptionRunes, false); err != nil {
				return nil, fmt.Errorf("%s.description %w", rowLabel, err)
			}
			if err := checkHttpCollectionCredential(row.Description, true); err != nil {
				return nil, fmt.Errorf("%s.description %w", rowLabel, err)
			}
		}
		if value.Enabled != nil {
			row.Enabled = *value.Enabled
		}
		if isHttpCollectionSensitiveKey(key, headers) && row.Value != "" && !httpCollectionPlaceholder.MatchString(row.Value) {
			return nil, fmt.Errorf("%s: %q is sensitive; its value must be empty or a single {{variable}}", rowLabel, key)
		}
		result = append(result, row)
	}
	return result, nil
}

// isHttpCollectionSensitiveKey reports a sensitive header name or a key that
// contains a sensitive fragment (case-insensitive).
func isHttpCollectionSensitiveKey(key string, header bool) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if header && httpCollectionHas(httpCollectionSensitiveHeaders, lower) {
		return true
	}
	for _, fragment := range httpCollectionSensitiveKeys {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// validateHttpCollectionURL accepts an absolute http(s) URL without userinfo
// or a URL that starts with a {{variable}}. The authority part (up to the
// first "/", "?" or "#") must never contain "@".
func validateHttpCollectionURL(value string) (string, error) {
	url := strings.TrimSpace(value)
	if url == "" {
		return "", errors.New("is required")
	}
	if !utf8.ValidString(url) {
		return "", errors.New("must be valid UTF-8")
	}
	if len(url) > httpCollectionURLMaxLength {
		return "", fmt.Errorf("must be at most %d bytes", httpCollectionURLMaxLength)
	}
	if strings.ContainsAny(url, " \t\n\r") {
		return "", errors.New("must not contain whitespace")
	}
	if err := checkContributionRunes(url, false, false); err != nil {
		return "", err
	}
	rest := ""
	lower := strings.ToLower(url)
	switch {
	case strings.HasPrefix(lower, "http://"):
		rest = url[len("http://"):]
	case strings.HasPrefix(lower, "https://"):
		rest = url[len("https://"):]
	case httpCollectionLeadingVariable.MatchString(url):
		rest = url[len(httpCollectionLeadingVariable.FindString(url)):]
	default:
		return "", errors.New("must be an absolute http(s) URL or start with a {{variable}}")
	}
	authority := rest
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority = rest[:end]
	}
	if strings.Contains(authority, "@") {
		return "", errors.New("must not contain user information (user:password@)")
	}
	if (strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")) && authority == "" {
		return "", errors.New("must include a host")
	}
	if err := checkHttpCollectionCredential(url, false); err != nil {
		return "", err
	}
	return url, nil
}

// checkHttpCollectionCredential rejects values shaped like credentials.
func checkHttpCollectionCredential(value string, prose bool) error {
	patterns := httpCollectionCredentialPatterns
	if prose {
		patterns = httpCollectionProseCredentialPatterns
	}
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return errors.New("looks like a credential; templates must use empty values or {{variables}} instead")
		}
	}
	return nil
}

func httpCollectionHas(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// httpCollections returns the validated entries of a manifest.
func (manifest Manifest) httpCollections() []HttpCollectionContribution {
	resolved := manifest.contribution(httpCollectionPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]HttpCollectionContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(HttpCollectionContribution); ok {
			items = append(items, item)
		}
	}
	return items
}
