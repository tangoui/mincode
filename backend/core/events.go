package core

// 事件发射桥：backend 各子包不直接依赖 宿主应用实例，
// 由 main.go 在创建 app 后注入 emitter，实现"后端 → 前端"事件推送
// （对齐 Tauri 的 Channel / app.emit 语义，前端经 vue-app/src/services/desktop-events.ts 订阅）。

import "sync"

const AppNotificationEvent = "app-notification"

// AppNotificationProgress 描述可选的后台任务进度。Total 为空时，前端只显示当前进度值。
type AppNotificationProgress struct {
	Current int  `json:"current"`
	Total   *int `json:"total,omitempty"`
}

// AppNotificationPayload 是后端写入通知中心的稳定事件协议。
// Code 应优先用于已知业务场景，让前端通过 i18n 渲染文案；不要在任何字段中传递
// 密码、令牌、私钥、未脱敏的远程输出或敏感本地路径。
type AppNotificationPayload struct {
	Source      string                   `json:"source"`
	Level       string                   `json:"level"`
	Code        string                   `json:"code,omitempty"`
	Title       string                   `json:"title,omitempty"`
	Message     string                   `json:"message,omitempty"`
	Detail      string                   `json:"detail,omitempty"`
	ProgressKey string                   `json:"progressKey,omitempty"`
	Progress    *AppNotificationProgress `json:"progress,omitempty"`
}

var (
	emitterMu sync.RWMutex
	emitter   func(name string, data any)
)

// SetEmitter 注入事件发射实现（main.go 创建 app 后调用一次）。
func SetEmitter(fn func(name string, data any)) {
	emitterMu.Lock()
	defer emitterMu.Unlock()
	emitter = fn
}

// Emit 向前端发射一个事件。emitter 未注入时静默丢弃（nil 安全）。
func Emit(name string, data any) {
	emitterMu.RLock()
	fn := emitter
	emitterMu.RUnlock()
	if fn != nil {
		fn(name, data)
	}
}

// Notify 向前端通知中心写入一条应用通知。
func Notify(payload AppNotificationPayload) {
	Emit(AppNotificationEvent, payload)
}
