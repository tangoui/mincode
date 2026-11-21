package cloudcompanion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
)

// QuotaTrust is provisioned independently of any HTTP response. Never populate
// it from a marketplace manifest, member API response, or browser settings.
type QuotaTrust struct {
	KeyID        string
	PublicKeyPEM []byte
}

// Build-time public trust root; never supply this from a member API response.
// Empty values leave entitlement display unavailable (not an authorization fallback).
var QuotaSnapshotKeyID string
var QuotaSnapshotPublicKeyBase64 string

type snapshotArgs struct {
	ExpectedAccountID int64 `json:"expectedAccountId"`
}

type snapshotDisplay struct {
	AccountID         int64               `json:"accountId"`
	Plan              string              `json:"plan"`
	ExpiresAt         *int64              `json:"expiresAt"`
	SnapshotExpiresAt int64               `json:"snapshotExpiresAt"`
	Limits            license.QuotaPolicy `json:"limits"`
}

type snapshotResponse struct {
	Status   string           `json:"status"`
	Message  string           `json:"message,omitempty"`
	Snapshot *snapshotDisplay `json:"snapshot,omitempty"`
}

func pinnedQuotaTrust() (QuotaTrust, error) {
	keyID, publicKeyBase64 := QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64
	if keyID == "" && publicKeyBase64 == "" {
		keyID, publicKeyBase64 = developmentQuotaTrust()
	}
	if keyID == "" || publicKeyBase64 == "" {
		return QuotaTrust{}, errors.New("配额签名公钥未配置")
	}
	publicKeyPEM, err := base64.StdEncoding.Strict().DecodeString(publicKeyBase64)
	if err != nil || base64.StdEncoding.EncodeToString(publicKeyPEM) != publicKeyBase64 {
		return QuotaTrust{}, errors.New("配额签名公钥配置无效")
	}
	if err := license.ValidateQuotaTrustRoot(keyID, publicKeyPEM); err != nil {
		return QuotaTrust{}, errors.New("配额签名公钥配置无效")
	}
	return QuotaTrust{KeyID: keyID, PublicKeyPEM: publicKeyPEM}, nil
}

