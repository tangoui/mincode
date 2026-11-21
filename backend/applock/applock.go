// Package applock 是应用锁屏的唯一权威状态。
//
// 口令校验记录、锁定状态与偏好都由后端持有并写入 app_kv[app_lock_v1]；前端各窗口
// 只拿到不含口令材料的公开状态，所有变更都经下面的命令完成。这样：
//   - 解锁只接受口令明文，校验记录取自后端存储，调用方无法“自带记录”；
//   - 修改/关闭口令后，任何窗口都无法再用旧口令解锁，也无法把旧快照写回；
//   - 失败退避在进程内统一计数，换个窗口重试不会重置。
//
// 命令契约（入参 camelCase）：
//
//	app_lock_state           {}                               -> State
//	app_lock_lock            {}                               -> State
//	app_lock_unlock          {passcode}                       -> AttemptResult
//	app_lock_set_passcode    {currentPasscode?, passcode}     -> AttemptResult
//	app_lock_disable         {currentPasscode}                -> AttemptResult
//	app_lock_set_preferences {autoLockMinutes?, lockOnLaunch?} -> State
//
// 每次状态变化都会广播 app-lock:state（载荷为 State），前端收到后重新拉取并按
// revision 只接受更新的状态。
package applock

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"mincode/app/backend/core"
	"mincode/app/backend/crypto"
	"mincode/app/backend/internal/applockpolicy"
	"mincode/app/backend/storage"
)

const (
	StateEvent = "app-lock:state"

	defaultAutoLockMinutes = 10
	maxAutoLockMinutes     = 720
	minPasscodeLen         = 4
	maxPasscodeLen         = 128
	freeAttempts           = 3
	maxBackoff             = 60 * time.Second
)

var (
	openDB = storage.DB
	now    = time.Now
	state  = &manager{}
)

func init() {
	core.Register("app_lock_state", stateHandler)
	core.Register("app_lock_lock", lockHandler)
	core.Register("app_lock_unlock", unlockHandler)
	core.Register("app_lock_set_passcode", setPasscodeHandler)
	core.Register("app_lock_disable", disableHandler)
	core.Register("app_lock_set_preferences", setPreferencesHandler)
}

// State 是发给前端的公开状态，不含任何口令材料。
type State struct {
	Enabled         bool   `json:"enabled"`
	AutoLockMinutes int    `json:"autoLockMinutes"`
	LockOnLaunch    bool   `json:"lockOnLaunch"`
	Locked          bool   `json:"locked"`
	Revision        uint64 `json:"revision"`
}

// AttemptResult 是需要校验口令的操作结果。
type AttemptResult struct {
	OK             bool  `json:"ok"`
	FailedAttempts int   `json:"failedAttempts"`
	RetryAfterMs   int64 `json:"retryAfterMs"`
	State          State `json:"state"`
}

// record 是持久化形状，与旧版前端写入的 app_lock_v1 兼容（多了 revision）。
type record struct {
	Enabled         bool                   `json:"enabled"`
	Passcode        *crypto.PasscodeRecord `json:"passcode,omitempty"`
	AutoLockMinutes int                    `json:"autoLockMinutes"`
	LockOnLaunch    bool                   `json:"lockOnLaunch"`
	Locked          bool                   `json:"locked"`
	Revision        uint64                 `json:"revision"`
}

func (r record) public() State {
	return State{
		Enabled:         r.Enabled,
		AutoLockMinutes: r.AutoLockMinutes,
		LockOnLaunch:    r.LockOnLaunch,
		Locked:          r.Locked,
		Revision:        r.Revision,
	}
}

func (r record) configured() bool { return r.Enabled && r.Passcode != nil }

type manager struct {
	// opMu 串行化所有变更（包括耗时的 PBKDF2），保证校验与提交之间记录不变。
	opMu sync.Mutex
	// mu 只保护下面的字段，持有时间很短，读状态不会被口令派生阻塞。
	mu      sync.Mutex
	loaded  bool
	cur     record
	failed  int
	retryAt time.Time
}

// ---- 持久化 ----

