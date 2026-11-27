package agentcontrol

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"mincode/app/backend/externalmcp"
)

func randomID(size int) string {
	value := make([]byte, size)
	// crypto/rand.Read always fills value and terminates on system failure.
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}

func IssuePaneToken(paneID string) (url, token string) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.server == nil || paneID == "" {
		return "", ""
	}
	token = randomID(32)
	control.tokenPanes[token] = paneID
	return control.url, token
}

func RevokePane(paneID string) {
	control.mu.Lock()
	defer control.mu.Unlock()
	for token, owner := range control.tokenPanes {
		if owner == paneID {
			delete(control.tokenPanes, token)
		}
	}
	for _, call := range control.pending {
		if call.source.Kind == "pane" && call.source.PaneID == paneID {
			call.cancel()
		}
	}
}

// RevokePaneToken retires the credentials for one shell process.
func RevokePaneToken(paneID, token string) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if token != "" && control.tokenPanes[token] == paneID {
		delete(control.tokenPanes, token)
		for _, call := range control.pending {
			if call.source.token == token {
				call.cancel()
			}
		}
	}
}

func SetExternalTrusted(enabled bool) error {
	control.lifecycle.Lock()
	defer control.lifecycle.Unlock()
	control.mu.Lock()
	defer control.mu.Unlock()

	if control.server == nil {
		return errors.New("MinCode control service is unavailable")
	}
	if enabled == control.trusted {
		return nil
	}
	if !enabled {
		control.trusted = false
		for _, call := range control.pending {
			if call.source.Kind == "external" {
				call.cancel()
			}
		}
		return externalmcp.SetControlRecord("", "")
	}
	token := randomID(32)
	if err := externalmcp.SetControlRecord(control.url, token); err != nil {
		return err
	}
	control.trusted = true
	control.externalToken = token
	return nil
}

type origin struct {
	Kind   string `json:"kind"`
	PaneID string `json:"paneId,omitempty"`
	CWD    string `json:"cwd,omitempty"`
	token  string
}

func (s *serverState) authenticate(header string) (origin, int) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(token) != 64 {
		return origin{}, http.StatusUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authenticateLocked(token)
}

type authError int

func (e authError) Error() string {
	if e == http.StatusForbidden {
		return "external agent control is disabled"
	}
	return "invalid MinCode control token"
}

func (s *serverState) authenticateLocked(token string) (origin, int) {
	if id := s.tokenPanes[token]; id != "" {
		return origin{Kind: "pane", PaneID: id, token: token}, 0
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.externalToken)) == 1 {
		if !s.trusted {
			return origin{}, http.StatusForbidden
		}
		return origin{Kind: "external", token: token}, 0
	}
	return origin{}, http.StatusUnauthorized
}
