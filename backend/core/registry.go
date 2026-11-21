// Package core 提供 backend 各功能子包共享的基础设施：命令注册表与跨域公共
// helper。将这些共享符号放入独立包，可避免根包与功能子包之间的循环依赖，并让
// 每个功能域以独立 Go 子包的形式组织。
//
// 设计：功能子包在各自的 init() 中调用 core.Register(cmd, handler) 注册命令；
// 前端统一入口经由 backend.Dispatch(cmd, argsJson) → core.Dispatch 分发。处理器
// 输入/输出均为 JSON 字符串，语义与 Tauri 命令（camelCase 参数与返回结构）一致。
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Handler 处理单个命令。cmd 为命令名，argsJson 为前端传入的参数 JSON（对象），
// 返回值为结果 JSON；无返回值（void）时返回空字符串。
type Handler func(cmd, argsJson string) (string, error)

// HandlerCtx 与 Handler 相同，但接收调用上下文：前端取消调用（CancellablePromise.cancel /
// AbortSignal）或窗口关闭时 ctx 会被取消，长耗时命令应据此提前结束。
type HandlerCtx func(ctx context.Context, cmd, argsJson string) (string, error)

var (
	mu       sync.RWMutex
	handlers = map[string]HandlerCtx{}
)

// Register 注册一个命令处理器。重复注册会 panic，便于在开发期尽早发现冲突。
func Register(cmd string, h Handler) {
	RegisterCtx(cmd, func(_ context.Context, cmd, argsJson string) (string, error) {
		return h(cmd, argsJson)
	})
}

// RegisterCtx 注册一个可感知调用上下文的命令处理器。
func RegisterCtx(cmd string, h HandlerCtx) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := handlers[cmd]; exists {
		panic(fmt.Sprintf("backend: duplicate command handler: %s", cmd))
	}
	handlers[cmd] = h
}

// Dispatch 按命令名分发调用（无调用上下文的内部调用）。
func Dispatch(cmd, argsJson string) (string, error) {
	return DispatchContext(context.Background(), cmd, argsJson)
}

// DispatchContext 按命令名分发调用，并把调用上下文传给处理器。
func DispatchContext(ctx context.Context, cmd, argsJson string) (string, error) {
	mu.RLock()
	h, ok := handlers[cmd]
	mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown command: %s", cmd)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h(ctx, cmd, argsJson)
}

// Commands 返回已注册的命令名列表（用于调试/自检）。
func Commands() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(handlers))
	for k := range handlers {
		out = append(out, k)
	}
	return out
}

// DecodeArgs 将参数 JSON 反序列化到目标结构。空串按空对象处理。
func DecodeArgs(argsJson string, dst any) error {
	if argsJson == "" {
		argsJson = "{}"
	}
	if err := json.Unmarshal([]byte(argsJson), dst); err != nil {
		return fmt.Errorf("invalid args: %w", err)
	}
	return nil
}

// DecodeArgsPayload 解析形如 {"payload": {...}} 的入参，将内层 payload 反序列化到
// dst。这与 Tauri `invoke(cmd, { payload })` 把 payload 作为命名参数的语义对齐：
// 前端通过 platform 抽象层传来的 argsJson 顶层是 {"payload": {...}}，需剥离外层。
// 若入参没有 payload 外层（历史/兼容场景），则回退为直接解析整个对象到 dst。
func DecodeArgsPayload(argsJson string, dst any) error {
	if strings.TrimSpace(argsJson) == "" {
		argsJson = "{}"
	}
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(argsJson), &env); err == nil && len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, dst); err != nil {
			return fmt.Errorf("invalid args payload: %w", err)
		}
		return nil
	}
	// 无 payload 外层：直接解析整体
	if err := json.Unmarshal([]byte(argsJson), dst); err != nil {
		return fmt.Errorf("invalid args: %w", err)
	}
	return nil
}

// EncodeResult 将结果序列化为 JSON 字符串。
func EncodeResult(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(b), nil
}
