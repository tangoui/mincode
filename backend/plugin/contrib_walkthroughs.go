package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"mincode/app/plugins/api/svgsafe"
)

// Walkthroughs (contributes.walkthroughs) are short, user-opened guides shown
// in the plugin details drawer (and, for one featured walkthrough per
// plugin, on the dashboard's "Get started" card). Declarative and process
// plugins may contribute them. Step bodies are either plain text or a
// package markdown file; step images are package SVG/PNG files. The host
// never inlines asset bytes: markdown, SVG and PNG files become content-hash
// keys of canonical assets (markdown normalized, SVG re-serialized by
// svgsafe, PNG re-encoded), served by plugin_host_contribution_assets and
// re-validated on every fetch. The frontend renders markdown through
// renderPluginMarkdownHtml (no raw HTML, no inline images, http(s) links via
// the system browser) and images only in image context. A step action may
// only name one of the plugin's own commands (process plugins only) and runs
// through the normal command path. Nothing opens automatically. Mirrors:
// tools/mincode-cli/walkthroughs.go (CLI), assertWalkthroughsEntry (cloud)
// and vue-app/src/domain/plugins/walkthroughs.ts (frontend revalidation).
const (
	walkthroughPointKey        = "walkthroughs"
	walkthroughMarkdownHint    = "markdown"
	walkthroughImageHint       = "image"
	walkthroughMarkdownMime    = "text/markdown"
	walkthroughMarkdownExt     = ".md"
	walkthroughIDPatternSource = "^[a-z0-9][a-z0-9-]{0,31}$"
)

var (
	walkthroughSpec                = mustContributionSpec(walkthroughPointKey)
	walkthroughStepsMax            = int(walkthroughSpec.Limit("stepsMax"))
	walkthroughTitleMaxRunes       = int(walkthroughSpec.Limit("titleMaxRunes"))
	walkthroughDescriptionMaxRunes = int(walkthroughSpec.Limit("descriptionMaxRunes"))
	walkthroughLocalesMax          = int(walkthroughSpec.Limit("localesMax"))
	walkthroughTextMaxRunes        = int(walkthroughSpec.Limit("textMaxRunes"))
	walkthroughMarkdownMaxBytes    = int(walkthroughSpec.Limit("markdownMaxBytes"))
	walkthroughImageAltMaxRunes    = int(walkthroughSpec.Limit("imageAltMaxRunes"))
	walkthroughImageLimits         = contributionImageLimits{
		PNGMaxDimension: int(walkthroughSpec.Limit("pngMaxDimension")),
		SVG: svgsafe.Limits{
			MaxBytes:     int(walkthroughSpec.Assets.MaxBytes),
			MaxElements:  int(walkthroughSpec.Limit("svgMaxElements")),
			MaxDepth:     int(walkthroughSpec.Limit("svgMaxDepth")),
			MaxAttrBytes: int(walkthroughSpec.Limit("svgMaxAttrBytes")),
			MaxUse:       int(walkthroughSpec.Limit("svgMaxUse")),
			MaxDimension: int(walkthroughSpec.Limit("svgMaxDimension")),
		},
	}
	walkthroughIDPattern = regexp.MustCompile(walkthroughIDPatternSource)
)

type walkthroughStepManifest struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	TitleLocalized    map[string]string `json:"titleLocalized,omitempty"`
	Markdown          *string           `json:"markdown,omitempty"`
	MarkdownLocalized map[string]string `json:"markdownLocalized,omitempty"`
	Text              *string           `json:"text,omitempty"`
	Image             *string           `json:"image,omitempty"`
	ImageAlt          *string           `json:"imageAlt,omitempty"`
	Command           *string           `json:"command,omitempty"`
}

type walkthroughManifestEntry struct {
	ID                   string                    `json:"id"`
	Title                string                    `json:"title"`
	TitleLocalized       map[string]string         `json:"titleLocalized,omitempty"`
	Description          string                    `json:"description,omitempty"`
	DescriptionLocalized map[string]string         `json:"descriptionLocalized,omitempty"`
	Featured             bool                      `json:"featured,omitempty"`
	Steps                []walkthroughStepManifest `json:"steps"`
}

// WalkthroughStepContribution is one snapshot step. Markdown,
// MarkdownLocalized and Image carry content-hash asset keys, never bytes.
type WalkthroughStepContribution struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	TitleLocalized    map[string]string `json:"titleLocalized,omitempty"`
	Markdown          string            `json:"markdown,omitempty"`
	MarkdownLocalized map[string]string `json:"markdownLocalized,omitempty"`
	Text              string            `json:"text,omitempty"`
	Image             string            `json:"image,omitempty"`
	ImageMime         string            `json:"imageMime,omitempty"`
	ImageAlt          string            `json:"imageAlt,omitempty"`
	Command           string            `json:"command,omitempty"`
}

