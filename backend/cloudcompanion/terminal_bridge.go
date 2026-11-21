package cloudcompanion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/storage"
	"mincode/app/backend/terminal"
)

// 前缀用区间谓词表达（':' 的下一个字符是 ';'），可走 app_kv 主键索引；LIKE 在 BINARY 排序下只能全表扫描。
const (
	loadCloudBindingsQuery      = "SELECT value FROM app_kv WHERE key >= 'cloud-terminal:' AND key < 'cloud-terminal;'"
	cleanupCloudOperationsQuery = "DELETE FROM app_kv WHERE key >= 'cloud-operation:' AND key < 'cloud-operation;' AND updated_at < datetime('now', '-7 days')"
)

var cloudTerminalSessionPattern = regexp.MustCompile(`^cloud-terminal-[A-Za-z0-9_-]{16,80}$`)

type cloudTerminalBinding struct {
	SessionID      string                        `json:"sessionId"`
	SourceDeviceID string                        `json:"sourceDeviceId"`
	Sequence       int64                         `json:"sequence"`
	OutputQueue    chan terminal.CloudShellEvent `json:"-"`
	// done 通知输出协程退出；OutputQueue 可能仍有并发发送方，因此不关闭它。
	done     chan struct{}
	stopOnce sync.Once
}

func newCloudTerminalBinding(sessionID, sourceDeviceID string) *cloudTerminalBinding {
	return &cloudTerminalBinding{
		SessionID:      sessionID,
		SourceDeviceID: sourceDeviceID,
		OutputQueue:    make(chan terminal.CloudShellEvent, 256),
		done:           make(chan struct{}),
	}
}

func (b *cloudTerminalBinding) stop() {
	b.stopOnce.Do(func() { close(b.done) })
}

type cloudTerminalManager struct {
	// store 负责持久化；为 nil 时使用本机数据库（测试替换为内存实现）。
	store cloudTerminalStore
	// sendMFAPrompt 把验证码提示发给手机；为 nil 时使用加密中继发送。
	sendMFAPrompt func(targetDeviceID, requestID string, payload map[string]any) error
	sshCreatesMu  sync.Mutex
	sshCreating   map[string]bool
	startMu       sync.Mutex
	mu            sync.Mutex
	bindings      map[string]*cloudTerminalBinding
	stop          func()
}

var cloudTerminals = &cloudTerminalManager{bindings: map[string]*cloudTerminalBinding{}}

