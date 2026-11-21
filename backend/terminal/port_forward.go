package terminal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"

	"golang.org/x/crypto/ssh"
)

const socksHandshakeTimeout = 15 * time.Second

const (
	portForwardErrorAddressInUse = "address_in_use"
	portForwardErrorInvalidRule  = "invalid_rule"
	portForwardErrorStartFailed  = "start_failed"
)

type portForwardFailure struct {
	code    string
	message string
}

type sshDialer interface {
	Dial(string, string) (net.Conn, error)
}

type sshRemoteListener interface {
	Listen(string, string) (net.Listener, error)
}

func init() {
	core.Register("stop_port_forward", cmdStopPortForward)
	core.Register("resume_port_forward", cmdResumePortForward)
	core.Register("list_port_forwards", cmdListPortForwards)
}

func cmdStopPortForward(_, args string) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		RuleID    string `json:"ruleId"`
	}
	if e := core.DecodeArgsPayload(args, &p); e != nil {
		return "", e
	}
	proc, ok := sessions.get(p.SessionID)
	if ok {
		proc.portForwardMu.Lock()
		proc.mu.Lock()
		p.RuleID = strings.TrimSpace(p.RuleID)
		kept := proc.portForwards[:0]
		for _, h := range proc.portForwards {
			if h.ruleID == p.RuleID {
				h.cancel()
			} else {
				kept = append(kept, h)
			}
		}
		proc.portForwards = kept
		delete(proc.portForwardFailures, p.RuleID)
		// 同步移出会话规则，避免重连时已停止的转发被重新建立。
		proc.portForwardRules = removePortForwardRule(proc.portForwardRules, p.RuleID)
		proc.mu.Unlock()
		proc.portForwardMu.Unlock()
	}
	return core.EncodeResult(nil)
}

func cmdResumePortForward(_, args string) (string, error) {
	var p struct {
		SessionID string                 `json:"sessionId"`
		Rule      PortForwardRulePayload `json:"rule"`
	}
	if e := core.DecodeArgsPayload(args, &p); e != nil {
		return "", e
	}
	proc, ok := sessions.get(p.SessionID)
	if !ok {
		return "", fmt.Errorf("会话不存在")
	}
	if e := resumePortForward(proc, p.Rule); e != nil {
		return "", e
	}
	return core.EncodeResult(nil)
}

// resumePortForward 启动单条规则并写回会话规则，使重连后仍按用户意图恢复该转发。
// 启动失败（如端口占用）的有效规则同样保留，与会话启动时的配置规则语义一致。
func resumePortForward(proc *shellProcess, rawRule PortForwardRulePayload) error {
	proc.portForwardMu.Lock()
	defer proc.portForwardMu.Unlock()
	failures := startConfiguredPortForwardsLocked(proc, []PortForwardRulePayload{rawRule})
	if rule, err := validatePortForwardRule(rawRule); err == nil {
		proc.mu.Lock()
		proc.portForwardRules = append(removePortForwardRule(proc.portForwardRules, rule.ID), rule)
		proc.mu.Unlock()
	}
	return errors.Join(failures...)
}

// removePortForwardRule 原地移除指定 ID 的规则。
func removePortForwardRule(rules []PortForwardRulePayload, ruleID string) []PortForwardRulePayload {
	kept := rules[:0]
	for _, rule := range rules {
		if strings.TrimSpace(rule.ID) != ruleID {
			kept = append(kept, rule)
		}
	}
	return kept
}

func cmdListPortForwards(_, args string) (string, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if e := core.DecodeArgsPayload(args, &p); e != nil {
		return "", e
	}
	proc, ok := sessions.get(p.SessionID)
	if !ok {
		return core.EncodeResult([]PortForwardStatus{})
	}
	return core.EncodeResult(snapshotPortForwardStatuses(proc))
}