// WalkthroughContribution is the snapshot entry.
type WalkthroughContribution struct {
	ID                   string                        `json:"id"`
	Title                string                        `json:"title"`
	TitleLocalized       map[string]string             `json:"titleLocalized,omitempty"`
	Description          string                        `json:"description,omitempty"`
	DescriptionLocalized map[string]string             `json:"descriptionLocalized,omitempty"`
	Featured             bool                          `json:"featured"`
	Steps                []WalkthroughStepContribution `json:"steps"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              walkthroughPointKey,
		Decode:           decodeWalkthroughEntry,
		Finalize:         finalizeWalkthroughEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate walkthrough id %q", id) },
		IssuePrefix:      "invalid walkthroughs contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped walkthrough %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.walkthroughs[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:     "walkthroughs",
		ValidateAsset: validateWalkthroughAsset,
	})
}

// validateWalkthroughAsset validates a step markdown file (hint "markdown")
// or step image (hint "image") and returns the canonical bytes. The hint
// pins the kind so a markdown field can never serve an image and vice versa.
func validateWalkthroughAsset(hint, ext string, data []byte) ([]byte, string, error) {
	switch hint {
	case walkthroughMarkdownHint:
		if ext != walkthroughMarkdownExt {
			return nil, "", errors.New("walkthrough markdown must be a .md file")
		}
		return canonicalWalkthroughMarkdown(data)
	case walkthroughImageHint:
		if ext != ".png" && ext != ".svg" {
			return nil, "", errors.New("walkthrough image must be a .png or .svg file")
		}
		return validateContributionImage(ext, data, walkthroughImageLimits)
	}
	return nil, "", fmt.Errorf("unknown walkthrough asset kind %q", hint)
}

// canonicalWalkthroughMarkdown strips a UTF-8 BOM, normalizes CRLF and
// applies the multi-line text rules (no controls, bidi overrides or
// zero-width characters) within markdownMaxBytes.
func canonicalWalkthroughMarkdown(data []byte) ([]byte, string, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(data) > walkthroughMarkdownMaxBytes {
		return nil, "", fmt.Errorf("walkthrough markdown must be at most %d bytes", walkthroughMarkdownMaxBytes)
	}
	text, err := validateContributionMultiline(string(data), 0, walkthroughMarkdownMaxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("walkthrough markdown %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, "", errors.New("walkthrough markdown must not be empty")
	}
	return []byte(text), walkthroughMarkdownMime, nil
}

func decodeWalkthroughEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry walkthroughManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse walkthrough: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !walkthroughIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("walkthrough id %q must match %s", id, walkthroughIDPatternSource)
	}
	walkthrough := WalkthroughContribution{ID: id, Featured: entry.Featured}
	var err error
	if walkthrough.Title, err = validateContributionLine(entry.Title, walkthroughTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("walkthrough %q title %w", id, err)
	}
	if walkthrough.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, walkthroughTitleMaxRunes, walkthroughLocalesMax); err != nil {
		return nil, id, fmt.Errorf("walkthrough %q titleLocalized %w", id, err)
	}
	if walkthrough.Description, err = validateContributionLine(entry.Description, walkthroughDescriptionMaxRunes, false); err != nil {
		return nil, id, fmt.Errorf("walkthrough %q description %w", id, err)
	}
	if walkthrough.DescriptionLocalized, err = validateContributionLocalized(entry.DescriptionLocalized, walkthroughDescriptionMaxRunes, walkthroughLocalesMax); err != nil {
		return nil, id, fmt.Errorf("walkthrough %q descriptionLocalized %w", id, err)
	}
	if len(entry.Steps) == 0 || len(entry.Steps) > walkthroughStepsMax {
		return nil, id, fmt.Errorf("walkthrough %q needs 1-%d steps", id, walkthroughStepsMax)
	}
	seen := make(map[string]bool, len(entry.Steps))
	walkthrough.Steps = make([]WalkthroughStepContribution, 0, len(entry.Steps))
	for index, source := range entry.Steps {
		step, err := decodeWalkthroughStep(ctx, source)
		if err != nil {
			return nil, id, fmt.Errorf("walkthrough %q steps[%d]: %w", id, index, err)
		}
		if seen[step.ID] {
			return nil, id, fmt.Errorf("walkthrough %q repeats step id %q", id, step.ID)
		}
		seen[step.ID] = true
		walkthrough.Steps = append(walkthrough.Steps, step)
	}
	return walkthrough, id, nil
}

func decodeWalkthroughStep(ctx *pointContext, source walkthroughStepManifest) (WalkthroughStepContribution, error) {
	step := WalkthroughStepContribution{ID: strings.TrimSpace(source.ID)}
	if !walkthroughIDPattern.MatchString(step.ID) {
		return step, fmt.Errorf("step id %q must match %s", step.ID, walkthroughIDPatternSource)
	}
	var err error
	if step.Title, err = validateContributionLine(source.Title, walkthroughTitleMaxRunes, true); err != nil {
		return step, fmt.Errorf("step %q title %w", step.ID, err)
	}
	if step.TitleLocalized, err = validateContributionLocalized(source.TitleLocalized, walkthroughTitleMaxRunes, walkthroughLocalesMax); err != nil {
		return step, fmt.Errorf("step %q titleLocalized %w", step.ID, err)
	}
	if (source.Markdown == nil) == (source.Text == nil) {
		return step, fmt.Errorf("step %q needs exactly one of markdown or text", step.ID)
	}
	if source.MarkdownLocalized != nil && source.Markdown == nil {
		return step, fmt.Errorf("step %q markdownLocalized requires markdown", step.ID)
	}
	if source.Text != nil {
		text, err := validateContributionMultiline(*source.Text, walkthroughTextMaxRunes, 0)
		if err != nil {
			return step, fmt.Errorf("step %q text %w", step.ID, err)
		}
		if strings.TrimSpace(text) == "" {
			return step, fmt.Errorf("step %q text is required", step.ID)
		}
		step.Text = text
	}
	if source.Markdown != nil {
		if step.Markdown, err = readWalkthroughMarkdown(ctx, *source.Markdown); err != nil {
			return step, fmt.Errorf("step %q markdown: %w", step.ID, err)
		}
		if len(source.MarkdownLocalized) > walkthroughLocalesMax {
			return step, fmt.Errorf("step %q markdownLocalized must have at most %d locales", step.ID, walkthroughLocalesMax)
		}
		locales := make([]string, 0, len(source.MarkdownLocalized))
		for locale := range source.MarkdownLocalized {
			locales = append(locales, locale)
		}
		sort.Strings(locales)
		for _, locale := range locales {
			if strings.TrimSpace(locale) != locale || locale == "" || len(locale) > 32 || checkContributionRunes(locale, false, false) != nil {
				return step, fmt.Errorf("step %q markdownLocalized locale %q is invalid", step.ID, locale)
			}
			key, err := readWalkthroughMarkdown(ctx, source.MarkdownLocalized[locale])
			if err != nil {
				return step, fmt.Errorf("step %q markdownLocalized.%s: %w", step.ID, locale, err)
			}
			if step.MarkdownLocalized == nil {
				step.MarkdownLocalized = make(map[string]string, len(locales))
			}
			step.MarkdownLocalized[locale] = key
		}
	}
	if source.ImageAlt != nil && source.Image == nil {
		return step, fmt.Errorf("step %q imageAlt requires image", step.ID)
	}
	if source.Image != nil {
		if source.ImageAlt == nil {
			return step, fmt.Errorf("step %q image requires imageAlt", step.ID)
		}
		if step.ImageAlt, err = validateContributionLine(*source.ImageAlt, walkthroughImageAltMaxRunes, true); err != nil {
			return step, fmt.Errorf("step %q imageAlt %w", step.ID, err)
		}
		relative := strings.TrimSpace(*source.Image)
		ext := strings.ToLower(relative)
		switch {
		case strings.HasSuffix(ext, ".png"):
			step.ImageMime = contributionImageMimePNG
		case strings.HasSuffix(ext, ".svg"):
			step.ImageMime = contributionImageMimeSVG
		default:
			return step, fmt.Errorf("step %q image must be a .png or .svg file", step.ID)
		}
		if step.Image, err = ctx.readAsset(relative, walkthroughImageHint); err != nil {
			return step, fmt.Errorf("step %q image: %w", step.ID, err)
		}
	}
	if source.Command != nil {
		command := strings.TrimSpace(*source.Command)
		if ctx.manifest.Runtime.Type != "process" {
			return step, fmt.Errorf("step %q command requires runtime.type process", step.ID)
		}
		if _, ok := ctx.commands[command]; command == "" || !ok {
			return step, fmt.Errorf("step %q command %q is not one of this plugin's contributes.commands", step.ID, command)
		}
		step.Command = command
	}
	return step, nil
}

func readWalkthroughMarkdown(ctx *pointContext, relative string) (string, error) {
	relative = strings.TrimSpace(relative)
	if !strings.HasSuffix(strings.ToLower(relative), walkthroughMarkdownExt) {
		return "", errors.New("must be a package .md file")
	}
	return ctx.readAsset(relative, walkthroughMarkdownHint)
}

// finalizeWalkthroughEntries keeps at most one featured walkthrough per
// plugin: every later featured entry becomes an issue.
func finalizeWalkthroughEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	featured := ""
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		walkthrough, ok := entry.Value.(WalkthroughContribution)
		if ok && walkthrough.Featured {
			if featured != "" {
				issues = append(issues, ContributionIssue{
					Point: walkthroughPointKey, Entry: entry.Index, ID: entry.ID,
					Message: fmt.Sprintf("only one walkthrough may be featured (already %q)", featured),
				})
				continue
			}
			featured = walkthrough.ID
		}
		kept = append(kept, entry)
	}
	return kept, issues
}
