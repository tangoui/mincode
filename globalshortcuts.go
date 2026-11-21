package main

import (
	"errors"
	"strings"
	"sync"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/sys/hotkey"
)

type globalShortcuts struct {
	app    *tango.App
	summon func()

	mu          sync.Mutex
	key         *hotkey.Hotkey
	accelerator string
}

func newGlobalShortcuts(app *tango.App, summon func()) *globalShortcuts {
	return &globalShortcuts{app: app, summon: summon}
}

func (g *globalShortcuts) setHost(enabled bool, accelerator string) error {
	accelerator = strings.TrimSpace(accelerator)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.key != nil {
		if enabled && g.accelerator == accelerator {
			return nil
		}
		if err := g.key.Unregister(); err != nil && !errors.Is(err, hotkey.ErrUnregistered) {
			return err
		}
		g.key = nil
		g.accelerator = ""
	}
	if !enabled || accelerator == "" {
		return nil
	}
	key, err := hotkey.Register(g.app, accelerator, func() { go g.summon() })
	if err != nil {
		return err
	}
	g.key = key
	g.accelerator = accelerator
	return nil
}
