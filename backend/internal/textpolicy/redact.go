package textpolicy

import (
	"net/url"
	"regexp"
)

const credentialNames = `password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|passphrase|credential|authorization|cookie|session[_-]?id`

var (
	credentialAssignment = regexp.MustCompile(`(?i)((?:` + credentialNames + `)["']?\s*[=:]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,'";}]+)`)
	credentialName       = regexp.MustCompile(`(?i)(^|[^a-z0-9])(?:` + credentialNames + `)($|[^a-z0-9])`)
	authorizationHeader  = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	privateKeyBlock      = regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?(?:-----END [^-\r\n]*PRIVATE KEY-----|\z)`)
	credentialURL        = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
)

// RedactCredentials masks credential assignments, authorization headers, URL
// credentials and PEM private keys in text that will leave the desktop.
func RedactCredentials(text string) string {
	text = privateKeyBlock.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = authorizationHeader.ReplaceAllString(text, "${1} [REDACTED]")
	text = credentialURL.ReplaceAllStringFunc(text, redactURL)
	return credentialAssignment.ReplaceAllString(text, "${1}[REDACTED]")
}

func redactURL(candidate string) string {
	u, err := url.Parse(candidate)
	if err != nil {
		return candidate
	}
	changed := false
	if u.User != nil {
		u.User = url.User("[REDACTED]")
		changed = true
	}
	query := u.Query()
	for key := range query {
		if credentialName.MatchString(key) {
			query.Set(key, "[REDACTED]")
			changed = true
		}
	}
	if !changed {
		return candidate
	}
	u.RawQuery = query.Encode()
	return u.String()
}
