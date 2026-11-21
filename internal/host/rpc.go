package host

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tangoui/tango/ipc"
)

// 页面经 tango ipc 使用的名字（页面侧见 vue-app/src/services/desktop-runtime.ts）。
const (
	// methodInvoke 调用后端命令：call(methodInvoke, {command, args})，结果是命令返回的 JSON 值。
	methodInvoke = "mincode.invoke"
	// methodInvokeStream 同上，但以只发一帧的流承载，页面取消流即取消后端 ctx
	// （ipc 的普通调用不能取消，前端带 AbortSignal 的调用走这里）。
	methodInvokeStream = "mincode.invokeStream"
	// methodNative 原生能力：call(methodNative, {op, args})，op 见 native.go。
	methodNative = "mincode.native"
	// eventEmit 页面广播应用事件：emit(eventEmit, {name, data})，送到所有页面（含发送者）。
	eventEmit = "mincode.emit"
)

func (h *Host) install() {
	h.router.Handle(methodInvoke, func(ctx context.Context, raw json.RawMessage) (any, error) {
		return h.invoke(ctx, raw)
	})
	h.router.HandleStream(methodInvokeStream, func(ctx context.Context, raw json.RawMessage, w ipc.StreamWriter) error {
		v, err := h.invoke(ctx, raw)
		if err != nil {
			return err
		}
		return w.Write(v)
	})
	h.router.Handle(methodNative, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := firstArg(raw, &a); err != nil {
			return nil, err
		}
		page, _ := ipc.ConnFrom(ctx)
		return h.callNative(page, a.Op, a.Args)
	})
	h.router.On(eventEmit, func(_ ipc.Conn, raw json.RawMessage) {
		var ev struct {
			Name string          `json:"name"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &ev) != nil || ev.Name == "" {
			return
		}
		var data any = ev.Data
		if len(ev.Data) == 0 {
			data = nil
		}
		h.Emit(ev.Name, data)
	})
}

// invoke 分发一次后端命令。后端返回的是 JSON 文本，原样作为结果值交给页面，
// 页面拿到的就是解析好的值（不再二次 JSON.parse）。
func (h *Host) invoke(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	if err := firstArg(raw, &a); err != nil {
		return nil, err
	}
	if a.Command == "" {
		return nil, ipc.NewError(ipc.CodeBadArgs, "missing command")
	}
	args := string(a.Args)
	if args == "" || args == "null" {
		args = "{}"
	}
	out, err := h.dispatch(ctx, a.Command, args)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	if !json.Valid([]byte(out)) {
		return out, nil // 非 JSON 结果按字符串交付
	}
	return json.RawMessage(out), nil
}

// firstArg 解出 ipc 调用的第一个位置参数（页面按位置传参，MinCode 只传一个对象）。
func firstArg(raw json.RawMessage, v any) error {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return ipc.NewError(ipc.CodeBadArgs, fmt.Sprintf("invalid arguments: %v", err))
	}
	if len(list) == 0 {
		return ipc.NewError(ipc.CodeBadArgs, "missing argument")
	}
	if err := json.Unmarshal(list[0], v); err != nil {
		return ipc.NewError(ipc.CodeBadArgs, fmt.Sprintf("invalid argument: %v", err))
	}
	return nil
}
