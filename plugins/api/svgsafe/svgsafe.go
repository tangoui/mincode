// Package svgsafe sanitizes plugin-supplied SVG icons into a canonical,
// script-free subset before MinCode hashes and serves them (fileIcons). It is
// shared verbatim by the desktop host and the `mincode plugin` CLI:
// vue-app/scripts/build-plugin-contracts.mjs copies this file to
// tools/mincode-cli/svgsafe_gen.go (package clause rewritten to main) and
// check:plugin-contracts fails on drift. Keep it dependency-free (standard
// library only) and in this single file.
//
// Policy (Phase D decision 12): allowlist and REJECT. Anything outside the
// allowed subset fails the whole document; nothing is silently stripped
// except comments and insignificant whitespace. The output is a canonical
// re-serialization (no XML declaration, default SVG namespace, attributes in
// sorted order, escaped values) that is itself re-validated to a fixed point,
// so the bytes MinCode serves are never the author's file.
//
// Rejected: DOCTYPE/ENTITY declarations, processing instructions other than
// a leading XML declaration, CDATA sections, elements outside the allowlist
// (script, style, foreignObject, image, text, a, animate*, set, filters ...),
// elements or attributes in foreign namespaces, `style` and every `on*`
// attribute, href/xlink:href other than a local `#id` fragment on use and
// gradients, url() other than exactly `url(#id)`, CSS functions outside a
// small allowlist, `javascript:`/`data:`/external URLs, escapes and quotes
// in attribute values, nested <use> expansion, and documents over the size,
// element, depth, attribute-value, <use> or dimension limits.
package svgsafe

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits bounds one SVG document. Values come from the fileIcons contract
// limits (svgMaxElements, svgMaxDepth, svgMaxAttrBytes, svgMaxUse,
// svgMaxDimension) and assets.maxBytes. Every field must be positive.
type Limits struct {
	MaxBytes     int
	MaxElements  int
	MaxDepth     int
	MaxAttrBytes int
	MaxUse       int
	MaxDimension int
}

const (
	svgNamespace   = "http://www.w3.org/2000/svg"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
	xmlNamespace   = "http://www.w3.org/XML/1998/namespace"

	// maxViewBoxMagnitude bounds every viewBox number.
	maxViewBoxMagnitude = 65536
)

// allowedElements is the element allowlist (SVG namespace only).
var allowedElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true,
	"linearGradient": true, "radialGradient": true, "stop": true,
	"clipPath": true, "mask": true, "title": true, "desc": true,
}

// textElements may hold character data (and nothing else).
var textElements = map[string]bool{"title": true, "desc": true}

// hrefElements may carry href / xlink:href (a local #fragment only).
var hrefElements = map[string]bool{"use": true, "linearGradient": true, "radialGradient": true}

// allowedAttributes is the unprefixed attribute allowlist. href, xlink:href
// (mapped to href), xml:space and the namespace declarations are handled
// separately.
var allowedAttributes = map[string]bool{
	// core and accessibility
	"id": true, "role": true, "aria-hidden": true, "focusable": true, "version": true,
	// geometry
	"x": true, "y": true, "width": true, "height": true, "rx": true, "ry": true,
	"cx": true, "cy": true, "r": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"fx": true, "fy": true, "fr": true, "points": true, "d": true, "pathLength": true,
	"viewBox": true, "preserveAspectRatio": true, "transform": true,
	// gradients and stops
	"gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"offset": true, "stop-color": true, "stop-opacity": true,
	// clipping and masking
	"clip-path": true, "clip-rule": true, "clipPathUnits": true,
	"mask": true, "maskUnits": true, "maskContentUnits": true,
	// paint
	"fill": true, "fill-opacity": true, "fill-rule": true,
	"stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true,
	"stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"stroke-opacity": true, "opacity": true, "color": true, "visibility": true,
	"display": true, "overflow": true, "shape-rendering": true, "vector-effect": true,
	"paint-order": true, "color-interpolation": true,
}

