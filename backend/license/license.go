// License 域：旧版独立授权码（/api/license/pricing|activate|verify）已下线。
//
// MinCode Cloud 不再提供这些端点；Pro 状态完全来自已登录云账号的签名权益快照
// （cloud_entitlement_snapshot / cloud_quota_effective）。这里保留命令注册，
// 只为让旧前端或旧持久化数据调用时得到明确、不联网的结果，而不是网络错误：
//
//	license_pricing  {}                -> {version:"", updated_at:null, currency:"", plans:[], message, retired:true}
//	license_activate {licenseKey, ...} -> error（提示改用云账号）
//	license_verify   {licenseKey, ...} -> error（提示改用云账号；不会让本地记录失效）
package license

import (
	"errors"

	"mincode/app/backend/core"
)

func init() {
	core.Register("license_pricing", licensePricingHandler)
	core.Register("license_activate", licenseActivateHandler)
	core.Register("license_verify", licenseVerifyHandler)
}

// ErrLegacyLicenseRetired is returned by the retired license commands. It never
// reaches the network and never authorizes or revokes anything.
var ErrLegacyLicenseRetired = errors.New("独立授权码已停用：请在 设置 → 云账号 登录，Pro 权益以云账号为准")

type retiredPricingResult struct {
	Version   string   `json:"version"`
	UpdatedAt *int64   `json:"updated_at"`
	Currency  string   `json:"currency"`
	Plans     []string `json:"plans"`
	Message   string   `json:"message"`
	Retired   bool     `json:"retired"`
}

func licensePricingHandler(_, _ string) (string, error) {
	return core.EncodeResult(retiredPricingResult{Plans: []string{}, Message: ErrLegacyLicenseRetired.Error(), Retired: true})
}

func licenseActivateHandler(_, _ string) (string, error) {
	return "", ErrLegacyLicenseRetired
}

func licenseVerifyHandler(_, _ string) (string, error) {
	return "", ErrLegacyLicenseRetired
}
