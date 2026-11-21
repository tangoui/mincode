// Package backend 汇聚所有功能子包并对外暴露统一的命令分发入口。
//
// 设计：命令注册表与跨域公共 helper 由子包 backend/core 提供；各功能域拆分为独立
// 子包（git/fs/sessions/ai/crypto/device/license/http/plugin/updater/misc），在各自
// init() 中通过 core.Register(...) 注册处理器。本包经 register_all.go 空导入这些子包
// 以触发注册，并将 Dispatch/Commands 转发至 core。处理器输入/输出均为 JSON 字符串，
// 语义与 Tauri 命令（camelCase 参数、camelCase 返回结构）保持一致。
package backend

import (
	"context"
	"sync"

	"mincode/app/backend/core"
)

var (
	startupOnce  sync.Once
	startupErr   error
	shutdownOnce sync.Once
)

// Dispatch 按命令名分发调用（前端统一入口）。
// 分发前先按命令前缀懒启动对应的按需域（db/plugin）。
func Dispatch(cmd, argsJson string) (string, error) {
	return DispatchContext(context.Background(), cmd, argsJson)
}

// DispatchContext 与 Dispatch 相同，并把前端调用上下文（可被取消）传给处理器。
func DispatchContext(ctx context.Context, cmd, argsJson string) (string, error) {
	ensureDomainsForCommand(cmd)
	return core.DispatchContext(ctx, cmd, argsJson)
}

// Commands 返回已注册的命令名列表（用于调试/自检）。
func Commands() []string {
	return core.Commands()
}

// Startup initializes backend domains after the frontend event bridge is ready.
// Individual plugin failures are retained in plugin status and returned for logging.
func Startup(ctx context.Context) error {
	startupOnce.Do(func() { startupErr = startupDomains(ctx) })
	return startupErr
}

// Shutdown 以调用方 deadline 为总预算关闭所有后端资源；可重复调用。
func Shutdown(ctx context.Context) {
	shutdownOnce.Do(func() { shutdownDomains(ctx) })
}
