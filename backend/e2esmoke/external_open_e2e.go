//go:build e2e

package e2esmoke

import (
	"errors"
	"os"

	"mincode/app/backend/core"
	"mincode/app/backend/plugin"
)

// Only disposable desktop smoke builds can inject native link input. Production
// builds do not register these commands; they never change system associations.
func init() {
	core.Register("e2e_smoke_external_link", func(_, raw string) (string, error) {
		if !Enabled() {
			return "", errors.New("e2e smoke harness is not enabled")
		}
		var args struct {
			URL string `json:"url"`
		}
		if err := core.DecodeArgs(raw, &args); err != nil {
			return "", err
		}
		return core.EncodeResult(plugin.EnqueueExternalURI(args.URL))
	})
	core.Register("e2e_smoke_external_ready", func(_, _ string) (string, error) {
		if !Enabled() {
			return "", errors.New("e2e smoke harness is not enabled")
		}
		if err := writeAtomic(os.Getenv(reportEnv)+".ready", []byte(`{"ready":true}`)); err != nil {
			return "", err
		}
		return core.EncodeResult(nil)
	})
}
