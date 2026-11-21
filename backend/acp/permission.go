package acp

import (
	"context"
	"fmt"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

const permissionTimeout = 10 * time.Minute

type permissionRequest struct {
	requestID string
	serverID  string
	sessionID string
	optionIDs map[string]struct{}
	outcome   chan acpsdk.RequestPermissionOutcome
}

// permissionTable 维护待用户响应的权限请求，并校验响应所属连接、会话和可选项。
type permissionTable struct {
	mu      sync.Mutex
	pending map[string]*permissionRequest
}

func newPermissionTable() *permissionTable {
	return &permissionTable{pending: map[string]*permissionRequest{}}
}

var permissions = newPermissionTable()

// register 创建一个待响应的权限请求，返回用于等待结果的 channel。
func (t *permissionTable) register(request permissionRequest) chan acpsdk.RequestPermissionOutcome {
	request.outcome = make(chan acpsdk.RequestPermissionOutcome, 1)
	t.mu.Lock()
	t.pending[request.requestID] = &request
	t.mu.Unlock()
	return request.outcome
}

// remove 清理一个已完成（或已放弃等待）的权限请求。
func (t *permissionTable) remove(requestID string) {
	t.mu.Lock()
	delete(t.pending, requestID)
	t.mu.Unlock()
}

// resolve 原子消费待响应请求，避免重放，并校验连接、会话与 optionId。
func (t *permissionTable) resolve(requestID, serverID, sessionID string, outcome acpsdk.RequestPermissionOutcome) error {
	t.mu.Lock()
	request, ok := t.pending[requestID]
	if !ok {
		t.mu.Unlock()
		return fmt.Errorf("permission request '%s' is not pending", requestID)
	}
	if request.serverID != serverID || request.sessionID != sessionID {
		t.mu.Unlock()
		return fmt.Errorf("permission request '%s' owner mismatch", requestID)
	}
	if outcome.Selected != nil {
		if _, ok := request.optionIDs[string(outcome.Selected.OptionId)]; !ok {
			t.mu.Unlock()
			return fmt.Errorf("permission option '%s' is not valid for request '%s'", outcome.Selected.OptionId, requestID)
		}
	}
	delete(t.pending, requestID)
	t.mu.Unlock()
	request.outcome <- outcome
	return nil
}

// resolvePermission 是包级入口，供 acp_respond_permission handler 调用。
func resolvePermission(requestID, serverID, sessionID string, outcome acpsdk.RequestPermissionOutcome) error {
	return permissions.resolve(requestID, serverID, sessionID, outcome)
}

// waitForPermission 等待已注册请求的用户响应、上下文取消或宿主超时。
func waitForPermission(ctx context.Context, requestID string, ch <-chan acpsdk.RequestPermissionOutcome) (acpsdk.RequestPermissionOutcome, error) {
	timer := time.NewTimer(permissionTimeout)
	defer func() {
		timer.Stop()
		permissions.remove(requestID)
	}()
	select {
	case outcome := <-ch:
		return outcome, nil
	case <-ctx.Done():
		return acpsdk.RequestPermissionOutcome{}, ctx.Err()
	case <-timer.C:
		return acpsdk.RequestPermissionOutcome{}, fmt.Errorf("permission request '%s' timed out", requestID)
	}
}