// allowedFunctions are the CSS/transform functions an attribute value may
// call. url() is further restricted to url(#id).
var allowedFunctions = map[string]bool{
	"url": true, "matrix": true, "translate": true, "scale": true, "rotate": true,
	"skewx": true, "skewy": true, "rgb": true, "rgba": true, "hsl": true, "hsla": true,
}

var (
	idPattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	fragmentPattern  = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	functionPattern  = regexp.MustCompile(`([a-z0-9_-]*)\s*\(`)
	urlPattern       = regexp.MustCompile(`(?i)url\s*\(`)
	urlTargetPattern = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.-]{0,127}\)`)
	numberPattern    = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	dimensionPattern = regexp.MustCompile(`^(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:px)?$`)
	// bannedValueParts are rejected anywhere in a lower-cased attribute value.
	bannedValueParts = []string{"javascript:", "vbscript:", "data:", "://", "expression(", "@import", "/*", "-moz-binding", "behavior:"}
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

type attribute struct {
	name  string
	value string
}

type node struct {
	name     string
	attrs    []attribute
	children []*node
	text     strings.Builder
	id       string
	href     string
	hasUse   bool
}

func (limits Limits) validate() error {
	if limits.MaxBytes <= 0 || limits.MaxElements <= 0 || limits.MaxDepth <= 0 ||
		limits.MaxAttrBytes <= 0 || limits.MaxUse <= 0 || limits.MaxDimension <= 0 {
		return errors.New("svg: limits must all be positive")
	}
	return nil
}

// Sanitize parses data as SVG, rejects anything outside the allowed subset
// (scripts, event handlers, external references, foreignObject, oversized
// documents) and returns the canonical serialization that is hashed and
// served. It never returns partially sanitized output on error.
func Sanitize(data []byte, limits Limits) ([]byte, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	root, err := parse(data, limits)
	if err != nil {
		return nil, err
	}
	canonical := serialize(root)
	if len(canonical) > limits.MaxBytes {
		return nil, fmt.Errorf("svg: canonical form exceeds %d bytes", limits.MaxBytes)
	}
	// Defence in depth: the canonical form must re-validate to itself.
	again, err := parse(canonical, limits)
	if err != nil {
		return nil, fmt.Errorf("svg: canonical form does not re-validate: %w", err)
	}
	if !bytes.Equal(serialize(again), canonical) {
		return nil, errors.New("svg: canonical form is not stable")
	}
	return canonical, nil
}

func parse(data []byte, limits Limits) (*node, error) {
	if len(data) > limits.MaxBytes {
		return nil, fmt.Errorf("svg: document exceeds %d bytes", limits.MaxBytes)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if !utf8.Valid(data) {
		return nil, errors.New("svg: document must be UTF-8")
	}
	if bytes.Contains(data, []byte("<![CDATA[")) {
		return nil, errors.New("svg: CDATA sections are not allowed")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	// No CharsetReader: a non-UTF-8 encoding declaration is an error.
	var (
		root     *node
		stack    []*node
		elements int
		uses     int
		first    = true
	)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("svg: malformed XML: %w", err)
		}
		isFirst := first
		first = false
		switch token := token.(type) {
		case xml.ProcInst:
			if token.Target != "xml" || !isFirst {
				return nil, fmt.Errorf("svg: processing instruction <?%s?> is not allowed", token.Target)
			}
		case xml.Directive:
			return nil, errors.New("svg: DOCTYPE and entity declarations are not allowed")
		case xml.Comment:
			// Dropped.
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(token)) != "" {
					return nil, errors.New("svg: text outside the root element is not allowed")
				}
				continue
			}
			top := stack[len(stack)-1]
			if !textElements[top.name] {
				if strings.TrimSpace(string(token)) != "" {
					return nil, fmt.Errorf("svg: text content in <%s> is not allowed", top.name)
				}
				continue
			}
			if err := checkText(string(token)); err != nil {
				return nil, err
			}
			if top.text.Len()+len(token) > limits.MaxAttrBytes {
				return nil, fmt.Errorf("svg: <%s> text exceeds %d bytes", top.name, limits.MaxAttrBytes)
			}
			top.text.Write(token)
		case xml.StartElement:
			if root != nil && len(stack) == 0 {
				return nil, errors.New("svg: only one root element is allowed")
			}
			if token.Name.Space != svgNamespace {
				return nil, fmt.Errorf("svg: element <%s> must be in the SVG namespace", qualifiedName(token.Name))
			}
			name := token.Name.Local
			if !allowedElements[name] {
				return nil, fmt.Errorf("svg: element <%s> is not allowed", name)
			}
			if root == nil && name != "svg" {
				return nil, fmt.Errorf("svg: root element must be <svg>, not <%s>", name)
			}
			if len(stack) > 0 && textElements[stack[len(stack)-1].name] {
				return nil, fmt.Errorf("svg: element <%s> inside <%s> is not allowed", name, stack[len(stack)-1].name)
			}
			elements++
			if elements > limits.MaxElements {
				return nil, fmt.Errorf("svg: more than %d elements", limits.MaxElements)
			}
			if len(stack)+1 > limits.MaxDepth {
				return nil, fmt.Errorf("svg: nesting deeper than %d levels", limits.MaxDepth)
			}
			if name == "use" {
				uses++
				if uses > limits.MaxUse {
					return nil, fmt.Errorf("svg: more than %d <use> elements", limits.MaxUse)
				}
			}
			current := &node{name: name}
			if err := readAttributes(current, token.Attr, limits); err != nil {
				return nil, err
			}
			if name == "use" && current.href == "" {
				return nil, errors.New("svg: <use> needs a local href=\"#id\"")
			}
			if len(stack) == 0 {
				root = current
				if err := checkRoot(current, limits); err != nil {
					return nil, err
				}
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, current)
			}
			stack = append(stack, current)
		case xml.EndElement:
			// The strict decoder already verified the element matches.
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, errors.New("svg: document has no <svg> root element")
	}
	if len(stack) != 0 {
		return nil, errors.New("svg: unterminated element")
	}
	if err := checkReferences(root); err != nil {
		return nil, err
	}
	return root, nil
}

func qualifiedName(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	return name.Space + ":" + name.Local
}

func readAttributes(current *node, attrs []xml.Attr, limits Limits) error {
	seen := map[string]bool{}
	for _, attr := range attrs {
		name, keep, err := canonicalAttributeName(current.name, attr)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		if seen[name] {
			return fmt.Errorf("svg: <%s> repeats attribute %s", current.name, name)
		}
		seen[name] = true
		if err := checkValue(current.name, name, attr.Value, limits); err != nil {
			return err
		}
		switch name {
		case "id":
			current.id = attr.Value
		case "href":
			current.href = attr.Value
		}
		current.attrs = append(current.attrs, attribute{name: name, value: attr.Value})
	}
	sort.Slice(current.attrs, func(i, j int) bool { return current.attrs[i].name < current.attrs[j].name })
	return nil
}

// canonicalAttributeName maps a parsed attribute to its canonical name.
// keep=false drops namespace declarations (the output declares only the
// default SVG namespace on the root).
func canonicalAttributeName(element string, attr xml.Attr) (string, bool, error) {
	space, local := attr.Name.Space, attr.Name.Local
	switch {
	case space == "" && local == "xmlns":
		if attr.Value != svgNamespace {
			return "", false, fmt.Errorf("svg: default namespace must be %s", svgNamespace)
		}
		return "", false, nil
	case space == "xmlns":
		if local == "xlink" && attr.Value == xlinkNamespace {
			return "", false, nil
		}
		return "", false, fmt.Errorf("svg: namespace declaration xmlns:%s is not allowed", local)
	case space == xlinkNamespace && local == "href":
		if !hrefElements[element] {
			return "", false, fmt.Errorf("svg: xlink:href is not allowed on <%s>", element)
		}
		return "href", true, nil
	case space == xmlNamespace && local == "space":
		return "xml:space", true, nil
	case space != "":
		return "", false, fmt.Errorf("svg: attribute %s on <%s> is not allowed", qualifiedName(attr.Name), element)
	}
	lower := strings.ToLower(local)
	switch {
	case strings.HasPrefix(lower, "on"):
		return "", false, fmt.Errorf("svg: event handler attribute %s on <%s> is not allowed", local, element)
	case lower == "style":
		return "", false, fmt.Errorf("svg: style attribute on <%s> is not allowed", element)
	case local == "href":
		if !hrefElements[element] {
			return "", false, fmt.Errorf("svg: href is not allowed on <%s>", element)
		}
		return "href", true, nil
	case !allowedAttributes[local]:
		return "", false, fmt.Errorf("svg: attribute %s on <%s> is not allowed", local, element)
	}
	return local, true, nil
}

func checkText(text string) error {
	for _, r := range text {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || (r >= 0x7f && r <= 0x9f) {
			return errors.New("svg: control characters are not allowed")
		}
	}
	return nil
}

func checkValue(element, name, value string, limits Limits) error {
	if len(value) > limits.MaxAttrBytes {
		return fmt.Errorf("svg: attribute %s on <%s> exceeds %d bytes", name, element, limits.MaxAttrBytes)
	}
	if err := checkText(value); err != nil {
		return fmt.Errorf("svg: attribute %s on <%s>: %w", name, element, err)
	}
	if strings.ContainsAny(value, "<>\\&\"'`") {
		return fmt.Errorf("svg: attribute %s on <%s> contains a forbidden character", name, element)
	}
	lower := strings.ToLower(value)
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, lower)
	for _, banned := range bannedValueParts {
		if strings.Contains(lower, banned) || strings.Contains(compact, banned) {
			return fmt.Errorf("svg: attribute %s on <%s> contains %q", name, element, banned)
		}
	}
	for _, match := range functionPattern.FindAllStringSubmatch(lower, -1) {
		if !allowedFunctions[match[1]] {
			return fmt.Errorf("svg: attribute %s on <%s> uses a disallowed function %q", name, element, match[1]+"(")
		}
	}
	for _, location := range urlPattern.FindAllStringIndex(value, -1) {
		if location[1]-location[0] != len("url(") || !urlTargetPattern.MatchString(value[location[1]:]) {
			return fmt.Errorf("svg: attribute %s on <%s> may only use url(#id)", name, element)
		}
	}
	switch name {
	case "id":
		if !idPattern.MatchString(value) {
			return fmt.Errorf("svg: id %q must match %s", value, idPattern)
		}
	case "href":
		if !fragmentPattern.MatchString(value) {
			return fmt.Errorf("svg: href on <%s> must be a local #id reference", element)
		}
	case "xml:space":
		if value != "default" && value != "preserve" {
			return errors.New("svg: xml:space must be default or preserve")
		}
	case "viewBox":
		if _, err := parseViewBox(value); err != nil {
			return err
		}
	}
	return nil
}

