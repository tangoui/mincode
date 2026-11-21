package main

import (
	"testing"
	"time"

	"mincode/app/backend/desktop"

	"github.com/tangoui/tango/sys/menu"
)

func TestBuildTrayMenuPluginGroups(t *testing.T) {
	clicked := make(chan [2]string, 1)
	groups := []desktop.PluginTrayGroup{
		{PluginID: "only.sep", Name: "Only separators", Items: []desktop.PluginTrayItem{{Separator: true}}},
		{PluginID: "dl.mgr", Name: "下载 & 管理", Items: []desktop.PluginTrayItem{
			{CommandID: "pause", Title: "全部暂停"},
			{Separator: true},
			{CommandID: "resume", Title: "全部继续"},
		}},
	}
	m := buildTrayMenu(func() {}, func() {}, func() {}, groups, "windows", func(p, c string) { clicked <- [2]string{p, c} })

	labels := func(items []*menu.Item) []string {
		var out []string
		for _, it := range items {
			if it.Separator {
				out = append(out, "--")
			} else {
				out = append(out, it.Label)
			}
		}
		return out
	}
	got := labels(m.Items)
	want := []string{"显示主窗口", "隐藏主窗口", "--", "退出", "--", "下载 && 管理"}
	if len(got) != len(want) {
		t.Fatalf("tray items %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tray items %q, want %q", got, want)
		}
	}
	sub := m.Items[len(m.Items)-1].Submenu
	if sub == nil || len(sub.Items) != 3 || !sub.Items[1].Separator {
		t.Fatalf("plugin submenu %+v", sub)
	}
	sub.Items[2].OnClick()
	select {
	case c := <-clicked:
		if c != [2]string{"dl.mgr", "resume"} {
			t.Fatalf("clicked %v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin item click not delivered")
	}

	if n := len(buildTrayMenu(func() {}, func() {}, func() {}, nil, "darwin", nil).Items); n != 4 {
		t.Fatalf("no plugin groups: %d items, want MinCode's own 4", n)
	}
}

func TestTrayLabelMnemonics(t *testing.T) {
	for _, c := range []struct{ in, goos, want string }{
		{"A & B_c", "windows", "A && B_c"},
		{"A & B_c", "linux", "A & B__c"},
		{"A & B_c", "darwin", "A & B_c"},
	} {
		if got := trayLabel(c.in, c.goos); got != c.want {
			t.Errorf("trayLabel(%q, %s) = %q, want %q", c.in, c.goos, got, c.want)
		}
	}
}

// 插件页面接管键盘时，“设置…”的快捷键要能关掉，系统角色项不受影响。
func TestAppMenuAccelerators(t *testing.T) {
	m := newAppMenu(nil, func() {}, func(string, string) {})
	settings := func(root *menu.Menu) *menu.Item {
		for _, it := range root.Items[0].Submenu.Items {
			if it.Label == "设置…" {
				return it
			}
		}
		t.Fatal("no settings item")
		return nil
	}
	if got := settings(m.build(true)).Accelerator; got != "CmdOrCtrl+," {
		t.Fatalf("enabled accelerator %q", got)
	}
	if got := settings(m.build(false)).Accelerator; got != "" {
		t.Fatalf("disabled accelerator %q", got)
	}
	// 安装前切换只记状态，不碰原生菜单（app 为 nil 时 apply 会崩）。
	m.setAccelerators(false)
	if m.accelerators {
		t.Fatal("state not recorded")
	}
}

func TestTrayMenuSkipsUnchangedGroups(t *testing.T) {
	tm := newTrayMenu(nil, func() {}, func() {}, func() {})
	groups := []desktop.PluginTrayGroup{{PluginID: "p", Name: "P", Items: []desktop.PluginTrayItem{{CommandID: "c", Title: "C"}}}}
	tm.setPluginGroups(groups) // 托盘未创建：只记下分组
	groups[0].Items[0].Title = "changed by caller"
	if tm.groups[0].Items[0].Title != "C" {
		t.Fatal("groups not copied")
	}
	tm.setPluginGroups(nil)
	if tm.groups != nil {
		t.Fatal("empty groups not recorded")
	}
}
