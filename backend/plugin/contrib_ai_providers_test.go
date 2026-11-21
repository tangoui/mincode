package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func validAiProviderEntry() map[string]any {
	return map[string]any{
		"id":             "acme.zhipu",
		"label":          "Zhipu (Free)",
		"labelLocalized": map[string]string{"zh": "智谱（免费）"},
		"baseUrl":        "https://open.bigmodel.cn/api/paas/v4/",
		"apiFormat":      "openai",
		"model":          "GLM-4.7-Flash",
		"models": []any{
			map[string]any{"id": "GLM-4.7-Flash", "free": true},
			map[string]any{"id": "GLM-4V-Flash", "free": true, "tools": false},
		},
		"contextWindow":   128000,
		"maxInputTokens":  120000,
		"maxOutputTokens": 8192,
		"tier":            "free",
		"region":          "cn",
		"verifiedAt":      "2026-10-29",
		"quotaNote":       "Free models with no time limit",
		"signupUrl":       "https://open.bigmodel.cn/usercenter/apikeys",
	}
}

func decodeAiProviderForTest(t *testing.T, mutate func(map[string]any)) (AiProviderContribution, error) {
	t.Helper()
	entry := validAiProviderEntry()
	if mutate != nil {
		mutate(entry)
	}
	raw, _ := json.Marshal(entry)
	ctx := &pointContext{manifest: &Manifest{ID: "acme", APIVersion: 2}, spec: aiProviderSpec}
	value, _, err := decodeAiProviderEntry(ctx, 0, "", raw)
	if err != nil {
		return AiProviderContribution{}, err
	}
	return value.(AiProviderContribution), nil
}

func TestAiProviderDecode(t *testing.T) {
	provider, err := decodeAiProviderForTest(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != "acme.zhipu" || provider.Host != "open.bigmodel.cn" || provider.LabelLocalized["zh"] != "智谱（免费）" {
		t.Fatalf("provider = %+v", provider)
	}
	if len(provider.Models) != 2 || !provider.Models[0].Tools || provider.Models[1].Tools || !provider.Models[0].Free {
		t.Fatalf("models = %+v", provider.Models)
	}
	if provider.Placeholders == nil || len(provider.Placeholders) != 0 {
		t.Fatalf("placeholders = %#v, want empty non-nil", provider.Placeholders)
	}
	encoded, _ := json.Marshal(provider)
	for _, field := range []string{`"baseUrl"`, `"signupUrl"`, `"verifiedAt"`, `"placeholders":[]`, `"tools":false`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("encoded provider lacks %s: %s", field, encoded)
		}
	}
}

func TestAiProviderPlaceholderDecode(t *testing.T) {
	provider, err := decodeAiProviderForTest(t, func(entry map[string]any) {
		entry["baseUrl"] = "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1"
		entry["placeholders"] = []any{map[string]any{"name": "account_id", "label": "Account ID", "labelLocalized": map[string]string{"zh": "账号 ID"}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Host != "api.cloudflare.com" || len(provider.Placeholders) != 1 || provider.Placeholders[0].Name != "account_id" {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestAiProviderRejects(t *testing.T) {
	set := func(key string, value any) func(map[string]any) {
		return func(entry map[string]any) { entry[key] = value }
	}
	placeholder := func(name string) map[string]any { return map[string]any{"name": name, "label": name} }
	cases := map[string]func(map[string]any){
		"http base url": set("baseUrl", "http://open.bigmodel.cn/v4"),
		"userinfo":      set("baseUrl", "https://u:p@open.bigmodel.cn/v4"),
		"host placeholder": func(entry map[string]any) {
			entry["baseUrl"] = "https://{h}.example.com/v1"
			entry["placeholders"] = []any{placeholder("h")}
		},
		"undeclared placeholder": set("baseUrl", "https://api.example.com/{account_id}/v1"),
		"unused placeholder":     set("placeholders", []any{placeholder("account_id")}),
		"three placeholders": func(entry map[string]any) {
			entry["baseUrl"] = "https://api.example.com/{a}/{b}/{c}"
			entry["placeholders"] = []any{placeholder("a"), placeholder("b"), placeholder("c")}
		},
		"partner tier":           set("tier", "partner"),
		"unknown region":         set("region", "mars"),
		"unknown api format":     set("apiFormat", "gemini"),
		"model not listed":       set("model", "glm-5"),
		"no models":              set("models", []any{}),
		"limited without caveat": set("tier", "limited"),
		"http signup url":        set("signupUrl", "http://open.bigmodel.cn/keys"),
		"bidi label":             set("label", "Zhipu\u202e"),
		"foreign id":             set("id", "other.zhipu"),
		"bad verified date":      set("verifiedAt", "2026/10/29"),
		"unknown field":          set("partner", true),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAiProviderForTest(t, mutate)
			if err == nil {
				t.Fatal("expected an error")
			}
			t.Log(err)
		})
	}
}

func TestAiProviderLimitedWithCaveat(t *testing.T) {
	provider, err := decodeAiProviderForTest(t, func(entry map[string]any) {
		entry["tier"] = "limited"
		entry["caveat"] = "Requires real-name verification"
		entry["caveatLocalized"] = map[string]string{"zh": "需要实名认证"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Tier != "limited" || provider.CaveatLocalized["zh"] != "需要实名认证" {
		t.Fatalf("provider = %+v", provider)
	}
}
