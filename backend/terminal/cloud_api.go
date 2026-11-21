package terminal

import "sync"

// CloudShellOptions 是 cloudcompanion 可使用的最小本地 PTY 创建契约。
type CloudShellOptions struct {
	SessionID string
	ShellType string
	Cwd       string
	Cols      uint32
	Rows      uint32
}

// CloudShellEvent 是云终端只读输出事件，不包含凭据或连接配置。
type CloudShellEvent struct {
	SessionID string
	Data      string
	Ended     bool
	Reason    string
}

var cloudShellSubscribers = struct {
	sync.RWMutex
	nextID    uint64
	listeners map[uint64]func(CloudShellEvent)
}{listeners: map[uint64]func(CloudShellEvent){}}

func StartCloudShell(options CloudShellOptions) error {
	var shellType, cwd *string
	if options.ShellType != "" {
		shellType = &options.ShellType
	}
	if options.Cwd != "" {
		cwd = &options.Cwd
	}
	cols, rows := options.Cols, options.Rows
	_, err := startLocalShell(StartLocalShellPayload{
		SessionID:      options.SessionID,
		ShellType:      shellType,
		Cwd:            cwd,
		AttachIfExists: true,
		Cols:           &cols,
		Rows:           &rows,
	})
	return err
}

func WriteCloudShell(sessionID, input string) error {
	return writeToShell(WriteToShellPayload{SessionID: sessionID, Input: input})
}

func ResizeCloudShell(sessionID string, cols, rows uint32) error {
	return applyLocalShellResize(sessionID, uint16(cols), uint16(rows))
}

func CloseCloudShell(sessionID string) {
	attention.forget(sessionID)
	if proc, ok := sessions.remove(sessionID); ok {
		shutdownShellProcess(proc)
	}
}

func CloudShellAlive(sessionID string) bool {
	_, ok := sessions.get(sessionID)
	return ok
}

func SubscribeCloudShellEvents(listener func(CloudShellEvent)) func() {
	cloudShellSubscribers.Lock()
	cloudShellSubscribers.nextID++
	id := cloudShellSubscribers.nextID
	cloudShellSubscribers.listeners[id] = listener
	cloudShellSubscribers.Unlock()
	return func() {
		cloudShellSubscribers.Lock()
		delete(cloudShellSubscribers.listeners, id)
		cloudShellSubscribers.Unlock()
	}
}

func publishCloudShellEvent(event CloudShellEvent) {
	cloudShellSubscribers.RLock()
	listeners := make([]func(CloudShellEvent), 0, len(cloudShellSubscribers.listeners))
	for _, listener := range cloudShellSubscribers.listeners {
		listeners = append(listeners, listener)
	}
	cloudShellSubscribers.RUnlock()
	for _, listener := range listeners {
		listener(event)
	}
}
