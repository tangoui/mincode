package main

import (
	"log"
	"runtime"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/sys/menu"
	"github.com/tangoui/tango/sys/tray"
)

// tango 的菜单在挂上去之后由 sys/menu 内部的锁保护，不能从外面改字段；菜单内容变化时
// （菜单快捷键开关、插件托盘项）都重新生成一份再整体替换。替换会等主线程，所以只从
// 后台 goroutine 调用——desktop.Adapter 的回调来自后端，符合这一点。

type appMenu struct {
	app      *tango.App
	showMain func()
	navigate func(kind, value string)
}

func newAppMenu(app *tango.App, showMain func(), navigate func(kind, value string)) *appMenu {
	return &appMenu{app: app, showMain: showMain, navigate: navigate}
}

func (m *appMenu) install() {
	if err := menu.SetAppMenu(m.app, m.build()); err != nil {
		log.Printf("app menu unavailable: %v", err)
	}
}

func (m *appMenu) build() *menu.Menu {
	root := menu.New()
	app := root.Submenu("MinCode")
	app.Role(menu.About)
	app.Add("设置…", func() { m.navigate("view", "settings") }).Accelerator = "CmdOrCtrl+,"
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

type trayMenu struct {
	app            *tango.App
	show, hide, qt func()
}

func newTrayMenu(app *tango.App, show, hide, quit func()) *trayMenu {
	return &trayMenu{app: app, show: show, hide: hide, qt: quit}
}

// install 创建托盘图标并挂上菜单（OnReady 里调用）。
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
	if err := icon.SetMenu(buildTrayMenu(t.show, t.hide, t.qt)); err != nil {
		log.Printf("tray menu unavailable: %v", err)
	}
}

func buildTrayMenu(show, hide, quit func()) *menu.Menu {
	m := menu.New()
	m.Add("显示主窗口", show)
	m.Add("隐藏主窗口", hide)
	m.Separator()
	m.Add("退出", quit)
	return m
}
