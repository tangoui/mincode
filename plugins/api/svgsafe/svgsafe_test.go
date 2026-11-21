package svgsafe

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

var testLimits = Limits{
	MaxBytes:     32768,
	MaxElements:  2048,
	MaxDepth:     32,
	MaxAttrBytes: 16384,
	MaxUse:       64,
	MaxDimension: 1024,
}

func wrap(body string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` + body + `</svg>`
}

func TestSanitizeAcceptsAndCanonicalizes(t *testing.T) {
	input := "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<!-- icon -->
<svg width="16" viewBox="0 0 16 16" height="16px" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
  <title>File &amp; folder</title>
  <defs>
    <linearGradient id="g1" x1="0" y1="0" x2="1" y2="1" gradientTransform="rotate(45)">
      <stop offset="0" stop-color="#fff"/>
      <stop offset="1" stop-color="rgb(0, 0, 0)" stop-opacity=".5"/>
    </linearGradient>
    <symbol id="dot" viewBox="0 0 2 2"><circle cx="1" cy="1" r="1"/></symbol>
    <clipPath id="clip"><rect width="16" height="16" rx="2"/></clipPath>
  </defs>
  <g clip-path="url(#clip)" fill="url(#g1)" transform="translate(1 1) scale(0.9)">
    <path d="M2 2h12v12H2z" fill-rule="evenodd"/>
    <use xlink:href="#dot" x="4" y="4"/>
  </g>
</svg>`
	got, err := Sanitize([]byte(input), testLimits)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	want := `<svg xmlns="http://www.w3.org/2000/svg" height="16px" viewBox="0 0 16 16" width="16">` +
		`<title>File &amp; folder</title><defs>` +
		`<linearGradient gradientTransform="rotate(45)" id="g1" x1="0" x2="1" y1="0" y2="1">` +
		`<stop offset="0" stop-color="#fff"/><stop offset="1" stop-color="rgb(0, 0, 0)" stop-opacity=".5"/>` +
		`</linearGradient><symbol id="dot" viewBox="0 0 2 2"><circle cx="1" cy="1" r="1"/></symbol>` +
		`<clipPath id="clip"><rect height="16" rx="2" width="16"/></clipPath></defs>` +
		`<g clip-path="url(#clip)" fill="url(#g1)" transform="translate(1 1) scale(0.9)">` +
		`<path d="M2 2h12v12H2z" fill-rule="evenodd"/><use href="#dot" x="4" y="4"/></g></svg>`
	if string(got) != want {
		t.Fatalf("Sanitize() =\n%s\nwant\n%s", got, want)
	}
	again, err := Sanitize(got, testLimits)
	if err != nil || !bytes.Equal(again, got) {
		t.Fatalf("canonical form is not a fixed point: %v\n%s", err, again)
	}
}

func TestSanitizeEscapesAttributeWhitespace(t *testing.T) {
	got, err := Sanitize([]byte(wrap("<path d=\"M0 0\n\tL1 1\"/>")), testLimits)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if !strings.Contains(string(got), `d="M0 0&#10;&#9;L1 1"`) {
		t.Fatalf("attribute whitespace not escaped: %s", got)
	}
}

