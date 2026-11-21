package terminalactivity

import (
	"context"
	"os"
)

func Startup(_ context.Context) error {
	return nil
}

func Shutdown(_ context.Context) {
	state.mu.Lock()
	if !state.initialized {
		state.mu.Unlock()
		return
	}
	stop, done, running, inbox := state.stop, state.done, state.pollerRunning, state.inboxDir
	state.initialized = false
	state.pollerRunning = false
	state.registrations = make(map[string]registration)
	state.mu.Unlock()
	if running {
		close(stop)
		<-done
	}
	_ = os.RemoveAll(inbox)
}