func normalizeRecord(raw []byte) record {
	out := record{AutoLockMinutes: defaultAutoLockMinutes, LockOnLaunch: true}
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out
	}
	var passcode crypto.PasscodeRecord
	if value, ok := fields["passcode"]; ok && json.Unmarshal(value, &passcode) == nil && crypto.ValidPasscodeRecord(passcode) {
		out.Passcode = &passcode
	}
	// 没有可用口令记录时锁必须视为关闭，否则一份损坏的配置会把用户永久挡在门外。
	out.Enabled = jsonBool(fields["enabled"]) && out.Passcode != nil
	if !out.Enabled {
		out.Passcode = nil
	}
	if value, ok := fields["autoLockMinutes"]; ok {
		out.AutoLockMinutes = normalizeAutoLockMinutes(value)
	}
	if value, ok := fields["lockOnLaunch"]; ok {
		var b bool
		if json.Unmarshal(value, &b) == nil && !b {
			out.LockOnLaunch = false
		}
	}
	out.Locked = out.Enabled && jsonBool(fields["locked"])
	if value, ok := fields["revision"]; ok {
		_ = json.Unmarshal(value, &out.Revision)
	}
	return out
}

func jsonBool(raw json.RawMessage) bool {
	var b bool
	return len(raw) > 0 && json.Unmarshal(raw, &b) == nil && b
}

// normalizeAutoLockMinutes 与前端 normalizeAutoLockMinutes 的语义保持一致：
// 读不懂的值回落到默认间隔（而不是 0，0 等于悄悄关掉自动锁屏）。
func normalizeAutoLockMinutes(raw json.RawMessage) int {
	minutes := math.NaN()
	var v any
	if json.Unmarshal(raw, &v) == nil {
		switch x := v.(type) {
		case float64:
			minutes = x
		case string:
			if s := strings.TrimSpace(x); s == "" {
				minutes = 0
			} else if f, err := strconv.ParseFloat(s, 64); err == nil {
				minutes = f
			}
		case bool:
			if x {
				minutes = 1
			} else {
				minutes = 0
			}
		case nil:
			minutes = 0
		}
	}
	return clampAutoLockMinutes(minutes)
}

func clampAutoLockMinutes(minutes float64) int {
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) {
		return defaultAutoLockMinutes
	}
	if minutes <= 0 {
		return 0
	}
	return int(math.Min(math.Floor(minutes+0.5), maxAutoLockMinutes))
}

func persist(r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		applockpolicy.StorageKey, raw); err != nil {
		return fmt.Errorf("写入锁屏配置: %w", err)
	}
	return nil
}

// ensureLoadedLocked 在进程内首次访问时读取记录并应用“启动即锁”。调用方须持有 mu。
func (m *manager) ensureLoadedLocked() error {
	if m.loaded {
		return nil
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	var raw []byte
	err = db.QueryRow("SELECT value FROM app_kv WHERE key = ?", applockpolicy.StorageKey).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("读取锁屏配置: %w", err)
	}
	r := normalizeRecord(raw)
	launchLocked := r.Enabled && (r.LockOnLaunch || r.Locked)
	if launchLocked != r.Locked {
		r.Locked = launchLocked
		// 写回失败不影响本次锁定：内存状态已是锁定，只是强退后可能需要重新判定。
		if err := persist(r); err != nil {
			log.Printf("[applock] persist launch lock: %v", err)
		}
	}
	m.cur = r
	m.loaded = true
	return nil
}

func (m *manager) snapshot() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoadedLocked(); err != nil {
		return State{}, err
	}
	return m.cur.public(), nil
}

// commit 在 opMu 保护下应用一次变更：revision 递增、先落盘再生效，随后广播。
// bestEffort 为 true 时（锁定），落盘失败也保留内存中的变更，因为锁定方向总是安全的。
func (m *manager) commit(change func(r *record), bestEffort bool) (State, error) {
	m.mu.Lock()
	if err := m.ensureLoadedLocked(); err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	next := m.cur
	change(&next)
	next.Revision = m.cur.Revision + 1
	if err := persist(next); err != nil {
		if !bestEffort {
			m.mu.Unlock()
			return State{}, err
		}
		log.Printf("[applock] persist: %v", err)
	}
	m.cur = next
	public := next.public()
	m.mu.Unlock()
	core.Emit(StateEvent, public)
	notifyStateObservers(public)
	return public, nil
}

