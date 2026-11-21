package terminal

import (
	"path/filepath"
	"strings"
	"testing"
)

func feedAll(inputs ...string) []string {
	c := &sshInputCapture{}
	var out []string
	for _, in := range inputs {
		out = append(out, c.feed(in)...)
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSSHInputCaptureCommitAndEditing(t *testing.T) {
	// 退格修正后提交；回车后缓冲清空，下一条独立记录。
	got := feedAll("ls -l\x7f\x7f\r", "pwd\r")
	if !equalStrings(got, []string{"ls ", "pwd"}) {
		t.Fatalf("got %q", got)
	}
	// 多段输入拼接成一条命令；Ctrl+U 清行后重新键入。
	got = feedAll("git ", "sta", "\x15", "git status\r")
	if !equalStrings(got, []string{"git status"}) {
		t.Fatalf("got %q", got)
	}
	// UTF-8 命令原样保留。
	got = feedAll("echo 你好\r")
	if !equalStrings(got, []string{"echo 你好"}) {
		t.Fatalf("got %q", got)
	}
}

func TestSSHInputCaptureTaintedInputs(t *testing.T) {
	// Tab 补全：输入流只有前缀，提交时丢弃。
	if got := feedAll("git st\t\r"); len(got) != 0 {
		t.Fatalf("tab completion recorded %q", got)
	}
	// 方向键召回历史：召回文本不在输入流，提交时丢弃。
	if got := feedAll("\x1b[A\r"); len(got) != 0 {
		t.Fatalf("history recall recorded %q", got)
	}
	// 光标移动后编辑无法可靠还原，整行丢弃。
	if got := feedAll("ls\x1b[D -l\r"); len(got) != 0 {
		t.Fatalf("cursor move recorded %q", got)
	}
	// Ctrl+C 放弃当前命令。
	if got := feedAll("abort\x03\r"); len(got) != 0 {
		t.Fatalf("ctrl+c recorded %q", got)
	}
	// tainted 状态在提交后复位，后续正常行仍可记录。
	got := feedAll("git st\t\r", "ls\r")
	if !equalStrings(got, []string{"ls"}) {
		t.Fatalf("got %q", got)
	}
}

func TestSSHInputCapturePaste(t *testing.T) {
	// bracketed paste 标记本身不影响缓冲；单行粘贴正常提交。
	got := feedAll("\x1b[200~ls -l\x1b[201~\r")
	if !equalStrings(got, []string{"ls -l"}) {
		t.Fatalf("got %q", got)
	}
	// 多行粘贴按行提交：shell 对粘贴的多行本就是逐行执行，逐行记录才准确。
	got = feedAll("\x1b[200~ls\npwd\x1b[201~")
	if !equalStrings(got, []string{"ls", "pwd"}) {
		t.Fatalf("got %q", got)
	}
	for _, line := range got {
		if !safeHistoryCommand(line) {
			t.Fatalf("single-line paste must pass safe filter: %q", line)
		}
	}
	// 粘贴无尾换行后键入新命令不得与残留缓冲粘连。
	got = feedAll("\x1b[200~ls\x1b[201~", "whoami\r")
	if !equalStrings(got, []string{"ls", "whoami"}) {
		t.Fatalf("got %q", got)
	}
}

func TestRecordSSHCommandsFiltersAndPause(t *testing.T) {
	root := t.TempDir()
	p := sshHistoryPayload{ProfileID: "a", Host: "h", Port: 22, Username: "u"}
	// 敏感命令、前后空格命令被过滤；重复命令只保留一条。
	recordSSHCommandsAt(root, p, []string{"ls -l", "curl --password x", "  pad  ", "ls -l"})
	data, err := readSSHHistory(filepath.Join(root, "com.mincode.desktop", "ssh-command-history", historyKey(p)+".json"))
	if err != nil || len(data.Entries) != 1 || data.Entries[0].Command != "ls -l" {
		t.Fatalf("got %+v err=%v", data, err)
	}
	// 已暂停保存时不记录。
	data.Enabled = false
	path := filepath.Join(root, "com.mincode.desktop", "ssh-command-history", historyKey(p)+".json")
	if err := writeSSHHistory(path, data); err != nil {
		t.Fatal(err)
	}
	recordSSHCommandsAt(root, p, []string{"whoami"})
	data, err = readSSHHistory(path)
	if err != nil || len(data.Entries) != 1 {
		t.Fatalf("paused save must not record: %+v err=%v", data, err)
	}
	// 恢复保存后新命令合并，且保留既有收藏标记。
	data.Enabled = true
	data.Entries[0].Favorite = true
	if err := writeSSHHistory(path, data); err != nil {
		t.Fatal(err)
	}
	recordSSHCommandsAt(root, p, []string{"whoami"})
	data, err = readSSHHistory(path)
	if err != nil || len(data.Entries) != 2 || !data.Entries[1].Favorite || data.Entries[0].Command != "whoami" {
		t.Fatalf("got %+v err=%v", data, err)
	}
}

func TestWireOnDataCapturesSSHHistory(t *testing.T) {
	// wire/Streams 数据面的输入必须与 write_to_shell 一样静默记录 SSH 历史。
	root := t.TempDir()
	old := userDataDirFunc
	userDataDirFunc = func() string { return root }
	defer func() { userDataDirFunc = old }()

	p := sshHistoryPayload{ProfileID: "a", Host: "h", Port: 22, Username: "u"}
	proc := &shellProcess{
		sshHistoryProfileID: p.ProfileID, sshHistoryHost: p.Host,
		sshHistoryPort: p.Port, sshHistoryUser: p.Username,
		stdinTx: make(chan string, 1), stdinDone: make(chan struct{}),
	}
	sessions.put("wire-capture-test", proc)
	defer sessions.remove("wire-capture-test")

	wireHandlersForSession("wire-capture-test").onData("nihaoya\r")

	path := filepath.Join(root, "com.mincode.desktop", "ssh-command-history", historyKey(p)+".json")
	data, err := readSSHHistory(path)
	if err != nil || len(data.Entries) != 1 || data.Entries[0].Command != "nihaoya" {
		t.Fatalf("wire input must be recorded: %+v err=%v", data, err)
	}
}

func TestCaptureSSHInputSkipsNonSSHSessions(t *testing.T) {
	// 本地 shell：无 SSH 标识，不建立捕获状态。
	proc := &shellProcess{}
	captureSSHInput(proc, "ls\r")
	if proc.historyCapture != nil {
		t.Fatal("local shell must not capture")
	}
	// 派生会话（docker exec 等）：ephemeral 不记录。
	proc = &shellProcess{sshHistoryProfileID: "a", sshHistoryHost: "h", sshHistoryPort: 22, sshHistoryUser: "u", ephemeral: true}
	captureSSHInput(proc, "ls\r")
	if proc.historyCapture != nil {
		t.Fatal("ephemeral session must not capture")
	}
	// 缺少 profile 标识：无法与手动导入对齐存储，不记录。
	proc = &shellProcess{sshHistoryHost: "h", sshHistoryPort: 22, sshHistoryUser: "u"}
	captureSSHInput(proc, "ls\r")
	if proc.historyCapture != nil {
		t.Fatal("session without profile id must not capture")
	}
}

func TestSSHInputCaptureSkipsSecretPrompts(t *testing.T) {
	prompts := []string{
		"[sudo] password for u: ",
		"Password:",
		"Enter password: ",
		"Enter passphrase for key '/home/u/.ssh/id_ed25519': ",
		"请输入密码：",
		"\x1b[1mPassword:\x1b[0m ",
		"Verification code: ",
		"Enter PIN for 'token': ",
	}
	for _, prompt := range prompts {
		// 整段输入：远端停在密码提示符，回显关闭，整行不记录。
		c := &sshInputCapture{}
		c.observeOutput("u@h:~$ sudo ls\r\n" + prompt)
		if got := c.feed("hunter2\r"); len(got) != 0 {
			t.Fatalf("prompt %q recorded secret %q", prompt, got)
		}
		// 逐键输入同样不记录。
		c = &sshInputCapture{}
		c.observeOutput(prompt)
		var got []string
		for _, r := range "hunter2\r" {
			got = append(got, c.feed(string(r))...)
		}
		if len(got) != 0 {
			t.Fatalf("prompt %q recorded keystroke secret %q", prompt, got)
		}
	}
}

func TestSSHInputCaptureRecordsAfterSecretPrompt(t *testing.T) {
	c := &sshInputCapture{}
	c.observeOutput("Last login: Mon from 10.0.0.1\r\nu@h:~$ ")
	if got := c.feed("ls\r"); !equalStrings(got, []string{"ls"}) {
		t.Fatalf("normal prompt: got %q", got)
	}
	c.observeOutput("ls\r\nfile\r\nu@h:~$ ")
	c.feed("sudo -i\r")
	c.observeOutput("sudo -i\r\n[sudo] password for u: ")
	if got := c.feed("hunter2\r"); len(got) != 0 {
		t.Fatalf("secret recorded %q", got)
	}
	// 远端换行并回到普通提示符后，后续命令照常记录。
	c.observeOutput("\r\nroot@h:~# ")
	if got := c.feed("whoami\r"); !equalStrings(got, []string{"whoami"}) {
		t.Fatalf("after prompt: got %q", got)
	}
}

func TestSSHInputCaptureOutputTailBounded(t *testing.T) {
	c := &sshInputCapture{}
	c.observeOutput(strings.Repeat("x", 4*sshOutputTailLimit))
	if len(c.outputTail) != sshOutputTailLimit {
		t.Fatalf("tail len = %d", len(c.outputTail))
	}
	for i := 0; i < 100; i++ {
		c.observeOutput("yyyy")
	}
	if len(c.outputTail) > sshOutputTailLimit {
		t.Fatalf("tail grew to %d", len(c.outputTail))
	}
	// 截断后的长行仍以提示符结尾时照样识别。
	c.observeOutput("Password: ")
	if !c.atSecretPrompt() {
		t.Fatal("prompt after long line not detected")
	}
	c.observeOutput("\r\n")
	if len(c.outputTail) != 0 || c.atSecretPrompt() {
		t.Fatalf("newline must reset tail: %q", c.outputTail)
	}
}

func TestObserveSSHOutputSuppressesPasswordHistory(t *testing.T) {
	root := t.TempDir()
	old := userDataDirFunc
	userDataDirFunc = func() string { return root }
	defer func() { userDataDirFunc = old }()

	p := sshHistoryPayload{ProfileID: "a", Host: "h", Port: 22, Username: "u"}
	proc := &shellProcess{
		sshHistoryProfileID: p.ProfileID, sshHistoryHost: p.Host,
		sshHistoryPort: p.Port, sshHistoryUser: p.Username,
	}
	// 连接后首个输入即为密码（如登录脚本里的 sudo）：输出先到也要建立捕获状态。
	observeSSHOutput(proc, "[sudo] password for u: ")
	captureSSHInput(proc, "hunter2\r")
	observeSSHOutput(proc, "\r\nu@h:~$ ")
	captureSSHInput(proc, "whoami\r")

	path := filepath.Join(root, "com.mincode.desktop", "ssh-command-history", historyKey(p)+".json")
	data, err := readSSHHistory(path)
	if err != nil || len(data.Entries) != 1 || data.Entries[0].Command != "whoami" {
		t.Fatalf("password must not be recorded: %+v err=%v", data, err)
	}

	local := &shellProcess{}
	observeSSHOutput(local, "Password: ")
	if local.historyCapture != nil {
		t.Fatal("local shell must not track output")
	}
}
