package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// AccountProfile is display-only member data. Tokens returned alongside it by
// BuildAdmin are discarded, and the mobile number is masked before leaving Go.
type AccountProfile struct {
	AccountID     int64  `json:"accountId"`
	Username      string `json:"username"`
	Nickname      string `json:"nickname"`
	Email         string `json:"email"`
	Mobile        string `json:"mobile"`
	Motto         string `json:"motto"`
	Money         string `json:"money"`
	Score         int64  `json:"score"`
	JoinTime      int64  `json:"joinTime"`
	LastLoginTime int64  `json:"lastLoginTime"`
	LastLoginIP   string `json:"lastLoginIp"`
}

func init() {
	core.Register("cloud_account_profile", cloudAccountProfileHandler)
}

func cloudAccountProfileHandler(_, _ string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	profile, err := fetchMemberProfile(ctx)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(profile)
}

func fetchMemberProfile(ctx context.Context) (AccountProfile, error) {
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return AccountProfile{}, errors.New("请先登录云账号")
	}
	if err != nil {
		return AccountProfile{}, fmt.Errorf("读取云账号会话: %w", err)
	}
	if !isMemberSession(session) {
		return AccountProfile{}, errors.New("当前会话不是云端会员账号")
	}
	var envelope cloudEnvelope
	err = withMemberToken(ctx, session, func(token string) error {
		status, received, err := memberGet(ctx, session.APIBaseURL, "/api/index/index", token, 512*1024)
		if errors.Is(err, errMemberTokenRejected) {
			return err
		}
		if err != nil {
			return errors.New("获取账号资料失败，请稍后重试")
		}
		if status != http.StatusOK || received.Code != 1 {
			return errors.New("账号资料响应无效或会话已失效")
		}
		envelope = received
		return nil
	})
	if err != nil {
		return AccountProfile{}, err
	}
	var data struct {
		UserInfo map[string]json.RawMessage `json:"userInfo"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || len(data.UserInfo) == 0 {
		return AccountProfile{}, errors.New("账号资料响应缺少会员信息")
	}
	profile := parseMemberProfile(data.UserInfo)
	if profile.AccountID != session.AccountID {
		return AccountProfile{}, errors.New("账号资料与当前云账号不一致")
	}
	if !sameMemberSession(session) {
		return AccountProfile{}, errors.New("云账号会话已切换，请重试")
	}
	return profile, nil
}

// Only allowlisted fields are copied; token and refresh_token never leave this function.
func parseMemberProfile(info map[string]json.RawMessage) AccountProfile {
	return AccountProfile{
		AccountID:     jsonInt(info["id"]),
		Username:      jsonText(info["username"], 64),
		Nickname:      jsonText(info["nickname"], 64),
		Email:         jsonText(info["email"], 128),
		Mobile:        maskMobile(jsonText(info["mobile"], 32)),
		Motto:         jsonText(info["motto"], 255),
		Money:         jsonText(info["money"], 32),
		Score:         jsonInt(info["score"]),
		JoinTime:      jsonInt(info["join_time"]),
		LastLoginTime: jsonInt(info["last_login_time"]),
		LastLoginIP:   jsonText(info["last_login_ip"], 64),
	}
}

// jsonText accepts BuildAdmin's mixed string/number encodings and drops oversized or control-character values.
func jsonText(raw json.RawMessage, max int) string {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	var text string
	switch typed := value.(type) {
	case string:
		text = strings.TrimSpace(typed)
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
	if len(text) > max || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return text
}

func jsonInt(raw json.RawMessage) int64 {
	value, err := strconv.ParseInt(jsonText(raw, 20), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func maskMobile(mobile string) string {
	runes := []rune(mobile)
	if len(runes) < 7 {
		return mobile
	}
	return string(runes[:3]) + strings.Repeat("*", len(runes)-7) + string(runes[len(runes)-4:])
}