// TestSanitizeRejectsXSSCorpus covers script execution, external fetches,
// parser tricks and resource exhaustion vectors. Every entry must fail.
func TestSanitizeRejectsXSSCorpus(t *testing.T) {
	corpus := map[string]string{
		"script element":            wrap(`<script>alert(1)</script>`),
		"script namespaced":         `<svg xmlns="http://www.w3.org/2000/svg" xmlns:h="http://www.w3.org/1999/xhtml" viewBox="0 0 1 1"><h:script>alert(1)</h:script></svg>`,
		"script upper case":         wrap(`<SCRIPT>alert(1)</SCRIPT>`),
		"script cdata":              wrap(`<script><![CDATA[alert(1)]]></script>`),
		"cdata in title":            wrap(`<title><![CDATA[x]]></title>`),
		"foreignObject":             wrap(`<foreignObject><body xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(1)"/></body></foreignObject>`),
		"style element":             wrap(`<style>@import url(https://evil.example/x.css);</style>`),
		"style attribute":           wrap(`<rect width="1" height="1" style="fill:red"/>`),
		"onload root":               `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1" onload="alert(1)"/>`,
		"onclick":                   wrap(`<rect width="1" height="1" onclick="alert(1)"/>`),
		"ONMOUSEOVER":               wrap(`<rect width="1" height="1" ONMOUSEOVER="alert(1)"/>`),
		"onbegin":                   wrap(`<g onbegin="alert(1)"/>`),
		"animate href":              wrap(`<animate attributeName="href" to="javascript:alert(1)"/>`),
		"set element":               wrap(`<set attributeName="onmouseover" to="alert(1)"/>`),
		"animateTransform":          wrap(`<animateTransform attributeName="transform" type="rotate"/>`),
		"anchor":                    wrap(`<a href="javascript:alert(1)"><rect width="1" height="1"/></a>`),
		"image external":            wrap(`<image href="https://evil.example/x.png"/>`),
		"feImage":                   wrap(`<filter id="f"><feImage href="https://evil.example/x"/></filter>`),
		"text element":              wrap(`<text>hi</text>`),
		"iframe":                    wrap(`<iframe src="https://evil.example"/>`),
		"use external":              wrap(`<use href="https://evil.example/sprite.svg#a"/>`),
		"use external xlink":        `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 1 1"><use xlink:href="sprite.svg#a"/></svg>`,
		"use data uri":              wrap(`<use href="data:image/svg+xml;base64,PHN2Zz4="/>`),
		"use javascript":            wrap(`<use href="javascript:alert(1)"/>`),
		"use missing target":        wrap(`<use href="#nope"/>`),
		"use without href":          wrap(`<use x="1"/>`),
		"href on path":              wrap(`<path id="a" href="#a" d="M0 0"/>`),
		"gradient external href":    wrap(`<linearGradient id="g" href="other.svg#g"/>`),
		"fill external url":         wrap(`<rect width="1" height="1" fill="url(https://evil.example/#g)"/>`),
		"fill url quoted":           wrap(`<rect width="1" height="1" fill="url('#g')"/>`),
		"fill url spaced":           wrap(`<rect width="1" height="1" fill="url( #g)"/>`),
		"fill url data":             wrap(`<rect width="1" height="1" fill="url(data:x)"/>`),
		"mask url relative":         wrap(`<rect width="1" height="1" mask="url(m.svg#m)"/>`),
		"expression":                wrap(`<rect width="1" height="1" fill="expression(alert(1))"/>`),
		"import":                    wrap(`<rect width="1" height="1" fill="@import x"/>`),
		"css comment":               wrap(`<rect width="1" height="1" fill="red/**/"/>`),
		"backslash escape":          wrap(`<rect width="1" height="1" fill="\75rl(x)"/>`),
		"entity lt in attr":         wrap(`<rect width="1" height="1" fill="&lt;script&gt;"/>`),
		"char ref javascript":       wrap(`<use href="&#106;avascript:alert(1)"/>`),
		"disallowed function":       wrap(`<rect width="1" height="1" fill="var(--x)"/>`),
		"calc function":             wrap(`<rect width="calc(1px)" height="1"/>`),
		"doctype":                   `<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`,
		"xxe":                       `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><title>&xxe;</title></svg>`,
		"billion laughs":            `<!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><title>&lol2;</title></svg>`,
		"undeclared entity":         wrap(`<title>&nbsp;</title>`),
		"stylesheet PI":             `<?xml-stylesheet href="https://evil.example/x.css"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`,
		"PI inside":                 wrap(`<?php echo 1; ?>`),
		"non utf8 encoding":         `<?xml version="1.0" encoding="UTF-7"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`,
		"invalid utf8":              wrap("<title>\xff</title>"),
		"control char":              wrap("<title>\x01</title>"),
		"no namespace":              `<svg viewBox="0 0 1 1"/>`,
		"wrong namespace":           `<svg xmlns="http://www.w3.org/1999/xhtml" viewBox="0 0 1 1"/>`,
		"html root":                 `<html xmlns="http://www.w3.org/2000/svg"/>`,
		"extra namespace decl":      `<svg xmlns="http://www.w3.org/2000/svg" xmlns:ev="http://www.w3.org/2001/xml-events" viewBox="0 0 1 1"/>`,
		"foreign attribute":         `<svg xmlns="http://www.w3.org/2000/svg" xmlns:x="urn:x" viewBox="0 0 1 1" x:y="1"/>`,
		"unknown attribute":         wrap(`<rect width="1" height="1" formaction="x"/>`),
		"missing viewBox":           `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"/>`,
		"bad viewBox":               `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 0 16"/>`,
		"huge viewBox":              `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1e9 16"/>`,
		"huge width":                `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="100000"/>`,
		"percent width":             `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="100%"/>`,
		"text in g":                 wrap(`<g>hello</g>`),
		"text outside root":         wrap(``) + `trailing`,
		"two roots":                 wrap(``) + wrap(``),
		"element in title":          wrap(`<title><rect/></title>`),
		"duplicate attribute alias": `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 1 1"><rect id="a"/><use href="#a" xlink:href="#a"/></svg>`,
		"duplicate id":              wrap(`<rect id="a"/><rect id="a"/>`),
		"bad id":                    wrap(`<rect id="a b"/>`),
		"nested use amplification":  wrap(`<g id="a"><rect/></g><g id="b"><use href="#a"/></g><use href="#b"/>`),
		"use self cycle":            wrap(`<g id="a"><use href="#a"/></g>`),
		"unterminated":              `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><g>`,
		"empty":                     ``,
		"not xml":                   `hello`,
	}
	for name, input := range corpus {
		t.Run(name, func(t *testing.T) {
			if got, err := Sanitize([]byte(input), testLimits); err == nil {
				t.Fatalf("Sanitize(%q) accepted:\n%s", input, got)
			}
		})
	}
}

