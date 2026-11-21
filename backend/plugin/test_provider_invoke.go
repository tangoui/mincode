package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"mincode/app/backend/core"
)

// plugin_host_test_discover and plugin_host_test_run call a test provider's
// actions for one project. The WebView names the provider by reference; the
// host canonicalizes the project root with the task rules and builds the
// fixed payloads {provider, locale, project: {name, root}} (discover) and
// {provider, locale, project, tests: [id]} (run). Bounds: payload ≤
// min(action maxPayloadBytes, payloadMaxBytes); discover ≤
// discoverTimeoutMs, ≤ discoverResultMaxBytes, ≤ testsMax nodes nested ≤
// depthMax levels; run ≤ runTimeoutMs, ≤ runResultMaxBytes, ≤ runTestsMax
// ids. One call per plugin at a time. Invalid or duplicate nodes and results
// are dropped and counted; messages and output are sanitized plain text cut
// to their limits. The frontend runs tests only on explicit user action.
var (
	testProviderPayloadMaxBytes  = int(testProviderSpec.Limit("payloadMaxBytes"))
	testProviderDiscoverMaxBytes = int(testProviderSpec.Limit("discoverResultMaxBytes"))
	testProviderRunMaxBytes      = int(testProviderSpec.Limit("runResultMaxBytes"))
	testProviderTestsMax         = int(testProviderSpec.Limit("testsMax"))
	testProviderTestIDMaxLength  = int(testProviderSpec.Limit("testIdMaxLength"))
	testProviderLabelMaxRunes    = int(testProviderSpec.Limit("labelMaxRunes"))
	testProviderDepthMax         = int(testProviderSpec.Limit("depthMax"))
	testProviderPathMaxLength    = int(testProviderSpec.Limit("pathMaxLength"))
	testProviderLineMax          = int(testProviderSpec.Limit("lineMax"))
	testProviderRunTestsMax      = int(testProviderSpec.Limit("runTestsMax"))
	testProviderMessageMaxRunes  = int(testProviderSpec.Limit("messageMaxRunes"))
	testProviderOutputMaxRunes   = int(testProviderSpec.Limit("outputMaxRunes"))
	testProviderDurationMsMax    = float64(testProviderSpec.Limit("durationMsMax"))
	testProviderDiscoverTimeout  = time.Duration(testProviderSpec.Limit("discoverTimeoutMs")) * time.Millisecond
	testProviderRunTimeout       = time.Duration(testProviderSpec.Limit("runTimeoutMs")) * time.Millisecond
	testProviderConcurrency      = int(testProviderSpec.Limit("concurrentPerPlugin"))
	testProviderStatuses         = testProviderSpec.List("statuses")
)

type testDiscoverRequest struct {
	PluginID    string `json:"pluginId"`
	ProviderID  string `json:"providerId"`
	RequestID   string `json:"requestId"`
	ProjectRoot string `json:"projectRoot"`
	Locale      string `json:"locale,omitempty"`
}

type testRunRequest struct {
	PluginID    string   `json:"pluginId"`
	ProviderID  string   `json:"providerId"`
	RequestID   string   `json:"requestId"`
	ProjectRoot string   `json:"projectRoot"`
	Tests       []string `json:"tests"`
	Locale      string   `json:"locale,omitempty"`
}

type testProviderPayload struct {
	Provider string               `json:"provider"`
	Locale   string               `json:"locale"`
	Project  *contributionProject `json:"project"`
	// Tests is set only for runs; an empty list means all tests.
	Tests *[]string `json:"tests,omitempty"`
}

// TestItem is one validated node of a discovered test tree.
type TestItem struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Path     string     `json:"path,omitempty"`
	Line     int        `json:"line,omitempty"`
	Children []TestItem `json:"children,omitempty"`
}

type testDiscoverResponse struct {
	Tests     []TestItem `json:"tests"`
	Dropped   int        `json:"dropped"`
	Truncated bool       `json:"truncated"`
}

