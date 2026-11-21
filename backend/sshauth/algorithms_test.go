package sshauth

import (
	"errors"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAlgorithmSetDefaultsToModern(t *testing.T) {
	got := AlgorithmSet("")
	for _, insecure := range ssh.InsecureAlgorithms().KeyExchanges {
		if slices.Contains(got.KeyExchanges, insecure) {
			t.Fatalf("modern policy unexpectedly contains insecure KEX %q", insecure)
		}
	}
}

func TestAlgorithmSetLegacyExplicitlyAddsCompatibilityAlgorithms(t *testing.T) {
	got := AlgorithmSet("legacy")
	for _, insecure := range ssh.InsecureAlgorithms().KeyExchanges {
		if !slices.Contains(got.KeyExchanges, insecure) {
			t.Fatalf("legacy policy missing insecure KEX %q", insecure)
		}
	}
}

func TestIsNoCommonAlgorithmError(t *testing.T) {
	if IsNoCommonAlgorithmError(nil) {
		t.Fatal("nil error must not match")
	}
	matching := errors.New(`ssh: handshake failed: ssh: no common algorithm for host key; we offered: ["rsa-sha2-256"], peer offered: ["ssh-rsa"]`)
	if !IsNoCommonAlgorithmError(matching) {
		t.Fatal("negotiation failure must match")
	}
	if IsNoCommonAlgorithmError(errors.New("ssh: handshake failed: permission denied")) {
		t.Fatal("unrelated handshake failure must not match")
	}
}

func TestLegacyRetryConfigAppendsInsecureAlgorithms(t *testing.T) {
	modern := AlgorithmSet("")
	cfg := &ssh.ClientConfig{
		Config:            ssh.Config{KeyExchanges: modern.KeyExchanges, Ciphers: modern.Ciphers, MACs: modern.MACs},
		HostKeyAlgorithms: append([]string(nil), modern.HostKeys...),
	}
	retry, ok := LegacyRetryConfig(cfg)
	if !ok {
		t.Fatal("modern config should gain legacy algorithms")
	}
	insecure := ssh.InsecureAlgorithms()
	for _, algo := range insecure.HostKeys {
		if !slices.Contains(retry.HostKeyAlgorithms, algo) {
			t.Fatalf("retry config missing insecure host key %q", algo)
		}
	}
	for _, algo := range insecure.KeyExchanges {
		if !slices.Contains(retry.Config.KeyExchanges, algo) {
			t.Fatalf("retry config missing insecure KEX %q", algo)
		}
	}
	// 原配置不得被修改，避免影响同配置的其他在途连接。
	for _, algo := range insecure.HostKeys {
		if slices.Contains(cfg.HostKeyAlgorithms, algo) {
			t.Fatalf("original config mutated with insecure host key %q", algo)
		}
	}
}

func TestLegacyRetryConfigRejectsLegacyConfig(t *testing.T) {
	legacy := AlgorithmSet("legacy")
	cfg := &ssh.ClientConfig{
		Config:            ssh.Config{KeyExchanges: legacy.KeyExchanges, Ciphers: legacy.Ciphers, MACs: legacy.MACs},
		HostKeyAlgorithms: append([]string(nil), legacy.HostKeys...),
	}
	if retry, ok := LegacyRetryConfig(cfg); ok || retry != nil {
		t.Fatal("legacy config already contains compatibility algorithms; retry must be skipped")
	}
}