func TestSanitizeEnforcesLimits(t *testing.T) {
	tight := testLimits
	tight.MaxBytes = 128
	if _, err := Sanitize([]byte(wrap(strings.Repeat(" ", 200))), tight); err == nil {
		t.Fatal("MaxBytes not enforced")
	}

	tight = testLimits
	tight.MaxElements = 3
	if _, err := Sanitize([]byte(wrap(`<g/><g/><g/>`)), tight); err == nil {
		t.Fatal("MaxElements not enforced")
	}
	if _, err := Sanitize([]byte(wrap(`<g/><g/>`)), tight); err != nil {
		t.Fatalf("MaxElements rejected a document at the limit: %v", err)
	}

	tight = testLimits
	tight.MaxDepth = 3
	if _, err := Sanitize([]byte(wrap(`<g><g><g/></g></g>`)), tight); err == nil {
		t.Fatal("MaxDepth not enforced")
	}

	tight = testLimits
	tight.MaxUse = 2
	uses := `<rect id="a"/>` + strings.Repeat(`<use href="#a"/>`, 3)
	if _, err := Sanitize([]byte(wrap(uses)), tight); err == nil {
		t.Fatal("MaxUse not enforced")
	}

	tight = testLimits
	tight.MaxAttrBytes = 16
	if _, err := Sanitize([]byte(wrap(`<path d="`+strings.Repeat("M0 0", 10)+`"/>`)), tight); err == nil {
		t.Fatal("MaxAttrBytes not enforced")
	}
	if _, err := Sanitize([]byte(wrap(`<title>`+strings.Repeat("x", 20)+`</title>`)), tight); err == nil {
		t.Fatal("MaxAttrBytes not enforced for title text")
	}

	tight = testLimits
	tight.MaxDimension = 64
	if _, err := Sanitize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1" height="65"/>`), tight); err == nil {
		t.Fatal("MaxDimension not enforced")
	}

	if _, err := Sanitize([]byte(wrap(``)), Limits{}); err == nil {
		t.Fatal("zero limits accepted")
	}
}

func TestSanitizeDeepNestingDoesNotRecurseUnbounded(t *testing.T) {
	deep := strings.Repeat("<g>", 10000) + strings.Repeat("</g>", 10000)
	limits := testLimits
	limits.MaxBytes = 1 << 20
	if _, err := Sanitize([]byte(wrap(deep)), limits); err == nil {
		t.Fatal("deep nesting accepted")
	}
}

func TestSanitizeManyUsesOfFlatTarget(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<defs><path id="p" d="M0 0h1v1z"/></defs>`)
	for index := 0; index < 64; index++ {
		fmt.Fprintf(&body, `<use href="#p" x="%d"/>`, index)
	}
	if _, err := Sanitize([]byte(wrap(body.String())), testLimits); err != nil {
		t.Fatalf("flat <use> fan-out rejected: %v", err)
	}
}