// TestResult is one validated test outcome.
type TestResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
}

type testRunResponse struct {
	Results         []TestResult `json:"results"`
	Output          string       `json:"output,omitempty"`
	OutputTruncated bool         `json:"outputTruncated,omitempty"`
	Dropped         int          `json:"dropped"`
	Truncated       bool         `json:"truncated"`
}

type testDiscoverRawResult struct {
	Tests []json.RawMessage `json:"tests"`
}

type testRawItem struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Path     *string           `json:"path,omitempty"`
	Line     *int              `json:"line,omitempty"`
	Children []json.RawMessage `json:"children,omitempty"`
}

type testRunRawResult struct {
	Results []json.RawMessage `json:"results"`
	Output  *string           `json:"output,omitempty"`
}

type testRawResult struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Message    *string  `json:"message,omitempty"`
	DurationMs *float64 `json:"durationMs,omitempty"`
}

// testProviderCall is the resolved provider and payload of one call.
type testProviderCall struct {
	provider TestProviderContribution
	encoded  []byte
}

func (manager *pluginManager) prepareTestProviderCall(pluginID, providerID, projectRoot, locale string, tests *[]string, actionOf func(TestProviderContribution) string) (testProviderCall, error) {
	if _, _, ok := enabledContributionPoint(testProviderPointKey); !ok {
		return testProviderCall{}, newContributionError(contributionErrUnavailable, "test providers are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return testProviderCall{}, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	provider, lookupErr := testProviderLocked(record, providerID)
	var action ManifestUIAction
	if lookupErr == nil {
		_, _, actions := manifestCommandMaps(record.manifest)
		action = actions[actionOf(provider)]
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return testProviderCall{}, lookupErr
	}
	project, err := contributionProjectFor(projectRoot, true)
	if err != nil {
		return testProviderCall{}, err
	}
	encoded, err := encodeContributionJSON(testProviderPayload{Provider: provider.ID, Locale: locale, Project: project, Tests: tests})
	if err != nil {
		return testProviderCall{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	limit := testProviderPayloadMaxBytes
	if action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return testProviderCall{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, test provider %q allows %d", len(encoded), provider.ID, limit)
	}
	return testProviderCall{provider: provider, encoded: encoded}, nil
}

func testProviderUnchanged(provider TestProviderContribution) func(record *pluginRecord) error {
	return func(record *pluginRecord) error {
		current, err := testProviderLocked(record, provider.ID)
		if err != nil {
			return err
		}
		if !slices.Equal(testProviderActions(current), testProviderActions(provider)) {
			return newContributionError(contributionErrUnavailable, "test provider %q changed", provider.ID)
		}
		return nil
	}
}

func (manager *pluginManager) discoverTests(ctx context.Context, request testDiscoverRequest) (testDiscoverResponse, error) {
	locale, err := normalizeTestProviderRequest(request.PluginID, request.ProviderID, request.Locale)
	if err != nil {
		return testDiscoverResponse{}, err
	}
	call, err := manager.prepareTestProviderCall(request.PluginID, request.ProviderID, request.ProjectRoot, locale, nil,
		func(provider TestProviderContribution) string { return provider.DiscoverAction })
	if err != nil {
		return testDiscoverResponse{}, err
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: testProviderPointKey, EntryID: call.provider.ID, ActionID: call.provider.DiscoverAction,
		RequestID: request.RequestID, Payload: call.encoded, Timeout: testProviderDiscoverTimeout,
		MaxResultBytes: testProviderDiscoverMaxBytes, Concurrency: testProviderConcurrency,
		Validate: testProviderUnchanged(call.provider),
	})
	if err != nil {
		return testDiscoverResponse{}, err
	}
	response, err := parseTestDiscoverResult(raw)
	if err != nil {
		return testDiscoverResponse{}, newContributionError(contributionErrInvalidResult, "test provider %q: %v", call.provider.ID, err)
	}
	return response, nil
}

func (manager *pluginManager) runTests(ctx context.Context, request testRunRequest) (testRunResponse, error) {
	locale, err := normalizeTestProviderRequest(request.PluginID, request.ProviderID, request.Locale)
	if err != nil {
		return testRunResponse{}, err
	}
	if request.Tests == nil {
		request.Tests = []string{}
	}
	if len(request.Tests) > testProviderRunTestsMax {
		return testRunResponse{}, newContributionError(contributionErrInvalidRequest, "at most %d tests can run at once", testProviderRunTestsMax)
	}
	for index, id := range request.Tests {
		if !validateContributionItemID(id, testProviderTestIDMaxLength) || slices.Contains(request.Tests[:index], id) {
			return testRunResponse{}, newContributionError(contributionErrInvalidRequest, "tests[%d] is invalid or repeated", index)
		}
	}
	call, err := manager.prepareTestProviderCall(request.PluginID, request.ProviderID, request.ProjectRoot, locale, &request.Tests,
		func(provider TestProviderContribution) string { return provider.RunAction })
	if err != nil {
		return testRunResponse{}, err
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: testProviderPointKey, EntryID: call.provider.ID, ActionID: call.provider.RunAction,
		RequestID: request.RequestID, Payload: call.encoded, Timeout: testProviderRunTimeout,
		MaxResultBytes: testProviderRunMaxBytes, Concurrency: testProviderConcurrency,
		Validate: testProviderUnchanged(call.provider),
	})
	if err != nil {
		return testRunResponse{}, err
	}
	response, err := parseTestRunResult(raw)
	if err != nil {
		return testRunResponse{}, newContributionError(contributionErrInvalidResult, "test provider %q: %v", call.provider.ID, err)
	}
	return response, nil
}

func normalizeTestProviderRequest(pluginID, providerID, locale string) (string, error) {
	if !pluginIDPattern.MatchString(pluginID) {
		return "", newContributionError(contributionErrInvalidRequest, "pluginId %q is invalid", pluginID)
	}
	if !testProviderIDPattern.MatchString(providerID) {
		return "", newContributionError(contributionErrInvalidRequest, "providerId %q is invalid", providerID)
	}
	if locale == "" {
		locale = "en"
	}
	if len(locale) > 35 || !searchLocalePattern.MatchString(locale) {
		return "", newContributionError(contributionErrInvalidRequest, "locale %q is invalid", locale)
	}
	return locale, nil
}

// testProviderLocked finds the provider in the record's current generation.
// Called with record.mu held.
func testProviderLocked(record *pluginRecord, providerID string) (TestProviderContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(testProviderPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if provider, ok := entry.(TestProviderContribution); ok && provider.ID == providerID {
					return provider, nil
				}
			}
		}
	}
	return TestProviderContribution{}, newContributionError(contributionErrNotFound, "test provider %q is not contributed by %q", providerID, record.id)
}

