package terminal

import (
	"fmt"
	"sync"
	"time"

	"mincode/app/backend/core"

	"golang.org/x/crypto/ssh"
)

const (
	maxBatchServers     = 100
	maxBatchConcurrency = 8
	maxBatchOutputBytes = 4 * 1024 * 1024
	batchExecTimeout    = 60 * time.Second
)

func init() { core.Register("ssh_batch_exec", cmdSSHBatchExec) }

func cmdSSHBatchExec(_, args string) (string, error) {
	var p struct {
		Servers []BatchExecServer `json:"servers"`
		Command string            `json:"command"`
	}
	if e := core.DecodeArgsPayload(args, &p); e != nil {
		return "", e
	}
	if len(p.Servers) > maxBatchServers {
		return "", fmt.Errorf("批量执行服务器数量超过上限(%d)", maxBatchServers)
	}
	results := make([]BatchExecResult, len(p.Servers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxBatchConcurrency)
	for i, s := range p.Servers {
		wg.Add(1)
		go func(i int, s BatchExecServer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = batchExecOne(s, p.Command)
		}(i, s)
	}
	wg.Wait()
	return core.EncodeResult(results)
}
func batchExecOne(server BatchExecServer, command string) BatchExecResult {
	start := time.Now()
	host, user := normalizeTerminalSSHHostUser(server.Host, server.Username)
	result := BatchExecResult{ID: server.ID, Host: fmt.Sprintf("%s@%s:%d", user, host, terminalSSHPort(server.Port)), DurationMs: 0}
	fail := func(e error) BatchExecResult {
		msg := e.Error()
		result.Error = &msg
		result.DurationMs = uint64(time.Since(start).Milliseconds())
		return result
	}
	client, jump, e := dialTerminalSSH(server.Host, server.Port, server.Username, server.AuthType, server.Password, server.KeyFile, server.Passphrase, nil, "__batch_"+server.ID, server.JumpHost, server.SocksProxy, server.AlgorithmPolicy)
	if e != nil {
		return fail(e)
	}
	defer client.Close()
	if jump != nil {
		defer jump.Close()
	}
	session, e := client.NewSession()
	if e != nil {
		return fail(e)
	}
	defer session.Close()
	stdout := core.NewCappedWriter(maxBatchOutputBytes)
	stderr := core.NewCappedWriter(maxBatchOutputBytes)
	session.Stdout = stdout
	session.Stderr = stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case e = <-done:
	case <-time.After(batchExecTimeout):
		_ = session.Close()
		_ = client.Close()
		e = <-done
		return fail(fmt.Errorf("命令执行超时(%s)", batchExecTimeout))
	}
	stdoutBytes, stdoutTruncated := stdout.Bytes()
	stderrBytes, stderrTruncated := stderr.Bytes()
	result.Stdout = string(stdoutBytes)
	result.Stderr = string(stderrBytes)
	if stdoutTruncated {
		result.Stdout += "\n... (output truncated)"
	}
	if stderrTruncated {
		result.Stderr += "\n... (output truncated)"
	}
	if e != nil {
		if exit, ok := e.(*ssh.ExitError); ok {
			code := exit.ExitStatus()
			result.ExitCode = &code
		} else {
			return fail(e)
		}
	} else {
		code := 0
		result.ExitCode = &code
	}
	result.DurationMs = uint64(time.Since(start).Milliseconds())
	return result
}
