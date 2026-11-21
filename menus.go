package main

import (
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"mincode/app/backend/applock"
	"mincode/app/backend/desktop"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/sys/menu"
	"github.com/tangoui/tango/sys/tray"
)

// tango 的菜单在挂上去之后由 sys/menu 内部的锁保护，不能从外面改字段；菜单内容变化时
// （菜单快捷键开关、插件托盘项）都重新生成一份再整体替换。替换会等主线程，所以只从
// 后台 goroutine 调用——desktop.Adapter 的回调来自后端，符合这一点。

// appMenu 是应用菜单。插件页面接管键盘时，MinCode 自己的自定义菜单快捷键（目前是
// “设置…”的 CmdOrCtrl+,）要暂时关掉，把按键让给插件页面（desktop.Adapter.SetMenuAccelerators）；
// 编辑、退出等系统角色项由系统管理，不受影响。
type appMenu struct {
	app      *tango.App
	showMain func()
	navigate func(kind, value string)

	mu           sync.Mutex
	accelerators bool // 自定义菜单快捷键是否开启；初始 true
	installed    bool
}

func newAppMenu(app *tango.App, showMain func(), navigate func(kind, value string)) *appMenu {
	return &appMenu{app: app, showMain: showMain, navigate: navigate, accelerators: true}
}

// install 第一次设置应用菜单（OnReady 里调用）。
func (m *appMenu) install() {
	m.mu.Lock()
	m.installed = true
	enabled := m.accelerators
	m.mu.Unlock()
	m.apply(enabled)
}

// setAccelerators 是 desktop.Adapter.SetMenuAccelerators。
func (m *appMenu) setAccelerators(enabled bool) {
	m.mu.Lock()
	changed := m.accelerators != enabled
	m.accelerators = enabled
	installed := m.installed
	m.mu.Unlock()
	if changed && installed {
		m.apply(enabled)
	}
}

func (m *appMenu) apply(accelerators bool) {
	if err := menu.SetAppMenu(m.app, m.build(accelerators)); err != nil {
		log.Printf("app menu unavailable: %v", err)
	}
}

func (m *appMenu) build(accelerators bool) *menu.Menu {
	shortcut := func(accelerator string) string {
		if accelerators {
			return accelerator
		}
		return ""
	}
	root := menu.New()
	app := root.Submenu("MinCode")
	app.Role(menu.About)
	app.Add("设置…", func() { m.navigate("view", "settings") }).Accelerator = shortcut("CmdOrCtrl+,")
	app.Add("检查更新…", func() { m.navigate("view", "settings") })
	app.Separator()
	app.Role(menu.Hide)
	app.Separator()
	app.Role(menu.Quit)
	edit := root.Submenu("编辑")
	edit.Role(menu.Undo)
	edit.Role(menu.Redo)
	edit.Separator()
	edit.Role(menu.Cut)
	edit.Role(menu.Copy)
	edit.Role(menu.Paste)
	edit.Role(menu.SelectAll)
	window := root.Submenu("窗口")
	window.Add("显示主窗口", m.showMain)
	window.Role(menu.Minimize)
	return root
}

// trayMenu 是系统托盘：MinCode 自己的显示/隐藏/退出，加上插件贡献的托盘菜单
// （contributes.menus.tray，后端解析成分组后经 desktop.Adapter.SetPluginTray 交来），
// 每个插件一个以插件名命名的子菜单。
type trayMenu struct {
	app            *tango.App
	show, hide, qt func()

	mu      sync.Mutex
	icon    *tray.Tray
	groups  []desktop.PluginTrayGroup // 最近一次的插件分组
	buildMu sync.Mutex                // 串行化重建
}

func newTrayMenu(app *tango.App, show, hide, quit func()) *trayMenu {
	return &trayMenu{app: app, show: show, hide: hide, qt: quit}
}