func snapshotPortForwardStatuses(proc *shellProcess) []PortForwardStatus {
	proc.mu.Lock()
	rules := append([]PortForwardRulePayload(nil), proc.portForwardRules...)
	handles := append([]portForwardHandle(nil), proc.portForwards...)
	failures := make(map[string]portForwardFailure, len(proc.portForwardFailures))
	for ruleID, failure := range proc.portForwardFailures {
		failures[ruleID] = failure
	}
	proc.mu.Unlock()

	running := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		running[handle.ruleID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(rules)+len(handles)+len(failures))
	out := make([]PortForwardStatus, 0, len(rules)+len(handles)+len(failures))
	appendStatus := func(ruleID string) {
		ruleID = strings.TrimSpace(ruleID)
		if ruleID == "" {
			return
		}
		if _, exists := seen[ruleID]; exists {
			return
		}
		seen[ruleID] = struct{}{}
		_, isRunning := running[ruleID]
		status := PortForwardStatus{RuleID: ruleID, IsRunning: isRunning}
		if failure, exists := failures[ruleID]; exists && !isRunning {
			status.ErrorCode = failure.code
			status.ErrorMessage = failure.message
		}
		out = append(out, status)
	}
	for _, rule := range rules {
		appendStatus(rule.ID)
	}
	for _, handle := range handles {
		appendStatus(handle.ruleID)
	}
	remainingFailures := make([]string, 0, len(failures))
	for ruleID := range failures {
		if _, exists := seen[ruleID]; !exists {
			remainingFailures = append(remainingFailures, ruleID)
		}
	}
	sort.Strings(remainingFailures)
	for _, ruleID := range remainingFailures {
		appendStatus(ruleID)
	}
	return out
}

// startPortForwards is used by explicit resume actions. It reports failures to the
// caller, while successful sibling rules remain active.
func startPortForwards(proc *shellProcess, rules []PortForwardRulePayload) error {
	return errors.Join(startConfiguredPortForwards(proc, rules)...)
}

// startConfiguredPortForwards starts each configured rule independently and records
// per-rule failures for the UI. SSH shell startup and reconnect use this best-effort
// path so an occupied forwarding port never tears down an otherwise healthy shell.
func startConfiguredPortForwards(proc *shellProcess, rules []PortForwardRulePayload) []error {
	proc.portForwardMu.Lock()
	defer proc.portForwardMu.Unlock()
	return startConfiguredPortForwardsLocked(proc, rules)
}

// restartSessionPortForwards 供重连使用：在 portForwardMu 内读取会话当前规则
// （已同步停止/恢复操作）并重新建立转发。
func restartSessionPortForwards(proc *shellProcess) []error {
	proc.portForwardMu.Lock()
	defer proc.portForwardMu.Unlock()
	proc.mu.Lock()
	rules := append([]PortForwardRulePayload(nil), proc.portForwardRules...)
	proc.mu.Unlock()
	return startConfiguredPortForwardsLocked(proc, rules)
}

// startConfiguredPortForwardsLocked 要求调用方持有 proc.portForwardMu。
func startConfiguredPortForwardsLocked(proc *shellProcess, rules []PortForwardRulePayload) []error {
	proc.mu.Lock()
	clientHandle := proc.sshClient
	running := make(map[string]struct{}, len(proc.portForwards))
	for _, handle := range proc.portForwards {
		running[handle.ruleID] = struct{}{}
	}
	proc.mu.Unlock()
	if clientHandle == nil || clientHandle.client == nil {
		return []error{fmt.Errorf("SSH 连接不存在")}
	}

	failures := make([]error, 0)
	for _, rawRule := range rules {
		rule, err := validatePortForwardRule(rawRule)
		if err != nil {
			ruleID := strings.TrimSpace(rawRule.ID)
			setPortForwardFailure(proc, ruleID, portForwardFailure{
				code: portForwardErrorInvalidRule, message: err.Error(),
			})
			failures = append(failures, err)
			continue
		}
		if _, exists := running[rule.ID]; exists {
			clearPortForwardFailure(proc, rule.ID)
			continue
		}
		handle, err := startPortForward(clientHandle.client, rule)
		if err != nil {
			wrapped := fmt.Errorf("启动端口转发规则 %s: %w", rule.ID, err)
			setPortForwardFailure(proc, rule.ID, portForwardFailure{
				code: classifyPortForwardError(err), message: err.Error(),
			})
			failures = append(failures, wrapped)
			continue
		}
		proc.mu.Lock()
		proc.portForwards = append(proc.portForwards, handle)
		delete(proc.portForwardFailures, rule.ID)
		proc.mu.Unlock()
		running[rule.ID] = struct{}{}
	}
	return failures
}