func cloudEntitlementSnapshotHandler(_, argsJSON string) (string, error) {
	var args snapshotArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.ExpectedAccountID < 1 {
		return core.EncodeResult(snapshotResponse{Status: "signed_out"})
	}
	response := snapshotResponse{Status: "failed"}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return core.EncodeResult(snapshotResponse{Status: "signed_out"})
	}
	if err != nil {
		response.Message = fmt.Sprintf("读取云账号会话: %v", err)
		return core.EncodeResult(response)
	}
	if !isMemberSession(session) || session.AccountID != args.ExpectedAccountID {
		response.Message = "当前云账号已切换，请刷新后重试"
		return core.EncodeResult(response)
	}
	trust, err := pinnedQuotaTrust()
	if err != nil {
		return core.EncodeResult(snapshotResponse{Status: "unconfigured"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	verified, err := FetchMemberQuotaSnapshot(ctx, trust)
	if err != nil {
		response.Message = err.Error()
		return core.EncodeResult(response)
	}
	if verified.AccountID != args.ExpectedAccountID || !sameMemberSession(session) {
		response.Message = "云账号会话已切换，请重试"
		return core.EncodeResult(response)
	}
	return core.EncodeResult(snapshotResponse{Status: "verified", Snapshot: &snapshotDisplay{
		AccountID: verified.AccountID, Plan: verified.Plan, ExpiresAt: verified.ExpiresAt,
		SnapshotExpiresAt: verified.SnapshotExpiresAt, Limits: verified.Limits,
	}})
}

var redemptionCodePattern = regexp.MustCompile(`^[A-Z0-9-]{16,128}$`)
var redemptionGrantIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type redeemArgs struct {
	Code              string `json:"code"`
	ExpectedAccountID int64  `json:"expectedAccountId"`
}

type redeemResponse struct {
	GrantID string `json:"grant_id"`
}

func init() {
	core.Register("cloud_entitlement_redeem", cloudEntitlementRedeemHandler)
	core.Register("cloud_entitlement_snapshot", cloudEntitlementSnapshotHandler)
}

func cloudEntitlementRedeemHandler(_, argsJSON string) (string, error) {
	var args redeemArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if !redemptionCodePattern.MatchString(args.Code) {
		return "", errors.New("授权码须为 16–128 位大写字母、数字或连字符")
	}
	if args.ExpectedAccountID < 1 {
		return "", errors.New("请刷新当前云账号后再兑换")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	result, err := redeemMemberCode(ctx, args.Code, args.ExpectedAccountID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// A successful redemption only records a grant on the member account. It does
// not verify an entitlement, update the legacy license, or authorize features.
func redeemMemberCode(ctx context.Context, code string, expectedAccountID int64) (redeemResponse, error) {
	if !redemptionCodePattern.MatchString(code) {
		return redeemResponse{}, errors.New("授权码格式无效")
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return redeemResponse{}, errors.New("请先登录云账号")
	}
	if err != nil {
		return redeemResponse{}, fmt.Errorf("读取云账号会话: %w", err)
	}
	if !isMemberSession(session) || session.AccountID != expectedAccountID {
		return redeemResponse{}, errors.New("当前云账号已切换，请刷新后重试")
	}
	var result redeemResponse
	// A rejected token means the server did not process the redemption, so one retry is safe.
	if err := withMemberToken(ctx, session, func(token string) error {
		return cloudPost(ctx, session.APIBaseURL, "mincode/entitlement/redeem", url.Values{"code": {code}}, token, &result)
	}); err != nil {
		if message := redemptionRefusal(err); message != "" {
			return redeemResponse{}, errors.New(message)
		}
		return redeemResponse{}, fmt.Errorf("提交授权码兑换: %w", err)
	}
	if !sameMemberSession(session) {
		return redeemResponse{}, errors.New("兑换已提交，但云账号会话已切换；请在原账号核对记录")
	}
	if !redemptionGrantIDPattern.MatchString(result.GrantID) {
		return redeemResponse{}, errors.New("兑换已提交，但服务端未返回有效记录编号；请核对账号记录")
	}
	return result, nil
}

// redemptionReasons explains the cloud's fixed refusal reasons; unknown reasons keep the generic error.
var redemptionReasons = map[string]string{
	"invalid_format":      "授权码格式无效，请检查是否完整复制",
	"not_found":           "授权码不存在，请检查是否完整、准确地复制",
	"stopped":             "该授权码已被停止兑换，请联系发码方恢复或更换授权码",
	"exhausted":           "该授权码的兑换次数已用完",
	"expired":             "该授权码已过最晚兑换时间",
	"already_redeemed":    "当前账号已兑换过这个授权码，每个账号只能兑换一次",
	"product_unavailable": "授权码对应的授权商品已下架或停用，请联系发码方",
	"plan_unavailable":    "授权码对应套餐的配额策略未启用，请联系发码方",
}

func redemptionRefusal(err error) string {
	var refused *cloudRequestError
	if !errors.As(err, &refused) {
		return ""
	}
	var detail struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(refused.Data, &detail) != nil {
		return ""
	}
	if message, ok := redemptionReasons[detail.Reason]; ok {
		return "兑换失败：" + message
	}
	return ""
}

func sameMemberSession(session persistedSession) bool {
	current, err := readSession()
	return err == nil && isMemberSession(current) && current.AccountID == session.AccountID &&
		current.APIBaseURL == session.APIBaseURL && current.RefreshToken == session.RefreshToken
}

// FetchMemberQuotaSnapshot authenticates a current cloud member session
// and verifies the signed, account-bound snapshot. It does not register a
// device, cache limits, or authorize any resource/paid feature.
func FetchMemberQuotaSnapshot(ctx context.Context, trust QuotaTrust) (license.QuotaSnapshot, error) {
	if trust.KeyID == "" || len(trust.PublicKeyPEM) == 0 {
		return license.QuotaSnapshot{}, errors.New("配额签名公钥未配置")
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return license.QuotaSnapshot{}, errors.New("请先登录云账号")
	}
	if err != nil {
		return license.QuotaSnapshot{}, fmt.Errorf("读取云账号会话: %w", err)
	}
	if !isMemberSession(session) {
		return license.QuotaSnapshot{}, errors.New("当前会话不是云端会员账号")
	}
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return license.QuotaSnapshot{}, fmt.Errorf("读取本机设备标识: %w", err)
	}
	var envelope cloudEnvelope
	err = withMemberToken(ctx, session, func(token string) error {
		status, received, err := memberGet(ctx, session.APIBaseURL, "/api/mincode/entitlement/snapshot?"+memberDeviceQuery(identity).Encode(), token, 64*1024)
		if errors.Is(err, errMemberTokenRejected) {
			return err
		}
		if err != nil {
			return errors.New("获取配额快照失败，请稍后重试")
		}
		if status == http.StatusOK && received.Code != 1 {
			if limitErr := parseDeviceLimit(received.Data); limitErr != nil {
				return limitErr
			}
		}
		if status != http.StatusOK || received.Code != 1 || len(received.Data) == 0 {
			return errors.New("配额快照响应无效或会话已失效")
		}
		envelope = received
		return nil
	})
	if err != nil {
		return license.QuotaSnapshot{}, err
	}
	// A logout or account switch in flight must not attach the old response to
	// the new session. Compare the original credential without exposing it.
	current, err := readSession()
	if err != nil || !isMemberSession(current) || current.AccountID != session.AccountID ||
		current.APIBaseURL != session.APIBaseURL || current.RefreshToken != session.RefreshToken {
		return license.QuotaSnapshot{}, errors.New("云账号会话已切换，请重试")
	}
	verified, err := license.VerifyQuotaSnapshot(envelope.Data, time.Now(), trust.KeyID, trust.PublicKeyPEM, current.AccountID)
	if err != nil {
		return license.QuotaSnapshot{}, fmt.Errorf("验证配额快照: %w", err)
	}
	if !sameMemberSession(session) {
		return license.QuotaSnapshot{}, errors.New("云账号会话已切换，请重试")
	}
	return verified, nil
}