// install 创建托盘图标并挂上菜单（OnReady 里调用）；之前已到达的插件分组一并显示。
func (t *trayMenu) install() {
	icon, err := tray.New(t.app, tray.Options{
		Icon:     trayIconLight,
		MacIcon:  trayIconTemplate,
		Template: runtime.GOOS == "darwin",
		Tooltip:  "MinCode",
	})
	if err != nil {
		log.Printf("system tray unavailable: %v", err)
		return
	}
	icon.OnClick(t.show)
	t.mu.Lock()
	t.icon = icon
	t.mu.Unlock()
	go t.rebuild()
}

// setPluginGroups 是 desktop.Adapter.SetPluginTray：替换插件部分；与上次相同时不重建。
func (t *trayMenu) setPluginGroups(groups []desktop.PluginTrayGroup) {
	if len(groups) == 0 {
		groups = nil
	}
	t.mu.Lock()
	if reflect.DeepEqual(groups, t.groups) {
		t.mu.Unlock()
		return
	}
	t.groups = cloneTrayGroups(groups)
	t.mu.Unlock()
	t.rebuild()
}

// rebuild 按最新的插件分组重建菜单并挂到托盘；托盘还没创建时等 install 再建。
func (t *trayMenu) rebuild() {
	t.buildMu.Lock()
	defer t.buildMu.Unlock()
	t.mu.Lock()
	icon, groups := t.icon, t.groups
	t.mu.Unlock()
	if icon == nil {
		return
	}
	if err := icon.SetMenu(buildTrayMenu(t.show, t.hide, t.qt, groups, runtime.GOOS, trayPluginClicked)); err != nil {
		log.Printf("tray menu unavailable: %v", err)
	}
}

// trayPluginClicked 执行插件托盘项：应用锁定时什么也不做（锁定前打开的菜单也一样），
// 否则交给前端按插件命令执行（desktop:navigate 的 plugin-command 目标）。
func trayPluginClicked(pluginID, commandID string) {
	if applock.Locked() {
		return
	}
	_ = desktop.Navigate(desktop.PluginCommandTarget(pluginID, commandID))
}

// buildTrayMenu 先放 MinCode 自己的项；有插件分组时加一条分隔线，每个插件一个子菜单，
// 只含分隔线的分组跳过。每个插件项点击时带着建菜单时的 (pluginID, commandID)。
func buildTrayMenu(show, hide, quit func(), groups []desktop.PluginTrayGroup, goos string, onPlugin func(pluginID, commandID string)) *menu.Menu {
	m := menu.New()
	m.Add("显示主窗口", show)
	m.Add("隐藏主窗口", hide)
	m.Separator()
	m.Add("退出", quit)
	separated := false
	for _, group := range groups {
		if !trayGroupHasCommand(group) {
			continue
		}
		if !separated {
			m.Separator()
			separated = true
		}
		sub := m.Submenu(trayLabel(group.Name, goos))
		for _, item := range group.Items {
			if item.Separator {
				sub.Separator()
				continue
			}
			pluginID, commandID := group.PluginID, item.CommandID
			sub.Add(trayLabel(item.Title, goos), func() { go onPlugin(pluginID, commandID) })
		}
	}
	return m
}

func trayGroupHasCommand(group desktop.PluginTrayGroup) bool {
	for _, item := range group.Items {
		if !item.Separator {
			return true
		}
	}
	return false
}

// trayLabel 转义平台菜单的助记符：Windows 的 &、Linux 的 _ 会被当作加速字母。
func trayLabel(text, goos string) string {
	switch goos {
	case "windows":
		return strings.ReplaceAll(text, "&", "&&")
	case "linux":
		return strings.ReplaceAll(text, "_", "__")
	}
	return text
}

func cloneTrayGroups(groups []desktop.PluginTrayGroup) []desktop.PluginTrayGroup {
	if groups == nil {
		return nil
	}
	cloned := make([]desktop.PluginTrayGroup, len(groups))
	for i, group := range groups {
		cloned[i] = group
		cloned[i].Items = append([]desktop.PluginTrayItem(nil), group.Items...)
	}
	return cloned
}