// testTreeParser walks a discovered tree keeping the global node budget and
// id set.
type testTreeParser struct {
	seen      map[string]bool
	count     int
	dropped   int
	truncated bool
}

// parseTestDiscoverResult validates {"tests": [...]}. The top level is
// strict; an invalid or duplicate node is dropped with its children and
// counted, nodes beyond depthMax are dropped, and the walk stops after
// testsMax nodes (truncated).
func parseTestDiscoverResult(raw json.RawMessage) (testDiscoverResponse, error) {
	var result testDiscoverRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return testDiscoverResponse{}, fmt.Errorf("result must be {\"tests\": [...]}: %v", err)
	}
	if result.Tests == nil {
		return testDiscoverResponse{}, errors.New("result must contain tests")
	}
	parser := &testTreeParser{seen: map[string]bool{}}
	tests := parser.parseLevel(result.Tests, 1)
	return testDiscoverResponse{Tests: tests, Dropped: parser.dropped, Truncated: parser.truncated}, nil
}

func (parser *testTreeParser) parseLevel(rawItems []json.RawMessage, depth int) []TestItem {
	items := []TestItem{}
	for _, rawItem := range rawItems {
		if parser.count >= testProviderTestsMax {
			parser.truncated = true
			return items
		}
		if depth > testProviderDepthMax {
			parser.dropped++
			continue
		}
		var node testRawItem
		if err := decodeStrictJSON(rawItem, &node, true); err != nil || !validateContributionItemID(node.ID, testProviderTestIDMaxLength) || parser.seen[node.ID] {
			parser.dropped++
			continue
		}
		label, err := validateContributionLine(node.Label, testProviderLabelMaxRunes, true)
		if err != nil {
			parser.dropped++
			continue
		}
		item := TestItem{ID: node.ID, Label: label}
		if node.Path != nil {
			if item.Path, err = validateContributionRelativePath(*node.Path, testProviderPathMaxLength); err != nil {
				parser.dropped++
				continue
			}
		}
		if node.Line != nil {
			if *node.Line < 1 || *node.Line > testProviderLineMax || item.Path == "" {
				parser.dropped++
				continue
			}
			item.Line = *node.Line
		}
		parser.seen[node.ID] = true
		parser.count++
		if len(node.Children) > 0 {
			item.Children = parser.parseLevel(node.Children, depth+1)
			if len(item.Children) == 0 {
				item.Children = nil
			}
		}
		items = append(items, item)
	}
	return items
}

