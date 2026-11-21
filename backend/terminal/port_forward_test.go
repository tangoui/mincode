package terminal

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type testSOCKSDialer struct {
	address string
	peer    net.Conn
}

func (d *testSOCKSDialer) Dial(network, address string) (net.Conn, error) {
	d.address = address
	client, peer := net.Pipe()
	d.peer = peer
	return client, nil
}

func TestClassifyPortForwardError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "原始端口占用", err: syscall.EADDRINUSE, want: portForwardErrorAddressInUse},
		{name: "包装的端口占用", err: fmt.Errorf("监听失败: %w", syscall.EADDRINUSE), want: portForwardErrorAddressInUse},
		{name: "网络系统调用包装", err: fmt.Errorf("启动失败: %w", &net.OpError{
			Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE),
		}), want: portForwardErrorAddressInUse},
		{name: "连接被拒绝", err: syscall.ECONNREFUSED, want: portForwardErrorStartFailed},
		{name: "包装的连接被拒绝", err: fmt.Errorf("启动失败: %w", &net.OpError{
			Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
		}), want: portForwardErrorStartFailed},
		{name: "错误文字不作为错误码", err: fmt.Errorf("address already in use"), want: portForwardErrorStartFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPortForwardError(tc.err); got != tc.want {
				t.Fatalf("错误分类 = %q, 期望 %q: %v", got, tc.want, tc.err)
			}
		})
	}
}

func TestValidateDynamicForwardClearsUnusedTarget(t *testing.T) {
	rule, err := validatePortForwardRule(PortForwardRulePayload{
		ID: " proxy ", RuleType: "d", BindPort: 1080, TargetHost: "ignored", TargetPort: 443,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ID != "proxy" || rule.RuleType != "D" || rule.TargetHost != "" || rule.TargetPort != 0 {
		t.Fatalf("unexpected normalized rule: %#v", rule)
	}
}

func TestValidateForwardRequiresTargetOnlyForLocalAndRemote(t *testing.T) {
	for _, ruleType := range []string{"L", "R"} {
		_, err := validatePortForwardRule(PortForwardRulePayload{ID: ruleType, RuleType: ruleType, BindPort: 8080})
		if err == nil {
			t.Fatalf("expected missing target to fail for %s", ruleType)
		}
	}
}

func TestSOCKS5DomainConnectRelaysTraffic(t *testing.T) {
	client, server := net.Pipe()
	dialer := &testSOCKSDialer{}
	stop := make(chan struct{})
	go serveSOCKS5Connection(dialer, server, stop)
	defer close(stop)
	defer client.Close()

	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(client, methodReply); err != nil {
		t.Fatal(err)
	}
	if methodReply[0] != 5 || methodReply[1] != 0 {
		t.Fatalf("unexpected method reply: %v", methodReply)
	}

	host := []byte("example.com")
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = binary.BigEndian.AppendUint16(request, 443)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 || dialer.address != "example.com:443" {
		t.Fatalf("reply=%v address=%q", reply, dialer.address)
	}

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	remote := make([]byte, 4)
	if _, err := io.ReadFull(dialer.peer, remote); err != nil {
		t.Fatal(err)
	}
	if string(remote) != "ping" {
		t.Fatalf("unexpected relayed payload %q", remote)
	}
	_ = dialer.peer.Close()
}

func TestSOCKS5RejectsUnsupportedAuthentication(t *testing.T) {
	client, server := net.Pipe()
	go serveSOCKS5Connection(&testSOCKSDialer{}, server, make(chan struct{}))
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = client.Write([]byte{5, 1, 2})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0xff {
		t.Fatalf("expected no acceptable methods, got %v", reply)
	}
}

func TestConfiguredPortForwardKeepsShellUsableWhenAnotherPortIsOccupied(t *testing.T) {
	occupied := mustListenLoopback(t)
	defer occupied.Close()
	occupiedPort := uint16(occupied.Addr().(*net.TCPAddr).Port)
	availablePort := reserveAvailableLoopbackPort(t)

	rules := []PortForwardRulePayload{
		{ID: "available", RuleType: "L", BindPort: availablePort, TargetHost: "127.0.0.1", TargetPort: 22},
		{ID: "occupied", RuleType: "L", BindPort: occupiedPort, TargetHost: "127.0.0.1", TargetPort: 22},
	}
	proc := &shellProcess{
		sshClient:        &sshClientHandle{client: &ssh.Client{}},
		portForwardRules: append([]PortForwardRulePayload(nil), rules...),
	}

	failures := startConfiguredPortForwards(proc, rules)
	if len(failures) != 1 {
		t.Fatalf("expected one forwarding failure, got %d: %v", len(failures), failures)
	}
	if len(proc.portForwards) != 1 || proc.portForwards[0].ruleID != "available" {
		t.Fatalf("successful forwarding rule was not preserved: %#v", proc.portForwards)
	}

	statuses := snapshotPortForwardStatuses(proc)
	statusByID := make(map[string]PortForwardStatus, len(statuses))
	for _, status := range statuses {
		statusByID[status.RuleID] = status
	}
	if !statusByID["available"].IsRunning {
		t.Fatalf("available rule should be running: %#v", statusByID["available"])
	}
	occupiedStatus := statusByID["occupied"]
	if occupiedStatus.IsRunning || occupiedStatus.ErrorCode != portForwardErrorAddressInUse {
		t.Fatalf("occupied rule should be reported as failed: %#v", occupiedStatus)
	}

	if err := startPortForwards(proc, []PortForwardRulePayload{rules[1]}); err == nil {
		t.Fatal("manual retry should return the bind error while the port remains occupied")
	}

	cancelForwardHandles(proc.portForwards)
	proc.portForwards = nil
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(availablePort)))
	if err != nil {
		t.Fatalf("active forwarding listener was not released during cleanup: %v", err)
	}
	_ = listener.Close()
}

func mustListenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func reserveAvailableLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	listener := mustListenLoopback(t)
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestStopAndResumePortForwardSyncSessionRules(t *testing.T) {
	const sessionID = "port-forward-stop-resume-test"
	rule := PortForwardRulePayload{
		ID: "web", RuleType: "L", BindPort: reserveAvailableLoopbackPort(t), TargetHost: "127.0.0.1", TargetPort: 22,
	}
	other := PortForwardRulePayload{ID: "other", RuleType: "D", BindPort: 1080}
	proc := &shellProcess{
		sshClient:        &sshClientHandle{client: &ssh.Client{}},
		portForwardRules: []PortForwardRulePayload{rule, other},
	}
	sessions.put(sessionID, proc)
	defer sessions.remove(sessionID)
	defer func() {
		proc.mu.Lock()
		handles := proc.portForwards
		proc.portForwards = nil
		proc.mu.Unlock()
		cancelForwardHandles(handles)
	}()

	if failures := startConfiguredPortForwards(proc, []PortForwardRulePayload{rule}); len(failures) != 0 {
		t.Fatalf("start forward: %v", failures)
	}
	ruleIDs := func() []string {
		proc.mu.Lock()
		defer proc.mu.Unlock()
		ids := make([]string, 0, len(proc.portForwardRules))
		for _, r := range proc.portForwardRules {
			ids = append(ids, r.ID)
		}
		return ids
	}

	if _, err := cmdStopPortForward("", `{"payload":{"sessionId":"`+sessionID+`","ruleId":" web "}}`); err != nil {
		t.Fatal(err)
	}
	if ids := ruleIDs(); len(ids) != 1 || ids[0] != "other" {
		t.Fatalf("stopped rule must be removed from session rules, got %v", ids)
	}
	if len(proc.portForwards) != 0 {
		t.Fatalf("stopped forward still running: %#v", proc.portForwards)
	}

	resume := fmt.Sprintf(
		`{"payload":{"sessionId":%q,"rule":{"id":"web","type":"l","bindPort":%d,"targetHost":"127.0.0.1","targetPort":22}}}`,
		sessionID, rule.BindPort,
	)
	if _, err := cmdResumePortForward("", resume); err != nil {
		t.Fatal(err)
	}
	if ids := ruleIDs(); len(ids) != 2 || ids[0] != "other" || ids[1] != "web" {
		t.Fatalf("resumed rule must be restored to session rules, got %v", ids)
	}
	proc.mu.Lock()
	restored := proc.portForwardRules[1]
	running := len(proc.portForwards) == 1 && proc.portForwards[0].ruleID == "web"
	proc.mu.Unlock()
	if restored.RuleType != "L" || !running {
		t.Fatalf("resumed forward not normalized/running: rule=%#v running=%v", restored, running)
	}

	// 重复恢复不应产生重复规则。
	if _, err := cmdResumePortForward("", resume); err != nil {
		t.Fatal(err)
	}
	if ids := ruleIDs(); len(ids) != 2 {
		t.Fatalf("duplicate resume duplicated session rules: %v", ids)
	}
}

func TestResumePortForwardDuringGraceKeepsRuleForReconnect(t *testing.T) {
	proc := &shellProcess{}
	rule := PortForwardRulePayload{ID: "web", RuleType: "L", BindPort: 18080, TargetHost: "127.0.0.1", TargetPort: 80}
	if err := resumePortForward(proc, rule); err == nil {
		t.Fatal("resume without SSH client should report an error")
	}
	if len(proc.portForwardRules) != 1 || proc.portForwardRules[0].ID != "web" {
		t.Fatalf("rule must be kept so reconnect restores it: %#v", proc.portForwardRules)
	}
}
