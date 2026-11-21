//go:build !windows

// e2e-sshd is a disposable, loopback-only SSH server for the desktop smoke
// harness (tools/e2e/desktop-smoke.mjs). It accepts one user with a password
// supplied through the environment, serves PTY shells, exec and the SFTP
// subsystem from a sandbox directory, and is never part of the application.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// outputDelay, from E2E_SSHD_OUTPUT_DELAY_MS, holds back PTY output on its
// way to the client to simulate a laggy link. Zero keeps the direct copy.
var outputDelay time.Duration

func parseOutputDelay(value string) time.Duration {
	if value == "" {
		return 0
	}
	ms, err := strconv.Atoi(value)
	if err != nil || ms < 0 {
		fmt.Fprintln(os.Stderr, "E2E_SSHD_OUTPUT_DELAY_MS must be a non-negative integer")
		os.Exit(2)
	}
	return time.Duration(ms) * time.Millisecond
}

type readyInfo struct {
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback address to listen on")
	user := flag.String("user", "mincode-e2e", "accepted user name")
	home := flag.String("home", "", "sandbox directory used as HOME and working directory")
	readyFile := flag.String("ready-file", "", "file receiving {port,fingerprint} once listening")
	flag.Parse()
	password := os.Getenv("E2E_SSHD_PASSWORD")
	outputDelay = parseOutputDelay(os.Getenv("E2E_SSHD_OUTPUT_DELAY_MS"))
	if password == "" || *home == "" || *readyFile == "" {
		fmt.Fprintln(os.Stderr, "E2E_SSHD_PASSWORD, -home and -ready-file are required")
		os.Exit(2)
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "-listen must be a loopback address")
		os.Exit(2)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		log.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if meta.User() == *user && subtle.ConstantTimeCompare(given, []byte(password)) == 1 {
				return nil, nil
			}
			return nil, errors.New("access denied")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	info, _ := json.Marshal(readyInfo{Port: listener.Addr().(*net.TCPAddr).Port, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())})
	if err := os.WriteFile(*readyFile, info, 0o600); err != nil {
		log.Fatal(err)
	}
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		<-signals
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go serveConn(conn, config, *home)
	}
}

func serveConn(conn net.Conn, config *ssh.ServerConfig, home string) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	go func() {
		for request := range requests {
			if request.WantReply {
				_ = request.Reply(request.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go serveSession(channel, channelRequests, home)
	}
}

type session struct {
	channel ssh.Channel
	home    string
	mu      sync.Mutex
	term    string
	size    *pty.Winsize
	tty     *os.File
	started bool
}

func serveSession(channel ssh.Channel, requests <-chan *ssh.Request, home string) {
	current := &session{channel: channel, home: home}
	defer channel.Close()
	for request := range requests {
		ok := current.handle(request)
		if request.WantReply {
			_ = request.Reply(ok, nil)
		}
	}
}

func (current *session) handle(request *ssh.Request) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	switch request.Type {
	case "pty-req":
		var payload struct {
			Term          string
			Columns, Rows uint32
			Width, Height uint32
			Modes         string
		}
		if ssh.Unmarshal(request.Payload, &payload) != nil {
			return false
		}
		current.term = payload.Term
		current.size = &pty.Winsize{Cols: uint16(payload.Columns), Rows: uint16(payload.Rows)}
		return true
	case "window-change":
		var payload struct{ Columns, Rows, Width, Height uint32 }
		if ssh.Unmarshal(request.Payload, &payload) != nil {
			return false
		}
		current.size = &pty.Winsize{Cols: uint16(payload.Columns), Rows: uint16(payload.Rows)}
		if current.tty != nil {
			_ = pty.Setsize(current.tty, current.size)
		}
		return true
	case "env":
		return true
	case "shell":
		return current.start(exec.Command("/bin/sh", "-i"))
	case "exec":
		var payload struct{ Command string }
		if ssh.Unmarshal(request.Payload, &payload) != nil {
			return false
		}
		return current.start(exec.Command("/bin/sh", "-c", payload.Command))
	case "subsystem":
		var payload struct{ Name string }
		if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" || current.started {
			return false
		}
		current.started = true
		server, err := sftp.NewServer(current.channel, sftp.WithServerWorkingDirectory(current.home))
		if err != nil {
			return false
		}
		go func() {
			_ = server.Serve()
			_ = current.channel.Close()
		}()
		return true
	}
	return false
}

// start runs command attached to a PTY when one was requested, else to pipes.
func (current *session) start(command *exec.Cmd) bool {
	if current.started {
		return false
	}
	current.started = true
	term := current.term
	if term == "" {
		term = "xterm"
	}
	command.Dir = current.home
	command.Env = []string{
		"HOME=" + current.home, "USER=mincode-e2e", "SHELL=/bin/sh", "TERM=" + term,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "PS1=e2e$ ", "LANG=C.UTF-8",
	}
	if current.size != nil {
		tty, err := pty.StartWithSize(command, current.size)
		if err != nil {
			return false
		}
		current.tty = tty
		go func() { _, _ = io.Copy(tty, current.channel) }()
		go func() {
			if outputDelay > 0 {
				delayed := newDelayedWriter(current.channel, outputDelay)
				_, _ = io.Copy(delayed, tty)
				_ = delayed.Close()
			} else {
				_, _ = io.Copy(current.channel, tty)
			}
			current.finish(command)
		}()
		return true
	}
	command.Stdout = current.channel
	command.Stderr = current.channel.Stderr()
	// A pipe, not the channel itself: Wait would otherwise block until the
	// client closes its side even after the command exited.
	stdin, err := command.StdinPipe()
	if err != nil {
		return false
	}
	if err := command.Start(); err != nil {
		return false
	}
	go func() {
		_, _ = io.Copy(stdin, current.channel)
		_ = stdin.Close()
	}()
	go current.finish(command)
	return true
}

func (current *session) finish(command *exec.Cmd) {
	status := 0
	if err := command.Wait(); err != nil {
		var exitErr *exec.ExitError
		status = 1
		if errors.As(err, &exitErr) {
			status = exitErr.ExitCode()
		}
	}
	_, _ = current.channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
	_ = current.channel.Close()
	current.mu.Lock()
	if current.tty != nil {
		_ = current.tty.Close()
	}
	current.mu.Unlock()
}