func (m *cloudTerminalManager) start() error {
	// 后台 Startup 与首个云终端载荷可能并发触发 start，串行化避免重复加载绑定。
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if err := cleanupCloudOperations(); err != nil {
		return err
	}
	if err := m.loadBindings(); err != nil {
		return err
	}
	stop := terminal.SubscribeCloudShellEvents(m.handleEvent)
	terminal.SetCloudMFAPromptHandler(m.deliverMFAPrompt)
	m.mu.Lock()
	if m.stop == nil {
		m.stop = stop
		stop = nil
	}
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

func (m *cloudTerminalManager) stopAll() {
	m.mu.Lock()
	stop := m.stop
	bindings := make([]*cloudTerminalBinding, 0, len(m.bindings))
	for _, binding := range m.bindings {
		bindings = append(bindings, binding)
	}
	m.stop = nil
	m.bindings = map[string]*cloudTerminalBinding{}
	m.mu.Unlock()
	if stop != nil {
		stop()
		terminal.SetCloudMFAPromptHandler(nil)
	}
	for _, binding := range bindings {
		binding.stop()
		terminal.CloseCloudShell(binding.SessionID)
	}
}

func (m *cloudTerminalManager) setBinding(binding *cloudTerminalBinding) {
	m.mu.Lock()
	previous := m.bindings[binding.SessionID]
	m.bindings[binding.SessionID] = binding
	m.mu.Unlock()
	if previous != nil && previous != binding {
		previous.stop()
	}
}

func (m *cloudTerminalManager) handlePayload(event CloudPayloadEvent) (handled bool, resultErr error) {
	if !strings.HasPrefix(event.Channel, "terminal.") && !strings.HasPrefix(event.Channel, "ssh.") {
		return false, nil
	}
	if err := m.start(); err != nil {
		return true, err
	}
	operation := strings.TrimPrefix(event.Channel, "terminal.")
	if strings.HasPrefix(event.Channel, "ssh.") {
		operation = strings.TrimPrefix(event.Channel, "ssh.")
	}
	sessionID, _ := event.Payload["sessionId"].(string)
	sessionID = strings.TrimSpace(sessionID)
	if !cloudTerminalSessionPattern.MatchString(sessionID) {
		return true, errors.New("云终端 sessionId 无效")
	}
	if processed, err := cloudOperationProcessed(event.RequestID); err != nil {
		return true, err
	} else if processed {
		return true, nil
	}
	atMostOnce := operation == "input" || operation == "close"
	if atMostOnce {
		if err := beginCloudOperation(event.RequestID, event.Channel); err != nil {
			return true, err
		}
		defer func() {
			if resultErr != nil {
				_ = failCloudOperation(event.RequestID, event.Channel, resultErr.Error())
			}
		}()
	}

	switch operation {
	case "session.create":
		if strings.HasPrefix(event.Channel, "ssh.") {
			// ssh.session.create 由 startSSHCreate 在后台处理（见 realtime.go），不会走到这里。
			return true, errors.New("云 SSH 创建请求处理异常")
		} else if err := m.create(event, sessionID); err != nil {
			return true, err
		}
	case "input":
		if err := m.authorize(event.SourceDeviceID, sessionID); err != nil {
			return true, err
		}
		input, _ := event.Payload["input"].(string)
		if input == "" || len(input) > 32*1024 {
			return true, errors.New("云终端输入无效")
		}
		if err := terminal.WriteCloudShell(sessionID, input); err != nil {
			return true, err
		}
	case "resize":
		if err := m.authorize(event.SourceDeviceID, sessionID); err != nil {
			return true, err
		}
		cols, rows := number(event.Payload["cols"]), number(event.Payload["rows"])
		if cols < 20 || cols > 500 || rows < 5 || rows > 300 {
			return true, errors.New("云终端尺寸无效")
		}
		if err := terminal.ResizeCloudShell(sessionID, uint32(cols), uint32(rows)); err != nil {
			return true, err
		}
	case "auth.answer":
		if err := m.answerMFA(event.SourceDeviceID, sessionID, event.Payload); err != nil {
			return true, err
		}
	case "close":
		if err := m.authorize(event.SourceDeviceID, sessionID); err != nil {
			return true, err
		}
		terminal.CloseCloudShell(sessionID)
		m.removeBinding(sessionID)
	default:
		return true, errors.New("不支持的云终端操作")
	}
	if atMostOnce {
		if err := completeCloudOperation(event.RequestID, event.Channel); err != nil {
			return true, err
		}
	} else if err := markCloudOperation(event.RequestID, event.Channel); err != nil {
		return true, err
	}
	return true, nil
}

func (m *cloudTerminalManager) create(event CloudPayloadEvent, sessionID string) error {
	m.mu.Lock()
	if existing := m.bindings[sessionID]; existing != nil && existing.SourceDeviceID != event.SourceDeviceID {
		m.mu.Unlock()
		return errors.New("云终端会话已绑定其他设备")
	}
	m.mu.Unlock()
	cols, rows := number(event.Payload["cols"]), number(event.Payload["rows"])
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if cols < 20 || cols > 500 || rows < 5 || rows > 300 {
		return errors.New("云终端尺寸无效")
	}
	if err := terminal.StartCloudShell(terminal.CloudShellOptions{
		SessionID: sessionID,
		ShellType: stringValue(event.Payload["shellType"]),
		Cwd:       stringValue(event.Payload["cwd"]),
		Cols:      uint32(cols),
		Rows:      uint32(rows),
	}); err != nil {
		return err
	}
	binding := newCloudTerminalBinding(sessionID, event.SourceDeviceID)
	m.setBinding(binding)
	go m.runOutputQueue(binding)
	return saveCloudBinding(binding)
}

func (m *cloudTerminalManager) authorize(sourceDeviceID, sessionID string) error {
	m.mu.Lock()
	binding := m.bindings[sessionID]
	m.mu.Unlock()
	if binding == nil || binding.SourceDeviceID != sourceDeviceID {
		return errors.New("无权操作该云终端会话")
	}
	return nil
}

func (m *cloudTerminalManager) handleEvent(event terminal.CloudShellEvent) {
	m.mu.Lock()
	binding := m.bindings[event.SessionID]
	m.mu.Unlock()
	if binding == nil || binding.OutputQueue == nil {
		return
	}
	select {
	case binding.OutputQueue <- event:
	default:
		terminal.CloseCloudShell(event.SessionID)
		m.removeBinding(event.SessionID)
	}
}

func (m *cloudTerminalManager) runOutputQueue(binding *cloudTerminalBinding) {
	for {
		var event terminal.CloudShellEvent
		select {
		case <-binding.done:
			return
		case event = <-binding.OutputQueue:
		}
		m.mu.Lock()
		current := m.bindings[binding.SessionID]
		if current != binding {
			m.mu.Unlock()
			return
		}
		sequence := binding.Sequence
		binding.Sequence++
		m.mu.Unlock()
		payload := map[string]any{
			"sessionId": event.SessionID,
			"data":      event.Data,
			"ended":     event.Ended,
			"reason":    event.Reason,
			"offset":    sequence,
		}
		requestID := fmt.Sprintf("terminal-%s-%d", event.SessionID, sequence)
		if _, err := sendEncryptedCloudPayload(
			binding.SourceDeviceID,
			"terminal.output",
			requestID,
			sequence,
			payload,
		); err != nil {
			terminal.CloseCloudShell(event.SessionID)
			m.removeBinding(event.SessionID)
			return
		}
		if event.Ended {
			m.removeBinding(event.SessionID)
			return
		}
	}
}

func (m *cloudTerminalManager) removeBinding(sessionID string) {
	m.mu.Lock()
	binding := m.bindings[sessionID]
	delete(m.bindings, sessionID)
	m.mu.Unlock()
	if binding != nil {
		binding.stop()
	}
	m.terminalStore().deleteBinding(sessionID)
}

func (m *cloudTerminalManager) loadBindings() error {
	db, err := storage.DB()
	if err != nil {
		return err
	}
	rows, err := db.Query(loadCloudBindingsQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	loaded := make([]*cloudTerminalBinding, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var binding cloudTerminalBinding
		if json.Unmarshal(raw, &binding) == nil && terminal.CloudShellAlive(binding.SessionID) {
			restored := newCloudTerminalBinding(binding.SessionID, binding.SourceDeviceID)
			restored.Sequence = binding.Sequence
			loaded = append(loaded, restored)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, binding := range loaded {
		m.setBinding(binding)
	}
	for _, binding := range loaded {
		go m.runOutputQueue(binding)
	}
	return nil
}

func saveCloudBinding(binding *cloudTerminalBinding) error {
	db, err := storage.DB()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(binding)
	_, err = db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		"cloud-terminal:"+binding.SessionID, raw)
	return err
}

func cleanupCloudOperations() error {
	database, err := storage.DB()
	if err != nil {
		return err
	}
	_, err = database.Exec(
		cleanupCloudOperationsQuery,
	)
	return err
}

func cloudOperationProcessed(requestID string) (bool, error) {
	db, err := storage.DB()
	if err != nil {
		return false, err
	}
	var value []byte
	err = db.QueryRow("SELECT value FROM app_kv WHERE key = ?", "cloud-operation:"+requestID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(value, &state) == nil {
		switch state.Status {
		case "processing":
			return false, errors.New("云终端操作结果不确定，已阻止重复执行")
		case "failed":
			return true, errors.New("云终端操作此前已失败")
		}
	}
	return true, nil
}

func beginCloudOperation(requestID, channel string) error {
	return saveCloudOperation(requestID, channel, "processing", true)
}

func completeCloudOperation(requestID, channel string) error {
	return saveCloudOperation(requestID, channel, "completed", false)
}

func failCloudOperation(requestID, channel, message string) error {
	return saveCloudOperation(requestID, channel+":"+message, "failed", false)
}

func saveCloudOperation(requestID, channel, status string, insertOnly bool) error {
	db, err := storage.DB()
	if err != nil {
		return err
	}
	value, _ := json.Marshal(map[string]string{"channel": channel, "status": status})
	if insertOnly {
		result, err := db.Exec("INSERT OR IGNORE INTO app_kv(key, value) VALUES(?, ?)", "cloud-operation:"+requestID, value)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("云终端操作已存在")
		}
		return nil
	}
	_, err = db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		"cloud-operation:"+requestID, value)
	return err
}

func markCloudOperation(requestID, channel string) error {
	db, err := storage.DB()
	if err != nil {
		return err
	}
	key := "cloud-operation:" + requestID
	value, _ := json.Marshal(map[string]string{"channel": channel, "status": "completed"})
	_, err = db.Exec("INSERT OR IGNORE INTO app_kv(key, value) VALUES(?, ?)", key, value)
	return err
}

func number(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// cloudTerminalStore 抽出云终端用到的持久化，便于测试替换。
type cloudTerminalStore interface {
	operationProcessed(requestID string) (bool, error)
	markOperation(requestID, channel string) error
	saveBinding(binding *cloudTerminalBinding) error
	deleteBinding(sessionID string)
}

type databaseTerminalStore struct{}

func (databaseTerminalStore) operationProcessed(requestID string) (bool, error) {
	return cloudOperationProcessed(requestID)
}

func (databaseTerminalStore) markOperation(requestID, channel string) error {
	return markCloudOperation(requestID, channel)
}

func (databaseTerminalStore) saveBinding(binding *cloudTerminalBinding) error {
	return saveCloudBinding(binding)
}

func (databaseTerminalStore) deleteBinding(sessionID string) {
	if db, err := storage.DB(); err == nil {
		_, _ = db.Exec("DELETE FROM app_kv WHERE key = ?", "cloud-terminal:"+sessionID)
	}
}

func (m *cloudTerminalManager) terminalStore() cloudTerminalStore {
	if m.store != nil {
		return m.store
	}
	return databaseTerminalStore{}
}

func (m *cloudTerminalManager) bindingFor(sessionID string) *cloudTerminalBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bindings[sessionID]
}

type cloudSSHCreateRequest struct {
	sessionID  string
	profileID  string
	cols, rows uint32
}

func parseCloudSSHCreate(event CloudPayloadEvent) (cloudSSHCreateRequest, error) {
	sessionID := strings.TrimSpace(stringValue(event.Payload["sessionId"]))
	if !cloudTerminalSessionPattern.MatchString(sessionID) {
		return cloudSSHCreateRequest{}, errors.New("云终端 sessionId 无效")
	}
	profileID := stringValue(event.Payload["profileId"])
	cols, rows := number(event.Payload["cols"]), number(event.Payload["rows"])
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if profileID == "" || cols < 20 || cols > 500 || rows < 5 || rows > 300 {
		return cloudSSHCreateRequest{}, errors.New("云 SSH 创建参数无效")
	}
	return cloudSSHCreateRequest{sessionID: sessionID, profileID: profileID, cols: uint32(cols), rows: uint32(rows)}, nil
}

// startSSHCreate 在后台创建手机发起的 SSH 会话。SSH 连接可能要等手机输入验证码，
// 中继处理线程必须保持空闲才能收到那条回答，所以不能同步等待。
// 会话在连接前就绑定到发起的手机，验证码提示才能送达；同一请求重放时不会重复连接。
// done 在结束时恰好调用一次（重放到达且仍在连接中时不调用，由第一次请求负责回复）。
func (m *cloudTerminalManager) startSSHCreate(
	event CloudPayloadEvent,
	start func(profileID, sessionID string, cols, rows uint32) error,
	done func(error),
) {
	request, err := parseCloudSSHCreate(event)
	if err != nil {
		done(err)
		return
	}
	store := m.terminalStore()
	if processed, err := store.operationProcessed(event.RequestID); err != nil {
		done(err)
		return
	} else if processed {
		done(nil)
		return
	}
	m.sshCreatesMu.Lock()
	if m.sshCreating == nil {
		m.sshCreating = map[string]bool{}
	}
	if m.sshCreating[event.RequestID] {
		m.sshCreatesMu.Unlock()
		return
	}
	m.sshCreating[event.RequestID] = true
	m.sshCreatesMu.Unlock()

	binding := newCloudTerminalBinding(request.sessionID, event.SourceDeviceID)
	m.setBinding(binding)
	go func() {
		defer func() {
			m.sshCreatesMu.Lock()
			delete(m.sshCreating, event.RequestID)
			m.sshCreatesMu.Unlock()
		}()
		err := start(request.profileID, request.sessionID, request.cols, request.rows)
		if err == nil {
			go m.runOutputQueue(binding)
			err = store.saveBinding(binding)
			if err != nil {
				terminal.CloseCloudShell(request.sessionID)
			}
		}
		if err == nil {
			err = store.markOperation(event.RequestID, event.Channel)
		}
		if err != nil {
			m.removeBinding(request.sessionID)
		}
		done(err)
	}()
}

// deliverMFAPrompt 把手机发起的会话的验证码提示转发给那台手机；不是手机会话时返回 false，
// 由桌面界面照常弹出。
func (m *cloudTerminalManager) deliverMFAPrompt(prompt terminal.SshMfaPromptPayload) bool {
	binding := m.bindingFor(prompt.SessionID)
	if binding == nil {
		return false
	}
	send := m.sendMFAPrompt
	if send == nil {
		send = func(target, requestID string, payload map[string]any) error {
			_, err := sendEncryptedCloudPayload(target, "ssh.auth.prompt", requestID, 0, payload)
			return err
		}
	}
	prompts := make([]map[string]any, 0, len(prompt.Prompts))
	for _, item := range prompt.Prompts {
		prompts = append(prompts, map[string]any{"prompt": item.Prompt, "echo": item.Echo})
	}
	payload := map[string]any{
		"sessionId":    prompt.SessionID,
		"name":         prompt.Name,
		"instructions": prompt.Instructions,
		"prompts":      prompts,
	}
	requestID := fmt.Sprintf("ssh-auth-%s-%d", prompt.SessionID, time.Now().UnixNano())
	go func() {
		if err := send(binding.SourceDeviceID, requestID, payload); err != nil {
			log.Printf("cloud ssh mfa prompt delivery failed: %v", err)
		}
	}()
	return true
}

// answerMFA 把手机输入的验证码交给等待中的 SSH 认证；只接受发起该会话的手机。
// payload: {answers: [string]}，或 {cancel: true} 放弃本次认证。
func (m *cloudTerminalManager) answerMFA(sourceDeviceID, sessionID string, payload map[string]any) error {
	if err := m.authorize(sourceDeviceID, sessionID); err != nil {
		return err
	}
	var answers []string
	if cancel, _ := payload["cancel"].(bool); !cancel {
		raw, _ := payload["answers"].([]any)
		if len(raw) == 0 || len(raw) > 10 {
			return errors.New("验证码回答无效")
		}
		for _, value := range raw {
			text, ok := value.(string)
			if !ok || len(text) > 512 {
				return errors.New("验证码回答无效")
			}
			answers = append(answers, text)
		}
	}
	if !terminal.RespondCloudSSHMFA(sessionID, answers) {
		return errors.New("验证码已超时或当前无需输入，请重新连接")
	}
	return nil
}
