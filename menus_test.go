package main

import (
	"reflect"
	"testing"
)

func TestBuildTrayMenu(t *testing.T) {
	menu := buildTrayMenu(func() {}, func() {}, func() {})
	var labels []string
	for _, item := range menu.Items {
		labels = append(labels, item.Label)
	}
	if want := []string{"显示主窗口", "隐藏主窗口", "", "退出"}; !reflect.DeepEqual(labels, want) || !menu.Items[2].Separator {
		t.Fatalf("tray items = %q, want %q", labels, want)
	}
}

func TestAppMenuSettingsAccelerator(t *testing.T) {
	m := newAppMenu(nil, func() {}, func(string, string) {})
	for _, item := range m.build().Items[0].Submenu.Items {
		if item.Label == "设置…" {
			if item.Accelerator != "CmdOrCtrl+," {
				t.Fatalf("settings accelerator = %q", item.Accelerator)
			}
			return
		}
	}
	t.Fatal("no settings item")
}
