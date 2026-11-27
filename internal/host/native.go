package host

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tangoui/tango/ipc"
	"github.com/tangoui/tango/sys/dialog"
)

// callNative 处理 methodNative 的各项原生能力。窗口类操作作用于发起调用的页面所属窗口，
// 页面不能指定别的窗口。对话框被取消时返回 nil（前端据此当作取消）。
func (h *Host) callNative(page Page, op string, args json.RawMessage) (any, error) {
	decode := func(v any) error {
		if len(args) == 0 || string(args) == "null" {
			return nil
		}
		if err := json.Unmarshal(args, v); err != nil {
			return ipc.NewError(ipc.CodeBadArgs, fmt.Sprintf("invalid %s arguments: %v", op, err))
		}
		return nil
	}
	switch op {
	case "clipboard.setText":
		var a struct {
			Text string `json:"text"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		h.native.SetClipboardText(a.Text)
		return nil, nil
	case "clipboard.text":
		return h.native.ClipboardText(), nil

	case "dialog.message":
		var a struct {
			Kind    string               `json:"kind"`
			Options MessageDialogOptions `json:"options"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		return h.native.MessageDialog(a.Kind, a.Options)
	case "dialog.open":
		var opts OpenDialogOptions
		if err := decode(&opts); err != nil {
			return nil, err
		}
		paths, err := h.native.OpenDialog(opts)
		if errors.Is(err, dialog.ErrCanceled) {
			return nil, nil
		}
		if err != nil || len(paths) == 0 {
			return nil, err
		}
		return paths, nil
	case "dialog.save":
		var opts SaveDialogOptions
		if err := decode(&opts); err != nil {
			return nil, err
		}
		path, err := h.native.SaveDialog(opts)
		if errors.Is(err, dialog.ErrCanceled) {
			return nil, nil
		}
		if err != nil || path == "" {
			return nil, err
		}
		return path, nil

	case "notify.send":
		var a struct {
			Title  string         `json:"title"`
			Body   string         `json:"body"`
			Target map[string]any `json:"target"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		return nil, h.native.Notify(a.Title, a.Body, a.Target)
	case "shell.openURL":
		var a struct {
			URL string `json:"url"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		return nil, h.native.OpenExternal(a.URL)

	case "app.quit":
		h.native.Quit()
		return nil, nil
	case "app.hide":
		h.native.HideApp()
		return nil, nil
	case "app.show":
		h.native.ShowApp()
		return nil, nil
	case "system.isDarkMode":
		return h.native.IsDarkMode(), nil
	}

	var win Window
	if page != nil {
		win = h.native.WindowFor(page)
	}
	if win == nil {
		return nil, ipc.NewError(ipc.CodeNotFound, "unknown native op or no window for calling page: "+op)
	}
	switch op {
	case "window.minimize":
		win.Minimize()
	case "window.unminimize":
		win.Show()
	case "window.maximize":
		win.Maximize()
	case "window.unmaximize":
		win.Unmaximize()
	case "window.toggleMaximize":
		win.ToggleMaximize()
	case "window.fullscreen":
		win.SetFullScreen(true)
	case "window.unfullscreen":
		win.SetFullScreen(false)
	case "window.toggleFullscreen":
		win.SetFullScreen(!win.IsFullScreen())
	case "window.isFullscreen":
		return win.IsFullScreen(), nil
	case "window.isMaximized":
		return win.IsMaximized(), nil
	case "window.isMinimized":
		return win.IsMinimized(), nil
	case "window.restore":
		win.Unmaximize()
		win.Show()
	case "window.close":
		win.Close()
	case "window.hide":
		win.Hide()
	case "window.show":
		win.Show()
	case "window.focus":
		win.Focus()
	case "window.size":
		w, ht := win.Size()
		return map[string]int{"width": w, "height": ht}, nil
	case "window.setSize":
		var a struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		win.SetSize(a.Width, a.Height)
	case "window.position":
		x, y := win.Position()
		return map[string]int{"x": x, "y": y}, nil
	case "window.setPosition":
		var a struct {
			X int `json:"x"`
			Y int `json:"y"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		win.SetPosition(a.X, a.Y)
	case "window.setTitle":
		var a struct {
			Title string `json:"title"`
		}
		if err := decode(&a); err != nil {
			return nil, err
		}
		win.SetTitle(a.Title)
	default:
		return nil, ipc.NewError(ipc.CodeNotFound, "unknown native op: "+op)
	}
	return nil, nil
}