func parseViewBox(value string) ([4]float64, error) {
	var box [4]float64
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(fields) != 4 {
		return box, errors.New("svg: viewBox must have four numbers")
	}
	for index, field := range fields {
		if !numberPattern.MatchString(field) {
			return box, fmt.Errorf("svg: viewBox value %q is not a number", field)
		}
		parsed, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Abs(parsed) > maxViewBoxMagnitude {
			return box, fmt.Errorf("svg: viewBox value %q is out of range", field)
		}
		box[index] = parsed
	}
	if box[2] <= 0 || box[3] <= 0 {
		return box, errors.New("svg: viewBox width and height must be positive")
	}
	return box, nil
}

func checkRoot(root *node, limits Limits) error {
	hasViewBox := false
	for _, attr := range root.attrs {
		switch attr.name {
		case "viewBox":
			hasViewBox = true
		case "width", "height":
			if !dimensionPattern.MatchString(attr.value) {
				return fmt.Errorf("svg: root %s must be a number (optionally px)", attr.name)
			}
			parsed, err := strconv.ParseFloat(strings.TrimSuffix(attr.value, "px"), 64)
			if err != nil || parsed <= 0 || parsed > float64(limits.MaxDimension) {
				return fmt.Errorf("svg: root %s must be greater than 0 and at most %d", attr.name, limits.MaxDimension)
			}
		}
	}
	if !hasViewBox {
		return errors.New("svg: root <svg> needs a viewBox")
	}
	return nil
}

