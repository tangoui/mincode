package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// scheduleRunner fires contributes.schedules. Every tick it looks at the
// plugins that are enabled, listed and effective in the active workspace;
// a schedule is first due one interval after it became eligible, and a
// failed run doubles the delay up to backoffMaxMinutes. Runs go through
// invokeContributedAction (lazy activation, timeout, result bound). A plugin
// runs one schedule at a time: when several are due, the others wait for a
// later tick, and a run refused because the plugin is busy is retried on the
// next tick without counting as a failure. Nothing is persisted: after a
// restart every schedule waits a full interval again.
const scheduleTickInterval = 30 * time.Second

type scheduleState struct {
	action   string
	interval time.Duration
	next     time.Time
	failures int
	running  bool
}

type scheduleRunnerState struct {
	mu     sync.Mutex
	states map[string]*scheduleState // pluginID + "\x00" + schedule id
	once   sync.Once
	now    func() time.Time
	// invoke is manager.invokeContributedAction; a seam for tests.
	invoke func(manager *pluginManager, ctx context.Context, call contributedActionCall) (json.RawMessage, error)
	wg     sync.WaitGroup
}

var scheduleRunner = &scheduleRunnerState{
	states: map[string]*scheduleState{},
	now:    time.Now,
	invoke: func(manager *pluginManager, ctx context.Context, call contributedActionCall) (json.RawMessage, error) {
		return manager.invokeContributedAction(ctx, call)
	},
}

func (manager *pluginManager) ensureScheduleRunner() {
	scheduleRunner.once.Do(func() {
		go func() {
			ticker := time.NewTicker(scheduleTickInterval)
			defer ticker.Stop()
			for range ticker.C {
				scheduleRunner.step(manager)
			}
		}()
	})
}

type dueSchedule struct {
	key      string
	pluginID string
	schedule ScheduleContribution
}

// step starts every due schedule and forgets schedules that are no longer eligible.
func (r *scheduleRunnerState) step(manager *pluginManager) {
	now := r.now()
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	seen := map[string]bool{}
	var due []dueSchedule
	r.mu.Lock()
	busyPlugins := map[string]bool{}
	for key, state := range r.states {
		if state.running {
			pluginID, _, _ := strings.Cut(key, "\x00")
			busyPlugins[pluginID] = true
		}
	}
	for _, record := range records {
		record.mu.Lock()
		eligible := record.builtin == nil && record.manifest != nil && !record.manifest.isDeclarative() &&
			record.contributionsListedLocked() && manager.workspaceActiveLocked(record)
		var schedules []ScheduleContribution
		if eligible {
			schedules = schedulesLocked(record)
		}
		pluginID := record.id
		record.mu.Unlock()
		for _, schedule := range schedules {
			key := pluginID + "\x00" + schedule.ID
			seen[key] = true
			interval := time.Duration(schedule.IntervalMinutes) * time.Minute
			state := r.states[key]
			if state == nil || state.action != schedule.Action || state.interval != interval {
				// New, re-enabled or changed: wait one full interval first.
				r.states[key] = &scheduleState{action: schedule.Action, interval: interval, next: now.Add(interval)}
				continue
			}
			if !state.running && !busyPlugins[pluginID] && !now.Before(state.next) {
				state.running = true
				busyPlugins[pluginID] = true
				due = append(due, dueSchedule{key: key, pluginID: pluginID, schedule: schedule})
			}
		}
	}
	for key, state := range r.states {
		if !seen[key] && !state.running {
			delete(r.states, key)
		}
	}
	r.mu.Unlock()
	for _, item := range due {
		r.wg.Add(1)
		go func(item dueSchedule) {
			defer r.wg.Done()
			r.run(manager, item, now)
		}(item)
	}
}

func scheduleRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "sched-" + hex.EncodeToString(b[:])
}

func (r *scheduleRunnerState) run(manager *pluginManager, item dueSchedule, firedAt time.Time) {
	payload, _ := json.Marshal(map[string]string{"schedule": item.schedule.ID, "firedAt": firedAt.UTC().Format(time.RFC3339)})
	ctx, cancel := context.WithTimeout(context.Background(), scheduleTimeout+5*time.Second)
	defer cancel()
	scheduleID, action := item.schedule.ID, item.schedule.Action
	_, err := r.invoke(manager, ctx, contributedActionCall{
		PluginID: item.pluginID, Point: schedulePointKey, EntryID: scheduleID, ActionID: action,
		RequestID: scheduleRequestID(), Payload: payload, Timeout: scheduleTimeout,
		MaxResultBytes: scheduleResultMaxBytes, Concurrency: scheduleConcurrency,
		Validate: func(record *pluginRecord) error {
			for _, current := range schedulesLocked(record) {
				if current.ID == scheduleID && current.Action == action {
					return nil
				}
			}
			return newContributionError(contributionErrNotFound, "schedule %q is no longer contributed", scheduleID)
		},
	})
	busy := contributionErrorCodeOf(err) == contributionErrBusy
	r.mu.Lock()
	state := r.states[item.key]
	var delay time.Duration
	if state != nil {
		state.running = false
		if busy {
			// Not a failure: try again on the next tick.
			delay = 0
		} else if err != nil {
			state.failures++
			delay = state.interval << min(state.failures, 10)
			if delay > scheduleBackoffMax || delay <= 0 {
				delay = max(scheduleBackoffMax, state.interval)
			}
		} else {
			state.failures = 0
			delay = state.interval
		}
		state.next = r.now().Add(delay)
	}
	r.mu.Unlock()
	if err != nil && !busy {
		if record, lookupErr := manager.record(item.pluginID); lookupErr == nil {
			record.appendLog("warn", fmt.Sprintf("schedule %s failed: %v; next attempt in %s", scheduleID, err, delay.Round(time.Minute)))
		}
	}
}

func contributionErrorCodeOf(err error) string {
	var coded *contributionError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
