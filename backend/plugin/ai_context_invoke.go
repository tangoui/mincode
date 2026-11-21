package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// plugin_host_ai_context fetches one AI context provider's text. The WebView
// names the provider only by reference ({pluginId, providerId, requestId,
// argument?, locale?}) after the user picked its @mention and pressed Fetch;
// no document, selection or conversation text is sent. The host builds the
// fixed payload {"provider", "argument"?, "locale"} and returns sanitised
// plain text: hidden, control and bidirectional characters are removed and
// counted, CRLF becomes LF and text beyond resultMaxChars is cut. The AI tool
// layer never reaches this command (the action must not be aiCallable).
var (
	aiContextArgumentMaxRunes    = int(aiContextProviderSpec.Limit("argumentMaxRunes"))
	aiContextPayloadMaxBytes     = int(aiContextProviderSpec.Limit("payloadMaxBytes"))
	aiContextResultMaxBytes      = int(aiContextProviderSpec.Limit("resultMaxBytes"))
	aiContextResultMaxChars      = int(aiContextProviderSpec.Limit("resultMaxChars"))
	aiContextResultTitleMaxRunes = int(aiContextProviderSpec.Limit("resultTitleMaxRunes"))
	aiContextTimeout             = time.Duration(aiContextProviderSpec.Limit("timeoutMs")) * time.Millisecond
	aiContextConcurrency         = int(aiContextProviderSpec.Limit("concurrentPerPlugin"))

	aiContextLocalePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8}){0,3}$`)
)

type aiContextRequest struct {
	PluginID   string  `json:"pluginId"`
	ProviderID string  `json:"providerId"`
	RequestID  string  `json:"requestId"`
	Argument   *string `json:"argument,omitempty"`
	Locale     string  `json:"locale,omitempty"`
}

type aiContextPayload struct {
	Provider string  `json:"provider"`
	Argument *string `json:"argument,omitempty"`
	Locale   string  `json:"locale"`
}

type aiContextActionResult struct {
	Text      *string `json:"text"`
	Title     *string `json:"title"`
	Truncated bool    `json:"truncated"`
}

type aiContextResponse struct {
	Text              string `json:"text"`
	Title             string `json:"title,omitempty"`
	Truncated         bool   `json:"truncated"`
	RemovedCharacters int    `json:"removedCharacters"`
}

func (manager *pluginManager) aiContext(ctx context.Context, request aiContextRequest) (aiContextResponse, error) {
	if err := normalizeAiContextRequest(&request); err != nil {
		return aiContextResponse{}, err
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return aiContextResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	record.mu.Lock()
	provider, lookupErr := aiContextProviderForRequestLocked(record, request)
	record.mu.Unlock()
	if lookupErr != nil {
		return aiContextResponse{}, lookupErr
	}
	payload := aiContextPayload{Provider: provider.ID, Argument: request.Argument, Locale: request.Locale}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return aiContextResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if len(encoded) > aiContextPayloadMaxBytes {
		return aiContextResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, the limit is %d", len(encoded), aiContextPayloadMaxBytes)
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: aiContextProviderPointKey, EntryID: provider.ID, ActionID: provider.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: aiContextTimeout,
		MaxResultBytes: aiContextResultMaxBytes, Concurrency: aiContextConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := aiContextProviderForRequestLocked(record, request)
			if err != nil {
				return err
			}
			if current.Action != provider.Action {
				return newContributionError(contributionErrUnavailable, "AI context provider %q changed its action", provider.ID)
			}
			return nil
		},
	})
	if err != nil {
		return aiContextResponse{}, err
	}
	return parseAiContextResult(raw)
}

func normalizeAiContextRequest(request *aiContextRequest) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	request.PluginID = strings.TrimSpace(request.PluginID)
	if request.PluginID == "" {
		return invalid("pluginId is required")
	}
	if !aiContextProviderIDPattern.MatchString(request.ProviderID) {
		return invalid("providerId %q is invalid", request.ProviderID)
	}
	if request.Locale == "" {
		request.Locale = "en"
	} else if !aiContextLocalePattern.MatchString(request.Locale) {
		return invalid("locale %q is invalid", request.Locale)
	}
	if request.Argument != nil {
		argument, err := validateContributionLine(*request.Argument, aiContextArgumentMaxRunes, false)
		if err != nil {
			return invalid("argument %v", err)
		}
		if argument == "" {
			request.Argument = nil
		} else {
			request.Argument = &argument
		}
	}
	return nil
}

// aiContextProviderForRequestLocked finds the provider in the record's
// current generation and checks the argument against its declaration.
// Called with record.mu held.
func aiContextProviderForRequestLocked(record *pluginRecord, request aiContextRequest) (AiContextProviderContribution, error) {
	if record.manifest == nil {
		return AiContextProviderContribution{}, newContributionError(contributionErrNotFound, "plugin %q has no manifest", request.PluginID)
	}
	if resolved := record.manifest.contribution(aiContextProviderPointKey); resolved != nil {
		for _, entry := range resolved.Entries {
			provider, ok := entry.(AiContextProviderContribution)
			if !ok || provider.ID != request.ProviderID {
				continue
			}
			if provider.Argument == nil && request.Argument != nil {
				return provider, newContributionError(contributionErrInvalidRequest, "AI context provider %q takes no argument", provider.ID)
			}
			if provider.Argument != nil && provider.Argument.Required && request.Argument == nil {
				return provider, newContributionError(contributionErrInvalidRequest, "AI context provider %q requires an argument", provider.ID)
			}
			return provider, nil
		}
	}
	return AiContextProviderContribution{}, newContributionError(contributionErrNotFound, "AI context provider %q is not contributed by %q", request.ProviderID, request.PluginID)
}

// isAiContextRemovedRune reports characters removed from provider text: C0
// controls except \t and \n, DEL, C1 controls, U+2028/2029, bidi controls and
// zero-width characters.
func isAiContextRemovedRune(r rune) bool {
	return isUnsafeContributionRune(r, true, true)
}

// parseAiContextResult validates {"text", "title"?, "truncated"?}.
func parseAiContextResult(raw json.RawMessage) (aiContextResponse, error) {
	if !utf8.Valid(raw) {
		return aiContextResponse{}, newContributionError(contributionErrInvalidResult, "result must be valid UTF-8")
	}
	var result aiContextActionResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return aiContextResponse{}, newContributionError(contributionErrInvalidResult, "result must be {\"text\": string, \"title\"?: string, \"truncated\"?: boolean}: %v", err)
	}
	if result.Text == nil {
		return aiContextResponse{}, newContributionError(contributionErrInvalidResult, "result must include text")
	}
	text := *result.Text
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return aiContextResponse{}, newContributionError(contributionErrInvalidResult, "text must be valid UTF-8 without NUL")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	removed := 0
	text = strings.Map(func(r rune) rune {
		if isAiContextRemovedRune(r) {
			removed++
			return -1
		}
		return r
	}, text)
	response := aiContextResponse{Truncated: result.Truncated, RemovedCharacters: removed}
	if runes := []rune(text); len(runes) > aiContextResultMaxChars {
		text = string(runes[:aiContextResultMaxChars])
		response.Truncated = true
	}
	response.Text = text
	if result.Title != nil {
		title, err := validateContributionLine(*result.Title, aiContextResultTitleMaxRunes, false)
		if err != nil {
			return aiContextResponse{}, newContributionError(contributionErrInvalidResult, "title %v", err)
		}
		response.Title = title
	}
	return response, nil
}

func pluginHostAiContextHandler(_, args string) (string, error) {
	var request aiContextRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_ai_context: invalid arguments: %v", err)
	}
	response, err := host().aiContext(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_ai_context", pluginHostAiContextHandler)
}