// checkReferences requires unique ids, <use> targets that exist, and no
// <use> expansion of a subtree that itself contains <use> (which bounds the
// rendered element count to MaxUse × MaxElements and rules out cycles).
func checkReferences(root *node) error {
	ids := map[string]*node{}
	var uses []*node
	var walk func(current *node) (bool, error)
	walk = func(current *node) (bool, error) {
		if current.id != "" {
			if _, duplicate := ids[current.id]; duplicate {
				return false, fmt.Errorf("svg: duplicate id %q", current.id)
			}
			ids[current.id] = current
		}
		hasUse := current.name == "use"
		if hasUse {
			uses = append(uses, current)
		}
		for _, child := range current.children {
			childHasUse, err := walk(child)
			if err != nil {
				return false, err
			}
			hasUse = hasUse || childHasUse
		}
		current.hasUse = hasUse
		return hasUse, nil
	}
	if _, err := walk(root); err != nil {
		return err
	}
	for _, use := range uses {
		target, ok := ids[strings.TrimPrefix(use.href, "#")]
		if !ok {
			return fmt.Errorf("svg: <use> references unknown id %q", use.href)
		}
		if target.hasUse {
			return fmt.Errorf("svg: <use> may not reference %q, which contains <use>", use.href)
		}
	}
	return nil
}