// ---- 后端观察者 ----

var stateObservers struct {
	mu  sync.RWMutex
	fns []func(State)
}

// OnStateChange 注册一个后端观察者：每次状态变化广播之后调用，调用时不持有本包的锁，
// 观察者可以再调用 Locked。插件全局快捷键据此在锁定时注销、解锁后恢复。
func OnStateChange(fn func(State)) {
	if fn == nil {
		panic("nil app lock observer")
	}
	stateObservers.mu.Lock()
	stateObservers.fns = append(stateObservers.fns, fn)
	stateObservers.mu.Unlock()
}

func notifyStateObservers(s State) {
	stateObservers.mu.RLock()
	fns := append([]func(State){}, stateObservers.fns...)
	stateObservers.mu.RUnlock()
	for _, fn := range fns {
		fn(s)
	}
}

// resetStateObservers 供测试使用。
func resetStateObservers() {
	stateObservers.mu.Lock()
	stateObservers.fns = nil
	stateObservers.mu.Unlock()
}

// Locked 报告应用当前是否锁定（首次读取时同样应用“启动即锁”）。读不到记录时按锁定处理，
// 让依赖它的后端能力失败关闭。
func Locked() bool {
	s, err := state.snapshot()
	if err != nil {
		log.Printf("[applock] read state: %v", err)
		return true
	}
	return s.Locked
}

// ---- 口令校验与退避 ----

