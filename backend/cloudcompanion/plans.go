package cloudcompanion

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"syscall"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
)

// Public framework catalog: prices and limits are informational, never authorization.
type FrameworkPlan struct {
	Plan         string `json:"plan"`
	DisplayName  string `json:"display_name"`
	DurationDays *int64 `json:"duration_days"`
	PriceMinor   int64  `json:"price_minor"`
	// Nil means no discount; otherwise a list price above PriceMinor.
	OriginalPriceMinor *int64              `json:"original_price_minor"`
	Currency           string              `json:"currency"`
	VersionRange       string              `json:"version_range"`
	PolicyRevision     int64               `json:"policy_revision"`
	EffectiveAt        int64               `json:"effective_at"`
	Limits             license.QuotaPolicy `json:"limits"`
}

var planCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Pointer fields distinguish an explicitly configured zero (unlimited quota or
// free price) from a missing/null value in an untrusted catalog response.
type frameworkPlanWire struct {
	Plan               string          `json:"plan"`
	DisplayName        string          `json:"display_name"`
	DurationDays       json.RawMessage `json:"duration_days"`
	PriceMinor         *int64          `json:"price_minor"`
	OriginalPriceMinor *int64          `json:"original_price_minor"`
	Currency           string          `json:"currency"`
	VersionRange       string          `json:"version_range"`
	PolicyRevision     *int64          `json:"policy_revision"`
	EffectiveAt        *int64          `json:"effective_at"`
	Limits             *struct {
		MaxDevices             *int64 `json:"max_devices"`
		MaxActiveWorkspaces    *int64 `json:"max_active_workspaces"`
		MaxWindowsPerWorkspace *int64 `json:"max_windows_per_workspace"`
		MaxEnabledPlugins      *int64 `json:"max_enabled_plugins"`
		AllowPlugins           *bool  `json:"allow_plugins"`
	} `json:"limits"`
}

func validPlanLimit(value *int64) bool {
	return value != nil && *value >= 0 && *value <= 4294967295
}

func init() {
	core.Register("cloud_framework_plans", cloudFrameworkPlansHandler)
}

func cloudFrameworkPlansHandler(_, _ string) (string, error) {
	baseURL := ""
	session, err := readSession()
	if err == nil {
		if isMemberSession(session) {
			baseURL = session.APIBaseURL
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取云端目录配置: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	plans, err := fetchFrameworkPlans(ctx, baseURL)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(plans)
}

func frameworkPlansNetworkError(err error) error {
	var dnsErr *net.DNSError
	var certificateErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCertErr x509.CertificateInvalidError
	var networkErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkErr) && networkErr.Timeout():
		return errors.New("获取公开套餐超时，请检查云服务连接")
	case errors.As(err, &dnsErr):
		return errors.New("云服务域名无法解析，请检查网络或云服务地址")
	case errors.As(err, &certificateErr) || errors.As(err, &hostnameErr) || errors.As(err, &invalidCertErr):
		return errors.New("云服务 TLS 证书验证失败，请检查云服务证书")
	case errors.Is(err, syscall.ECONNRESET):
		return errors.New("云服务连接被重置，请检查服务器 HTTPS 配置或网络")
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("云服务拒绝连接，请检查服务是否已启动")
	default:
		return errors.New("无法连接云服务，请检查云服务地址、网络和 HTTPS 配置")
	}
}

func fetchFrameworkPlans(ctx context.Context, configuredURL string) ([]FrameworkPlan, error) {
	baseURL, err := cloudAPIBaseURL(configuredURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/mincode/frameworkPlans/index", nil)
	if err != nil {
		return nil, fmt.Errorf("创建公开套餐请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, frameworkPlansNetworkError(err)
	}
	defer res.Body.Close()
	raw, err := core.ReadAllLimited(res.Body, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("读取公开套餐: %w", err)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Items []frameworkPlanWire `json:"items"`
		} `json:"data"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &envelope) != nil || envelope.Code != 1 || envelope.Data.Items == nil || len(envelope.Data.Items) > 3 {
		return nil, errors.New("公开套餐响应无效")
	}
	seen := make(map[string]bool)
	plans := make([]FrameworkPlan, 0, len(envelope.Data.Items))
	for _, plan := range envelope.Data.Items {
		if (plan.Plan != "free" && plan.Plan != "annual" && plan.Plan != "perpetual") || seen[plan.Plan] ||
			strings.TrimSpace(plan.DisplayName) == "" || len(plan.DisplayName) > 120 ||
			plan.VersionRange == "" || len(plan.VersionRange) > 255 ||
			!planCurrencyPattern.MatchString(plan.Currency) || plan.PriceMinor == nil || *plan.PriceMinor < 0 || *plan.PriceMinor > 4294967295 ||
			(plan.Plan == "free" && *plan.PriceMinor != 0) || (plan.Plan != "free" && *plan.PriceMinor == 0) ||
			plan.PolicyRevision == nil || *plan.PolicyRevision < 1 || plan.EffectiveAt == nil || *plan.EffectiveAt < 1 ||
			plan.Limits == nil || !validPlanLimit(plan.Limits.MaxDevices) ||
			!validPlanLimit(plan.Limits.MaxActiveWorkspaces) || !validPlanLimit(plan.Limits.MaxWindowsPerWorkspace) ||
			(plan.Limits.MaxEnabledPlugins != nil && !validPlanLimit(plan.Limits.MaxEnabledPlugins)) ||
			plan.DurationDays == nil {
			return nil, errors.New("公开套餐字段无效")
		}
		var original *int64
		if plan.OriginalPriceMinor != nil && *plan.OriginalPriceMinor != *plan.PriceMinor {
			if plan.Plan == "free" || *plan.OriginalPriceMinor < *plan.PriceMinor || *plan.OriginalPriceMinor > 4294967295 {
				return nil, errors.New("公开套餐字段无效")
			}
			value := *plan.OriginalPriceMinor
			original = &value
		}
		var pluginLimit int64
		if plan.Limits.MaxEnabledPlugins != nil {
			pluginLimit = *plan.Limits.MaxEnabledPlugins
		}
		var duration *int64
		if string(plan.DurationDays) != "null" {
			var days int64
			if plan.Plan != "annual" || json.Unmarshal(plan.DurationDays, &days) != nil || days < 1 || days > 36500 {
				return nil, errors.New("公开套餐字段无效")
			}
			duration = &days
		} else if plan.Plan == "annual" {
			return nil, errors.New("公开套餐字段无效")
		}
		plans = append(plans, FrameworkPlan{
			Plan: plan.Plan, DisplayName: plan.DisplayName, DurationDays: duration,
			PriceMinor: *plan.PriceMinor, OriginalPriceMinor: original, Currency: plan.Currency, VersionRange: plan.VersionRange,
			PolicyRevision: *plan.PolicyRevision, EffectiveAt: *plan.EffectiveAt,
			Limits: license.QuotaPolicy{
				MaxDevices: *plan.Limits.MaxDevices, MaxActiveWorkspaces: *plan.Limits.MaxActiveWorkspaces,
				MaxWindowsPerWorkspace: *plan.Limits.MaxWindowsPerWorkspace, MaxEnabledPlugins: pluginLimit,
				PluginsDisabled: plan.Limits.AllowPlugins != nil && !*plan.Limits.AllowPlugins,
			},
		})
		seen[plan.Plan] = true
	}
	return plans, nil
}
