package main

import (
	"errors"
	"strings"
	"sync"

	"mincode/app/backend/desktop"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/sys/hotkey"
)

// globalShortcuts 是 MinCode 自己的唤起快捷键和插件全局快捷键共用的系统快捷键管理器
// （desktop.Adapter 的 SetGlobalShortcut / RegisterPluginShortcut / UnregisterPluginShortcut
// / HostShortcut）。底层是 tango 的 hotkey：macOS 用 Carbon 热键（不需要辅助功能权限），
// Windows 用 RegisterHotKey，Linux X11 用 XGrabKey；Wayland 不支持，返回不可用。
type globalShortcuts struct {
	app    *tango.App
	summon func() // 唤起快捷键：把主窗口调到前台

	mu   sync.Mutex
	keys map[string]*hotkey.Hotkey // 已注册的组合键（含 MinCode 自己的）
	host string                    // MinCode 自己注册的组合键，未开启为空
}

func newGlobalShortcuts(app *tango.App, summon func()) *globalShortcuts {
	return &globalShortcuts{app: app, summon: summon, keys: map[string]*hotkey.Hotkey{}}
}

// register 注册一个全局组合键；系统拒绝（被别的应用占用、组合键不合法、平台不支持）时返回错误。
// fn 在后台 goroutine 里执行，不占用主线程。
func (g *globalShortcuts) register(accelerator string, fn func()) error {
	accelerator = strings.TrimSpace(accelerator)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, held := g.keys[accelerator]; held {
		return &desktop.GlobalShortcutOccupiedError{Accelerator: accelerator}
	}
	hk, err := hotkey.Register(g.app, accelerator, func() { go fn() })
	if err != nil {
		return err
	}
	g.keys[accelerator] = hk
	return nil
}

// unregister 注销一个组合键；没注册过时什么也不做。
func (g *globalShortcuts) unregister(accelerator string) error {
	accelerator = strings.TrimSpace(accelerator)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.unregisterLocked(accelerator)
}

func (g *globalShortcuts) unregisterLocked(accelerator string) error {
	hk, held := g.keys[accelerator]
	if !held {
		return nil
	}
	delete(g.keys, accelerator)
	if g.host == accelerator {
		g.host = ""
	}
	if err := hk.Unregister(); err != nil && !errors.Is(err, hotkey.ErrUnregistered) {
		return err
	}
	return nil
}

// hostShortcut 是 MinCode 自己注册的唤起快捷键，未开启时为空。
func (g *globalShortcuts) hostShortcut() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.host
}

// setHost 开关或更换 MinCode 自己的唤起快捷键。换组合键时先释放旧的；新组合键已被插件快捷键
// 占着时报告占用（后端在调用前会先让插件释放同一组合键，仍被占着说明释放失败），
// 而不是声称注册了一个按下去没反应的快捷键。
func (g *globalShortcuts) setHost(enabled bool, accelerator string) error {
	accelerator = strings.TrimSpace(accelerator)
	g.mu.Lock()
	previous := g.host
	if previous != "" && (!enabled || previous != accelerator) {
		if err := g.unregisterLocked(previous); err != nil {
			g.mu.Unlock()
			return err
		}
	}
	if !enabled || accelerator == "" {
		g.mu.Unlock()
		return nil
	}
	if _, held := g.keys[accelerator]; held {
		mine := g.host == accelerator
		g.mu.Unlock()
		if mine {
			return nil
		}
		return &desktop.GlobalShortcutOccupiedError{Accelerator: accelerator}
	}
	g.mu.Unlock()
	if err := g.register(accelerator, g.summon); err != nil {
		return err
	}
	g.mu.Lock()
	g.host = accelerator
	g.mu.Unlock()
	return nil
}
