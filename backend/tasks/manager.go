package tasks

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Executor func(context.Context, Task, *Reporter) (Result, error)
type Manager struct {
	mu        sync.Mutex
	store     *Store
	executors map[Kind]Executor
	running   map[string]context.CancelFunc
	locks     map[string]string
	stopped   bool
	wake      chan struct{}
	done      chan struct{}
	onChange  func(Task)
	cleanup   func(Task)
}

// NewManager repairs interrupted records before permitting any new execution.
// onChange is only a notification, delivered outside the manager lock.
func NewManager(ctx context.Context, s *Store, executors map[Kind]Executor, onChange func(Task)) (*Manager, error) {
	if err := s.recoverInterrupted(ctx); err != nil {
		return nil, err
	}
	copied := make(map[Kind]Executor, len(executors))
	for k, v := range executors {
		if !validKind(k) || v == nil {
			return nil, ErrInvalidRequest
		}
		copied[k] = v
	}
	m := &Manager{store: s, executors: copied, running: map[string]context.CancelFunc{}, locks: map[string]string{}, wake: make(chan struct{}, 1), done: make(chan struct{}), onChange: onChange}
	go m.loop()
	return m, nil
}

// SetCleanup installs domain-owned terminal input cleanup before admission.
func (m *Manager) SetCleanup(cleanup func(Task)) { m.mu.Lock(); m.cleanup = cleanup; m.mu.Unlock() }
func (m *Manager) cleanupTask(task Task) {
	m.mu.Lock()
	fn := m.cleanup
	m.mu.Unlock()
	if fn != nil {
		fn(task)
	}
}
func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *Manager) emit(task Task) {
	if m.onChange != nil {
		m.onChange(task)
	}
}
func (m *Manager) Submit(ctx context.Context, r Request) (Task, error) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return Task{}, ErrStopped
	}
	if m.executors[r.Kind] == nil {
		m.mu.Unlock()
		return Task{}, ErrInvalidRequest
	}
	task, err := m.store.Admit(ctx, r)
	m.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	m.emit(task)
	m.signal()
	return task, nil
}
func (m *Manager) Get(ctx context.Context, id string) (Task, error) { return m.store.Get(ctx, id) }
func (m *Manager) List(ctx context.Context, f Filter) (Page, error) { return m.store.List(ctx, f) }
func (m *Manager) Cancel(ctx context.Context, id string) (Task, error) {
	m.mu.Lock()
	task, err := m.store.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return Task{}, err
	}
	if task.State.Terminal() {
		m.mu.Unlock()
		return task, nil
	}
	if task.State == StateQueued {
		task.State = StateCancelled
		task.FinishedAt = time.Now().UnixMilli()
	} else {
		task.State = StateCancelling
	}
	task, err = m.store.Update(ctx, task, task.Revision)
	if err == nil {
		if cancel := m.running[id]; cancel != nil {
			cancel()
		}
	}
	m.mu.Unlock()
	if err == nil {
		if task.State.Terminal() {
			m.cleanupTask(task)
		}
		m.emit(task)
		m.signal()
	}
	return task, err
}
func (m *Manager) loop() {
	defer close(m.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		if m.stopped && len(m.running) == 0 {
			m.mu.Unlock()
			return
		}
		if !m.stopped && len(m.running) < 2 {
			// Paginate all active records; a resource-blocked head must not starve unrelated work.
			cursor := ""
			for len(m.running) < 2 {
				page, err := m.store.List(context.Background(), Filter{ActiveOnly: true, Limit: 100, Cursor: cursor})
				if err != nil {
					break
				}
				for i := len(page.Tasks) - 1; i >= 0 && len(m.running) < 2; i-- {
					task := page.Tasks[i]
					if task.State != StateQueued {
						continue
					}
					blocked := false
					for _, key := range task.Request.Resources {
						if _, ok := m.locks[key]; ok {
							blocked = true
							break
						}
					}
					if blocked {
						continue
					}
					task.State = StateRunning
					task.StartedAt = time.Now().UnixMilli()
					task, err = m.store.claim(context.Background(), task)
					if err != nil {
						continue
					}
					ctx, cancel := context.WithCancel(context.Background())
					m.running[task.ID] = cancel
					for _, key := range task.Request.Resources {
						m.locks[key] = task.ID
					}
					go m.execute(ctx, task)
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
		}
		m.mu.Unlock()
		select {
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

var errExecutorPanic = errors.New("task_executor_panic")

func callExecutor(ctx context.Context, fn Executor, task Task, r *Reporter) (result Result, err error) {
	defer func() {
		if recover() != nil {
			err = errExecutorPanic
		}
	}()
	return fn(ctx, task, r)
}
func (m *Manager) execute(ctx context.Context, task Task) {
	m.emit(task)
	result, runErr := callExecutor(ctx, m.executors[task.Kind], task, &Reporter{manager: m, id: task.ID})
	// A cancelled operation still needs to record confirmed commits with a live local context.
	local, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.mu.Lock()
	current, err := m.store.Get(local, task.ID)
	if err == nil {
		current.Result = mergeResult(current.Result, result)
		current.State = StateSucceeded
		if runErr != nil {
			current.State = StateFailed
			current.ErrorCode = "task_execution_failed"
			var diagnostic Diagnostic
			if errors.As(runErr, &diagnostic) && validDiagnostic(string(diagnostic)) {
				current.ErrorCode = string(diagnostic)
			}
			if errors.Is(runErr, errExecutorPanic) {
				current.ErrorCode = "task_executor_panic"
			}
			if errors.Is(runErr, context.Canceled) {
				current.State = StateCancelled
				current.ErrorCode = ""
			}
		}
		current.FinishedAt = time.Now().UnixMilli()
		current, err = m.store.finish(local, current)
	}
	if stop := m.running[task.ID]; stop != nil {
		stop()
	}
	delete(m.running, task.ID)
	for key, owner := range m.locks {
		if owner == task.ID {
			delete(m.locks, key)
		}
	}
	// Persisted locks remain on local storage failure: fail closed for conflicting work.
	m.mu.Unlock()
	m.cleanupTask(task)
	if err == nil {
		m.emit(current)
	}
	m.signal()
}
func mergeResult(a, b Result) Result {
	if b.Artifact != "" {
		a.Artifact = b.Artifact
	}
	if b.Removed > a.Removed {
		a.Removed = b.Removed
	}
	if b.Records > a.Records {
		a.Records = b.Records
	}
	if b.Objects > a.Objects {
		a.Objects = b.Objects
	}
	if b.Files > a.Files {
		a.Files = b.Files
	}
	if b.Bytes > a.Bytes {
		a.Bytes = b.Bytes
	}
	if b.Certainty == EffectsUncertain || a.Certainty == EffectsUncertain {
		a.Certainty = EffectsUncertain
	} else if b.Certainty == EffectsKnown {
		a.Certainty = EffectsKnown
	}
	for _, warning := range b.Warnings {
		if len(a.Warnings) >= 50 {
			break
		}
		a.Warnings = append(a.Warnings, warning)
	}
	return a
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	for _, cancel := range m.running {
		cancel()
	}
	m.mu.Unlock()
	m.signal()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validDiagnostic(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
