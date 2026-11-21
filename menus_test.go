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

func TestAppMenuCheckUpdateNavigation(t *testing.T) {
	t.Setenv("TANGO_LAUNCH_BACKGROUND", "1")
	var target []string
	m := newAppMenu(nil, func() {}, func(kind, value string) {
		target = append(target, kind, value)
		if shouldFocusNavigationWindow(kind) {
			t.Errorf("background navigation %q would focus the window", kind)
		}
	})
	for _, item := range m.build().Items[0].Submenu.Items {
		if item.Label == "检查更新…" {
			item.OnClick()
		}
	}
	if want := []string{"check-update", ""}; !reflect.DeepEqual(target, want) {
		t.Fatalf("navigation target = %q, want %q", target, want)
	}
}

func TestAppMenuOnlyInstalledOnMacOS(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": true, "windows": false, "linux": false} {
		if got := installsAppMenu(goos); got != want {
			t.Errorf("installsAppMenu(%q) = %v, want %v", goos, got, want)
		}
	}
}