func setPortForwardFailure(proc *shellProcess, ruleID string, failure portForwardFailure) {
	if ruleID == "" {
		return
	}
	proc.mu.Lock()
	if proc.portForwardFailures == nil {
		proc.portForwardFailures = make(map[string]portForwardFailure)
	}
	proc.portForwardFailures[ruleID] = failure
	proc.mu.Unlock()
}

func clearPortForwardFailure(proc *shellProcess, ruleID string) {
	proc.mu.Lock()
	delete(proc.portForwardFailures, ruleID)
	proc.mu.Unlock()
}

func classifyPortForwardError(err error) string {
	if isPortForwardAddressInUse(err) {
		return portForwardErrorAddressInUse
	}
	return portForwardErrorStartFailed
}

func validatePortForwardRule(rule PortForwardRulePayload) (PortForwardRulePayload, error) {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.RuleType = strings.ToUpper(strings.TrimSpace(rule.RuleType))
	rule.TargetHost = strings.TrimSpace(rule.TargetHost)
	if rule.ID == "" || len(rule.ID) > 256 {
		return rule, fmt.Errorf("端口转发规则 ID 无效")
	}
	if rule.BindPort == 0 {
		return rule, fmt.Errorf("端口转发规则 %s 的监听端口不能为空", rule.ID)
	}
	switch rule.RuleType {
	case "D":
		// Dynamic forwarding resolves each destination from the SOCKS request.
		rule.TargetHost = ""
		rule.TargetPort = 0
	case "L", "R":
		if rule.TargetHost == "" || rule.TargetPort == 0 {
			return rule, fmt.Errorf("端口转发规则 %s 的目标地址和端口不能为空", rule.ID)
		}
	default:
		return rule, fmt.Errorf("未知端口转发类型: %s", rule.RuleType)
	}
	return rule, nil
}

func startPortForward(client *ssh.Client, rule PortForwardRulePayload) (portForwardHandle, error) {
	stop := make(chan struct{})
	var listener net.Listener
	var err error
	switch rule.RuleType {
	case "L", "D":
		// Deliberately keep local and dynamic forwarding loopback-only. Exposing a
		// SOCKS proxy on all interfaces would turn MinCode into an unintended proxy.
		listener, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(rule.BindPort)))
	case "R":
		listener, err = client.Listen("tcp", net.JoinHostPort("localhost", fmt.Sprint(rule.BindPort)))
	}
	if err != nil {
		return portForwardHandle{}, err
	}
	cancel := sync.OnceFunc(func() {
		close(stop)
		_ = listener.Close()
	})
	handle := portForwardHandle{ruleID: rule.ID, cancel: cancel}
	switch rule.RuleType {
	case "L":
		go runLocalForward(listener, client, rule, stop)
	case "R":
		go runRemoteForward(listener, rule, stop)
	case "D":
		go runDynamicForward(listener, client, stop)
	}
	return handle, nil
}

func cancelForwardHandles(handles []portForwardHandle) {
	for _, handle := range handles {
		if handle.cancel != nil {
			handle.cancel()
		}
	}
}

func runLocalForward(listener net.Listener, client sshDialer, rule PortForwardRulePayload, stop <-chan struct{}) {
	defer listener.Close()
	acceptForwardConnections(listener, stop, func(in net.Conn) {
		out, err := client.Dial("tcp", net.JoinHostPort(rule.TargetHost, fmt.Sprint(rule.TargetPort)))
		if err != nil {
			_ = in.Close()
			return
		}
		relayConnectionsUntilStopped(in, out, stop)
	})
}

