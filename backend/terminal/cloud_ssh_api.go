package terminal

import "sync/atomic"

// StartCloudSSH 使用调用方已从本机安全存储恢复的 SSH Profile 创建会话。
// cloudcompanion 不接收来自手机的 Host、用户名或凭据。
func StartCloudSSH(payload StartSshShellPayload) error {
	payload.AttachIfExists = true
	_, err := startSSHShell(payload)
	return err
}

// CloudMFAPromptHandler 接收手机端发起的云 SSH 会话的验证码提示；返回 true 表示已转发给手机，
// 桌面界面不再弹出。返回 false（不是云会话、手机不在线等）时照常发给桌面界面。
type CloudMFAPromptHandler func(prompt SshMfaPromptPayload) bool

var cloudMFAPromptHandler atomic.Pointer[CloudMFAPromptHandler]

// SetCloudMFAPromptHandler 由 cloudcompanion 在启动时注册；传 nil 取消。
func SetCloudMFAPromptHandler(handler CloudMFAPromptHandler) {
	if handler == nil {
		cloudMFAPromptHandler.Store(nil)
		return
	}
	cloudMFAPromptHandler.Store(&handler)
}

func deliverCloudMFAPrompt(prompt SshMfaPromptPayload) bool {
	handler := cloudMFAPromptHandler.Load()
	return handler != nil && (*handler)(prompt)
}

// RespondCloudSSHMFA 把手机端输入的验证码交给正在等待的 SSH 认证；没有等待中的提示时返回 false。
func RespondCloudSSHMFA(sessionID string, answers []string) bool {
	return mfaChannels.respond(sessionID, answers)
}
