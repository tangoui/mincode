package webautomation

import "mincode/app/backend/core"

func init() {
	core.Register("web_automation_list_drivers", listDriversHandler)
}

func listDriversHandler(_, _ string) (string, error) {
	return core.EncodeResult(Statuses())
}