func runRemoteForward(listener net.Listener, rule PortForwardRulePayload, stop <-chan struct{}) {
	defer listener.Close()
	acceptForwardConnections(listener, stop, func(in net.Conn) {
		out, err := net.Dial("tcp", net.JoinHostPort(rule.TargetHost, fmt.Sprint(rule.TargetPort)))
		if err != nil {
			_ = in.Close()
			return
		}
		relayConnectionsUntilStopped(in, out, stop)
	})
}

func runDynamicForward(listener net.Listener, client sshDialer, stop <-chan struct{}) {
	defer listener.Close()
	acceptForwardConnections(listener, stop, func(in net.Conn) {
		serveSOCKS5Connection(client, in, stop)
	})
}

func acceptForwardConnections(listener net.Listener, stop <-chan struct{}, handle func(net.Conn)) {
	for {
		in, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case <-stop:
			_ = in.Close()
			return
		default:
			go handle(in)
		}
	}
}

func serveSOCKS5Connection(client sshDialer, in net.Conn, stop <-chan struct{}) {
	defer in.Close()
	_ = in.SetDeadline(time.Now().Add(socksHandshakeTimeout))

	header := make([]byte, 2)
	if _, err := io.ReadFull(in, header); err != nil || header[0] != 5 || header[1] == 0 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(in, methods); err != nil {
		return
	}
	if !containsByte(methods, 0) {
		_, _ = in.Write([]byte{5, 0xff})
		return
	}
	if _, err := in.Write([]byte{5, 0}); err != nil {
		return
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(in, request); err != nil || request[0] != 5 {
		return
	}
	if request[1] != 1 {
		writeSOCKS5Reply(in, 7, nil)
		return
	}
	host, err := readSOCKS5Host(in, request[3])
	if err != nil {
		writeSOCKS5Reply(in, 8, nil)
		return
	}
	portBytes := make([]byte, 2)
	if _, err = io.ReadFull(in, portBytes); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBytes)
	if port == 0 {
		writeSOCKS5Reply(in, 4, nil)
		return
	}

	out, err := client.Dial("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		writeSOCKS5Reply(in, 5, nil)
		return
	}
	if err = writeSOCKS5Reply(in, 0, out.LocalAddr()); err != nil {
		_ = out.Close()
		return
	}
	_ = in.SetDeadline(time.Time{})
	relayConnectionsUntilStopped(in, out, stop)
}

func containsByte(values []byte, target byte) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func readSOCKS5Host(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(r, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(r, length); err != nil || length[0] == 0 {
			return "", errors.New("invalid SOCKS domain")
		}
		address := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, address); err != nil {
			return "", err
		}
		return string(address), nil
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(r, address); err != nil {
			return "", err
		}
		return net.IP(address).String(), nil
	default:
		return "", errors.New("unsupported SOCKS address type")
	}
}

func writeSOCKS5Reply(w io.Writer, reply byte, address net.Addr) error {
	ip := net.IPv4zero
	port := uint16(0)
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		if tcpAddress.IP != nil {
			ip = tcpAddress.IP
		}
		if tcpAddress.Port > 0 && tcpAddress.Port <= 65535 {
			port = uint16(tcpAddress.Port)
		}
	}
	response := []byte{5, reply, 0}
	if ipv4 := ip.To4(); ipv4 != nil {
		response = append(response, 1)
		response = append(response, ipv4...)
	} else {
		response = append(response, 4)
		response = append(response, ip.To16()...)
	}
	response = binary.BigEndian.AppendUint16(response, port)
	_, err := w.Write(response)
	return err
}

func relayConnectionsUntilStopped(a, b net.Conn, stop <-chan struct{}) {
	done := make(chan struct{})
	go func() {
		select {
		case <-stop:
			_ = a.Close()
			_ = b.Close()
		case <-done:
		}
	}()
	relayConnections(a, b)
	close(done)
}

func relayConnections(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
