package sshauth

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	AlgorithmPolicyModern = "modern"
	AlgorithmPolicyLegacy = "legacy"
)

// AlgorithmSet returns an isolated algorithm set for a connection. Modern is
// the secure default; compatibility algorithms are appended only in legacy mode.
func AlgorithmSet(policy string) ssh.Algorithms {
	supported := ssh.SupportedAlgorithms()
	result := ssh.Algorithms{
		KeyExchanges:   append([]string(nil), supported.KeyExchanges...),
		Ciphers:        append([]string(nil), supported.Ciphers...),
		MACs:           append([]string(nil), supported.MACs...),
		HostKeys:       append([]string(nil), supported.HostKeys...),
		PublicKeyAuths: append([]string(nil), supported.PublicKeyAuths...),
	}
	if NormalizeAlgorithmPolicy(policy) == AlgorithmPolicyLegacy {
		insecure := ssh.InsecureAlgorithms()
		result.KeyExchanges = appendUnique(result.KeyExchanges, insecure.KeyExchanges...)
		result.Ciphers = appendUnique(result.Ciphers, insecure.Ciphers...)
		result.MACs = appendUnique(result.MACs, insecure.MACs...)
		result.HostKeys = appendUnique(result.HostKeys, insecure.HostKeys...)
		result.PublicKeyAuths = appendUnique(result.PublicKeyAuths, insecure.PublicKeyAuths...)
	}
	return result
}

func NormalizeAlgorithmPolicy(policy string) string {
	if strings.EqualFold(strings.TrimSpace(policy), AlgorithmPolicyLegacy) {
		return AlgorithmPolicyLegacy
	}
	return AlgorithmPolicyModern
}

// IsNoCommonAlgorithmError 判定握手是否因双方算法集无交集而失败。
// 此类失败在现代策略下可通过追加历史兼容算法重试。
func IsNoCommonAlgorithmError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "ssh: no common algorithm")
}

// LegacyRetryConfig 返回追加了历史兼容算法的配置副本，用于现代策略
// 协商失败后的自动重试。配置已包含全部兼容算法（legacy 策略）时返回
// ok=false，调用方不应做无意义的二次拨号。
func LegacyRetryConfig(cfg *ssh.ClientConfig) (*ssh.ClientConfig, bool) {
	insecure := ssh.InsecureAlgorithms()
	out := *cfg
	out.Config = ssh.Config{
		KeyExchanges: appendUnique(append([]string(nil), cfg.Config.KeyExchanges...), insecure.KeyExchanges...),
		Ciphers:      appendUnique(append([]string(nil), cfg.Config.Ciphers...), insecure.Ciphers...),
		MACs:         appendUnique(append([]string(nil), cfg.Config.MACs...), insecure.MACs...),
	}
	out.HostKeyAlgorithms = appendUnique(append([]string(nil), cfg.HostKeyAlgorithms...), insecure.HostKeys...)
	changed := len(out.Config.KeyExchanges) != len(cfg.Config.KeyExchanges) ||
		len(out.Config.Ciphers) != len(cfg.Config.Ciphers) ||
		len(out.Config.MACs) != len(cfg.Config.MACs) ||
		len(out.HostKeyAlgorithms) != len(cfg.HostKeyAlgorithms)
	if !changed {
		return nil, false
	}
	return &out, true
}

func appendUnique(base []string, values ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(values))
	for _, value := range base {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		base = append(base, value)
	}
	return base
}