func serialize(root *node) []byte {
	var buffer bytes.Buffer
	writeNode(&buffer, root, true)
	return buffer.Bytes()
}

func writeNode(buffer *bytes.Buffer, current *node, isRoot bool) {
	buffer.WriteByte('<')
	buffer.WriteString(current.name)
	if isRoot {
		buffer.WriteString(` xmlns="` + svgNamespace + `"`)
	}
	for _, attr := range current.attrs {
		buffer.WriteByte(' ')
		buffer.WriteString(attr.name)
		buffer.WriteString(`="`)
		writeEscaped(buffer, attr.value, true)
		buffer.WriteByte('"')
	}
	text := current.text.String()
	if len(current.children) == 0 && text == "" {
		buffer.WriteString("/>")
		return
	}
	buffer.WriteByte('>')
	writeEscaped(buffer, text, false)
	for _, child := range current.children {
		writeNode(buffer, child, false)
	}
	buffer.WriteString("</")
	buffer.WriteString(current.name)
	buffer.WriteByte('>')
}

func writeEscaped(buffer *bytes.Buffer, value string, attribute bool) {
	for _, r := range value {
		switch {
		case r == '&':
			buffer.WriteString("&amp;")
		case r == '<':
			buffer.WriteString("&lt;")
		case r == '>':
			buffer.WriteString("&gt;")
		case r == '"' && attribute:
			buffer.WriteString("&quot;")
		case r == '\r':
			buffer.WriteString("&#13;")
		case (r == '\t' || r == '\n') && attribute:
			fmt.Fprintf(buffer, "&#%d;", r)
		default:
			buffer.WriteRune(r)
		}
	}
}
