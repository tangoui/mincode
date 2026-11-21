// Package filetmpl shares data-only template validation between host resources and plugins.
package filetmpl

import (
	"bytes"
	"errors"
	"fmt"
	"mincode/app/plugins/api"
	"regexp"
	"strings"
	"unicode/utf8"
)

var fileTemplateSpec, _ = api.ContributionPointSpecFor("fileTemplates")
var fileTemplatePathMaxLength = int(fileTemplateSpec.Limit("targetPathMaxLength"))
var fileTemplateDepthMax = int(fileTemplateSpec.Limit("targetDepthMax"))
var fileTemplateVariables = fileTemplateSpec.List("variables")
var fileTemplateDeniedSegments = fileTemplateSpec.List("deniedPathSegments")
var fileTemplateSensitiveNames = fileTemplateSpec.SensitiveFilenames
var fileTemplateVariableToken = regexp.MustCompile(`\{\{mincode\.([^{}]*)\}\}`)
var fileTemplateSegmentCharacters = regexp.MustCompile(`^[A-Za-z0-9._@+-]*$`)

const fileTemplateMime = "text/plain"

func TargetPath(value string) (string, error) {
	path := value
	if path == "" || strings.TrimSpace(path) != path {
		return "", errors.New("must be a non-empty relative path without surrounding spaces")
	}
	if len(path) > fileTemplatePathMaxLength {
		return "", fmt.Errorf("must be at most %d bytes", fileTemplatePathMaxLength)
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || (len(path) >= 2 && path[1] == ':') {
		return "", errors.New("must be a relative POSIX path")
	}
	segments := strings.Split(path, "/")
	if len(segments) > fileTemplateDepthMax {
		return "", fmt.Errorf("must have at most %d segments", fileTemplateDepthMax)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New(`must not contain empty, "." or ".." segments`)
		}
		literal := segment
		for _, match := range fileTemplateVariableToken.FindAllStringSubmatch(segment, -1) {
			if !isFileTemplateVariable(match[1]) {
				return "", fmt.Errorf("uses unknown variable {{mincode.%s}}", match[1])
			}
		}
		literal = fileTemplateVariableToken.ReplaceAllString(literal, "")
		if !fileTemplateSegmentCharacters.MatchString(literal) {
			return "", fmt.Errorf("segment %q may only use A-Z a-z 0-9 . _ @ + - and {{mincode.<variable>}}", segment)
		}
		if isFileTemplateDeniedSegment(segment) {
			return "", fmt.Errorf("must not use the %q directory", segment)
		}
	}
	if isFileTemplateSensitiveName(segments[len(segments)-1]) {
		return "", fmt.Errorf("must not create the sensitive file %q", segments[len(segments)-1])
	}
	return path, nil
}

func isFileTemplateVariable(name string) bool {
	for _, allowed := range fileTemplateVariables {
		if name == allowed {
			return true
		}
	}
	return false
}

func isFileTemplateDeniedSegment(segment string) bool {
	for _, denied := range fileTemplateDeniedSegments {
		if strings.EqualFold(segment, denied) {
			return true
		}
	}
	return false
}

func isFileTemplateSensitiveName(name string) bool {
	for _, sensitive := range fileTemplateSensitiveNames {
		if strings.EqualFold(name, sensitive) {
			return true
		}
	}
	return false
}

// Asset checks a .tmpl source: UTF-8 (BOM stripped), no
// NUL or C0 controls except tab, LF, CR and form feed, no bidirectional
// controls, and only allowlisted {{mincode.<variable>}} tokens. The canonical
// form is the BOM-stripped bytes.
func Asset(_ string, ext string, data []byte) ([]byte, string, error) {
	if ext != ".tmpl" {
		return nil, "", fmt.Errorf("unsupported template extension %q", ext)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, "", errors.New("must be valid UTF-8")
	}
	for _, r := range string(data) {
		switch {
		case r == '\t', r == '\n', r == '\r', r == '\f':
		case r < 0x20:
			return nil, "", fmt.Errorf("contains a control character U+%04X", r)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return nil, "", fmt.Errorf("contains a bidirectional control character U+%04X", r)
		}
	}
	for _, match := range fileTemplateVariableToken.FindAllSubmatch(data, -1) {
		if !isFileTemplateVariable(string(match[1])) {
			return nil, "", fmt.Errorf("uses unknown variable {{mincode.%s}}", match[1])
		}
	}
	return data, fileTemplateMime, nil
}