func backoff(failed int) time.Duration {
	penalized := failed - freeAttempts
	if penalized <= 0 {
		return 0
	}
	if penalized > 8 {
		return maxBackoff
	}
	d := time.Second << (penalized - 1)
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

type attempt struct {
	ok      bool
	failed  int
	retryMs int64
}

// checkPasscode 用后端保存的记录校验口令。调用方须持有 opMu 且不持有 mu。
// 未配置口令时视为通过。
func (m *manager) checkPasscode(input string) (attempt, error) {
	m.mu.Lock()
	if err := m.ensureLoadedLocked(); err != nil {
		m.mu.Unlock()
		return attempt{}, err
	}
	if !m.cur.configured() {
		m.mu.Unlock()
		return attempt{ok: true}, nil
	}
	if t := now(); t.Before(m.retryAt) {
		res := attempt{failed: m.failed, retryMs: m.retryAt.Sub(t).Milliseconds() + 1}
		m.mu.Unlock()
		return res, nil
	}
	verifier := *m.cur.Passcode
	m.mu.Unlock()

	ok, err := crypto.VerifyPasscode(verifier, input)
	if err != nil {
		return attempt{}, fmt.Errorf("校验锁屏口令: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.failed = 0
		m.retryAt = time.Time{}
		return attempt{ok: true}, nil
	}
	m.failed++
	res := attempt{failed: m.failed}
	if d := backoff(m.failed); d > 0 {
		m.retryAt = now().Add(d)
		res.retryMs = d.Milliseconds()
	}
	return res, nil
}

func (m *manager) resetFailures() {
	m.mu.Lock()
	m.failed = 0
	m.retryAt = time.Time{}
	m.mu.Unlock()
}

func failedResult(a attempt, s State) AttemptResult {
	return AttemptResult{OK: false, FailedAttempts: a.failed, RetryAfterMs: a.retryMs, State: s}
}

// ---- 操作 ----

func (m *manager) lock() (State, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	current, err := m.snapshot()
	if err != nil {
		return State{}, err
	}
	if !current.Enabled || current.Locked {
		return current, nil
	}
	m.resetFailures()
	return m.commit(func(r *record) { r.Locked = true }, true)
}

func (m *manager) unlock(passcode string) (AttemptResult, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	a, err := m.checkPasscode(passcode)
	if err != nil {
		return AttemptResult{}, err
	}
	if !a.ok {
		s, err := m.snapshot()
		if err != nil {
			return AttemptResult{}, err
		}
		return failedResult(a, s), nil
	}
	current, err := m.snapshot()
	if err != nil {
		return AttemptResult{}, err
	}
	if !current.Locked {
		return AttemptResult{OK: true, State: current}, nil
	}
	s, err := m.commit(func(r *record) { r.Locked = false }, false)
	if err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{OK: true, State: s}, nil
}

func validatePasscode(passcode string) error {
	n := len(utf16.Encode([]rune(passcode)))
	if n < minPasscodeLen || n > maxPasscodeLen {
		return fmt.Errorf("锁屏口令长度须在 %d 到 %d 个字符之间", minPasscodeLen, maxPasscodeLen)
	}
	return nil
}

func (m *manager) setPasscode(current, next string) (AttemptResult, error) {
	if err := validatePasscode(next); err != nil {
		return AttemptResult{}, err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	a, err := m.checkPasscode(current)
	if err != nil {
		return AttemptResult{}, err
	}
	if !a.ok {
		s, err := m.snapshot()
		if err != nil {
			return AttemptResult{}, err
		}
		return failedResult(a, s), nil
	}
	verifier, err := crypto.HashPasscode(next)
	if err != nil {
		return AttemptResult{}, fmt.Errorf("派生锁屏口令: %w", err)
	}
	s, err := m.commit(func(r *record) {
		r.Enabled = true
		r.Passcode = &verifier
		r.Locked = false
	}, false)
	if err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{OK: true, State: s}, nil
}

func (m *manager) disable(current string) (AttemptResult, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	a, err := m.checkPasscode(current)
	if err != nil {
		return AttemptResult{}, err
	}
	if !a.ok {
		s, err := m.snapshot()
		if err != nil {
			return AttemptResult{}, err
		}
		return failedResult(a, s), nil
	}
	s, err := m.commit(func(r *record) {
		r.Enabled = false
		r.Passcode = nil
		r.Locked = false
	}, false)
	if err != nil {
		return AttemptResult{}, err
	}
	m.resetFailures()
	return AttemptResult{OK: true, State: s}, nil
}

type preferences struct {
	AutoLockMinutes *float64 `json:"autoLockMinutes"`
	LockOnLaunch    *bool    `json:"lockOnLaunch"`
}

func (m *manager) setPreferences(p preferences) (State, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if p.AutoLockMinutes == nil && p.LockOnLaunch == nil {
		return m.snapshot()
	}
	return m.commit(func(r *record) {
		if p.AutoLockMinutes != nil {
			r.AutoLockMinutes = clampAutoLockMinutes(*p.AutoLockMinutes)
		}
		if p.LockOnLaunch != nil {
			r.LockOnLaunch = *p.LockOnLaunch
		}
	}, false)
}

// ---- 命令处理器 ----

func stateHandler(_, _ string) (string, error) {
	s, err := state.snapshot()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(s)
}

func lockHandler(_, _ string) (string, error) {
	s, err := state.lock()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(s)
}

type unlockArgs struct {
	Passcode string `json:"passcode"`
}

func unlockHandler(_, argsJSON string) (string, error) {
	var a unlockArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	res, err := state.unlock(a.Passcode)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type setPasscodeArgs struct {
	CurrentPasscode string `json:"currentPasscode"`
	Passcode        string `json:"passcode"`
}

func setPasscodeHandler(_, argsJSON string) (string, error) {
	var a setPasscodeArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	res, err := state.setPasscode(a.CurrentPasscode, a.Passcode)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type disableArgs struct {
	CurrentPasscode string `json:"currentPasscode"`
}

func disableHandler(_, argsJSON string) (string, error) {
	var a disableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	res, err := state.disable(a.CurrentPasscode)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func setPreferencesHandler(_, argsJSON string) (string, error) {
	var p preferences
	if err := core.DecodeArgs(argsJSON, &p); err != nil {
		return "", err
	}
	s, err := state.setPreferences(p)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(s)
}
