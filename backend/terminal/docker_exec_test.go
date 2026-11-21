package terminal

import (
	"strings"
	"testing"
)

func TestBuildDockerExecCommandRejectsInvalidContainer(t *testing.T) {
	cases := []string{
		"web;rm -rf /",
		"web && curl evil.sh",
		"web$(id)",
		"-flag",
		"",
		"   ",
		"web container",
		"web|nc",
	}
	for _, containerID := range cases {
		if _, err := buildDockerExecCommand(containerID, "bash"); err == nil {
			t.Fatalf("容器 ID %q 应被拒绝", containerID)
		}
	}
}

func TestBuildDockerExecCommandRejectsShellOutsideAllowList(t *testing.T) {
	// 路径穿越是白名单存在的理由：这些串没有 shell 元字符，
	// 引号转义会原样放行，只有白名单能挡住。
	cases := []string{
		"/bin/../../evil",
		"/usr/local/bin/bash",
		"rm -rf /",
		"bash -c id",
		"python",
		"BASH",
	}
	for _, shell := range cases {
		if _, err := buildDockerExecCommand("nginx", shell); err == nil {
			t.Fatalf("shell %q 应被拒绝", shell)
		}
	}
}

func TestBuildDockerExecCommandAcceptsAllowedShells(t *testing.T) {
	for _, shell := range []string{"bash", "sh", "ash", "zsh", "fish"} {
		command, err := buildDockerExecCommand("nginx", shell)
		if err != nil {
			t.Fatalf("shell %q 应被接受: %v", shell, err)
		}
		if !strings.HasSuffix(command, " "+shell) {
			t.Fatalf("命令 %q 应以 shell %q 结尾", command, shell)
		}
	}
}

func TestBuildDockerExecCommandDefaultsToPortableShell(t *testing.T) {
	command, err := buildDockerExecCommand("nginx", "")
	if err != nil {
		t.Fatalf("空 shell 应回退到 sh: %v", err)
	}
	if command != "docker exec -it 'nginx' sh" {
		t.Fatalf("非预期命令: %q", command)
	}
}

func TestBuildDockerExecCommandQuotesContainerID(t *testing.T) {
	command, err := buildDockerExecCommand("my-app.web_1", "bash")
	if err != nil {
		t.Fatalf("合法容器名应被接受: %v", err)
	}
	if command != "docker exec -it 'my-app.web_1' bash" {
		t.Fatalf("非预期命令: %q", command)
	}
}

func TestBuildDockerLogStreamCommandClampsTail(t *testing.T) {
	cases := []struct {
		tail int
		want string
	}{
		{tail: 0, want: "docker logs -f --tail 200 'nginx'"},
		{tail: -5, want: "docker logs -f --tail 200 'nginx'"},
		{tail: 50, want: "docker logs -f --tail 50 'nginx'"},
		{tail: 99999, want: "docker logs -f --tail 2000 'nginx'"},
	}
	for _, tc := range cases {
		command, err := buildDockerLogStreamCommand("nginx", tc.tail)
		if err != nil {
			t.Fatalf("tail=%d 应被接受: %v", tc.tail, err)
		}
		if command != tc.want {
			t.Fatalf("tail=%d 得到 %q，期望 %q", tc.tail, command, tc.want)
		}
	}
}

func TestBuildDockerLogStreamCommandRejectsInvalidContainer(t *testing.T) {
	if _, err := buildDockerLogStreamCommand("web;id", 100); err == nil {
		t.Fatal("非法容器 ID 应被拒绝")
	}
}

// 派生会话必须拒绝复用父会话 ID：否则 publishStart 会用 ephemeral 子进程
// 顶替父会话，用户的 SSH 终端会被静默替换掉。
func TestStartDerivedSSHSessionRejectsParentSessionID(t *testing.T) {
	err := startDerivedSSHSession("same-id", "same-id", "docker ps", true, 80, 24)
	if err == nil {
		t.Fatal("父子会话 ID 相同时应报错")
	}
	if _, ok := sessions.get("same-id"); ok {
		t.Fatal("失败的派生会话不应留在会话表中")
	}
}

func TestStartDerivedSSHSessionRequiresSessionIDs(t *testing.T) {
	if err := startDerivedSSHSession("", "child", "docker ps", true, 80, 24); err == nil {
		t.Fatal("父会话 ID 为空时应报错")
	}
	if err := startDerivedSSHSession("parent", "  ", "docker ps", true, 80, 24); err == nil {
		t.Fatal("子会话 ID 为空时应报错")
	}
}

func TestStartDerivedSSHSessionRejectsExistingChildSession(t *testing.T) {
	const childID = "docker-test-existing-child"
	existing := &shellProcess{}
	sessions.put(childID, existing)
	t.Cleanup(func() {
		sessions.removeIfSame(childID, existing)
	})

	err := startDerivedSSHSession("parent", childID, "docker ps", true, 80, 24)
	if err == nil {
		t.Fatal("已存在的子会话 ID 应被拒绝")
	}
	current, ok := sessions.get(childID)
	if !ok || current != existing {
		t.Fatal("拒绝冲突 ID 时不应替换已有会话")
	}
}

// sharedClient 是派生会话不会拖垮父 SSH 终端的唯一保障。
func TestShutdownSharedClientProcessKeepsParentConnection(t *testing.T) {
	parent := &shellProcess{sshClient: &sshClientHandle{}, sshLinkState: sshLinkActive}
	child := &shellProcess{sshClient: parent.sshClient, sharedClient: true, ephemeral: true}

	shutdownShellProcess(child)

	if parent.sshClient == nil || parent.sshClient.client != nil {
		// client 为 nil 是本用例的前提（没有真实连接），这里断言父句柄未被摘除。
		t.Fatal("父会话的 SSH 句柄不应被派生会话清理")
	}
	if child.sshClient != nil {
		t.Fatal("派生会话应释放自身对 SSH 句柄的引用")
	}
}
