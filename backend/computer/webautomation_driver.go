package computer

import (
	"fmt"

	"mincode/app/backend/webautomation"
)

type computerUseDriver struct{}

func (computerUseDriver) ID() string { return webautomation.DefaultDriverID }

func (computerUseDriver) Status() webautomation.DriverStatus {
	status := platformStatus()
	message := ""
	if !status.Supported {
		message = fmt.Sprintf("desktop Computer Use is not supported on %s", status.Platform)
	}
	return webautomation.DriverStatus{
		ID:        webautomation.DefaultDriverID,
		Name:      "Computer Use",
		Available: status.Supported,
		Platform:  status.Platform,
		Capabilities: []string{
			"open-url",
			"open-app",
			"capture-screen",
			"pointer",
			"keyboard",
		},
		Message: message,
	}
}

func (computerUseDriver) Execute(toolName string, arguments map[string]any) (any, error) {
	return ExecuteAITool(toolName, arguments)
}

func init() {
	if err := webautomation.Register(computerUseDriver{}); err != nil {
		panic(err)
	}
}