// parseTestRunResult validates {"results": [...], "output"?: string}. The top
// level is strict; invalid or duplicate results are dropped and counted,
// results past testsMax are cut. Messages and output are sanitized and cut.
func parseTestRunResult(raw json.RawMessage) (testRunResponse, error) {
	var result testRunRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return testRunResponse{}, fmt.Errorf("result must be {\"results\": [...]}: %v", err)
	}
	if result.Results == nil {
		return testRunResponse{}, errors.New("result must contain results")
	}
	response := testRunResponse{Results: []TestResult{}}
	rawResults := result.Results
	if len(rawResults) > testProviderTestsMax {
		response.Truncated = true
		rawResults = rawResults[:testProviderTestsMax]
	}
	seen := map[string]bool{}
	for _, rawResult := range rawResults {
		var item testRawResult
		if err := decodeStrictJSON(rawResult, &item, true); err != nil || !validateContributionItemID(item.ID, testProviderTestIDMaxLength) ||
			seen[item.ID] || !slices.Contains(testProviderStatuses, item.Status) {
			response.Dropped++
			continue
		}
		out := TestResult{ID: item.ID, Status: item.Status}
		if item.Message != nil {
			out.Message, _ = sanitizeContributionText(*item.Message, testProviderMessageMaxRunes)
		}
		if item.DurationMs != nil {
			duration := *item.DurationMs
			if math.IsNaN(duration) || duration < 0 || duration > testProviderDurationMsMax {
				response.Dropped++
				continue
			}
			rounded := int64(math.Round(duration))
			out.DurationMs = &rounded
		}
		seen[item.ID] = true
		response.Results = append(response.Results, out)
	}
	if result.Output != nil {
		response.Output, response.OutputTruncated = sanitizeContributionText(*result.Output, testProviderOutputMaxRunes)
	}
	return response, nil
}

func pluginHostTestDiscoverHandler(_, args string) (string, error) {
	var request testDiscoverRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_test_discover: invalid arguments: %v", err)
	}
	response, err := host().discoverTests(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func pluginHostTestRunHandler(_, args string) (string, error) {
	var request testRunRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_test_run: invalid arguments: %v", err)
	}
	response, err := host().runTests(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_test_discover", pluginHostTestDiscoverHandler)
	core.Register("plugin_host_test_run", pluginHostTestRunHandler)
}
