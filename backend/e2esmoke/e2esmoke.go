// Package e2esmoke lets the desktop smoke harness (tools/e2e/desktop-smoke.mjs)
// drive a real desktop build. Only binaries built with `-tags e2e` honour the
// MINCODE_E2E_CONFIG / MINCODE_E2E_REPORT environment; release builds always
// report the harness as disabled, so the commands exist but do nothing.
package e2esmoke

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	configEnv = "MINCODE_E2E_CONFIG"
	reportEnv = "MINCODE_E2E_REPORT"
	// maxDocumentBytes bounds both the scenario config and the report.
	maxDocumentBytes = 1 << 20
	// quitDelay lets the report call return to the WebView before teardown.
	quitDelay = 300 * time.Millisecond
)

var (
	quitMu   sync.Mutex
	quitFunc func()
	reported sync.Once
)

func init() {
	core.Register("e2e_smoke_config", configHandler)
	core.Register("e2e_smoke_report", reportHandler)
}

// Enabled reports whether this process runs under the smoke harness.
func Enabled() bool {
	return buildEnabled && os.Getenv(configEnv) != "" && os.Getenv(reportEnv) != ""
}

// IsolatedInstanceID exercises the real second-launch channel without sharing
// the production single-instance address. Release builds always return empty.
func IsolatedInstanceID() string {
	if !Enabled() {
		return ""
	}
	digest := sha256.Sum256([]byte(os.Getenv(configEnv)))
	return "com.mincode.e2e." + hex.EncodeToString(digest[:16])
}

// SetQuitHandler installs the application quit used after the report is written.
func SetQuitHandler(quit func()) {
	quitMu.Lock()
	quitFunc = quit
	quitMu.Unlock()
}

type configResult struct {
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config,omitempty"`
}

func configHandler(_, _ string) (string, error) {
	if !Enabled() {
		return core.EncodeResult(configResult{})
	}
	raw, err := readBounded(os.Getenv(configEnv))
	if err != nil {
		return "", fmt.Errorf("read e2e smoke config: %w", err)
	}
	if !json.Valid(raw) {
		return "", errors.New("e2e smoke config is not valid JSON")
	}
	return core.EncodeResult(configResult{Enabled: true, Config: raw})
}

type reportArgs struct {
	Report json.RawMessage `json:"report"`
}

func reportHandler(_, argsJSON string) (string, error) {
	if !Enabled() {
		return "", errors.New("e2e smoke harness is not enabled")
	}
	var args reportArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if len(args.Report) == 0 || len(args.Report) > maxDocumentBytes || !json.Valid(args.Report) {
		return "", errors.New("invalid e2e smoke report")
	}
	if err := writeAtomic(os.Getenv(reportEnv), args.Report); err != nil {
		return "", fmt.Errorf("write e2e smoke report: %w", err)
	}
	reported.Do(func() {
		quitMu.Lock()
		quit := quitFunc
		quitMu.Unlock()
		if quit != nil {
			time.AfterFunc(quitDelay, quit)
		}
	})
	return core.EncodeResult(nil)
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDocumentBytes {
		return nil, errors.New("document exceeds 1 MiB")
	}
	return raw, nil
}

func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".e2e-report-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
