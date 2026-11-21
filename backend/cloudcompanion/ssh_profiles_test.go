package cloudcompanion

import (
	"testing"

	"mincode/app/backend/terminal"
)

func strPtr(value string) *string { return &value }

func TestMobileSSHUnsupportedReasonAllowsAdvancedProfiles(t *testing.T) {
	cases := []storedSSHProfile{
		{AuthType: "password", JumpHost: &terminal.JumpHostPayload{Host: "jump", AuthType: "password"}},
		{AuthType: "key", SocksProxy: &terminal.SocksProxyPayload{Host: "proxy", Port: 1080}},
		{AuthType: "agent", PortForwards: []terminal.PortForwardRulePayload{{}}},
		{AuthType: "password", PostConnectCommands: []string{"cd /srv"}},
		{AuthType: "keyboard-interactive"},
	}
	for i, profile := range cases {
		if reason := mobileSSHUnsupportedReason(profile); reason != "" {
			t.Fatalf("case %d should be supported on mobile, got %q", i, reason)
		}
	}
	if reason := mobileSSHUnsupportedReason(storedSSHProfile{AuthType: "gssapi"}); reason == "" {
		t.Fatal("unknown auth type must stay unsupported")
	}
}

func TestCloudSSHPayloadCarriesJumpSocksAndTOTPCredentials(t *testing.T) {
	profile := storedSSHProfile{
		ID:                  "p1",
		Host:                "10.0.0.5",
		Port:                22,
		Username:            "www",
		AuthType:            "keyboard-interactive",
		KeyFile:             "/keys/id",
		AlgorithmPolicy:     "compatible",
		DefaultPath:         "/srv",
		JumpHost:            &terminal.JumpHostPayload{Host: "jump", Port: 2222, Username: "ops", AuthType: "password"},
		SocksProxy:          &terminal.SocksProxyPayload{Host: "proxy", Port: 1080, Username: strPtr("u")},
		PortForwards:        []terminal.PortForwardRulePayload{{}},
		PostConnectCommands: []string{"cd /srv"},
	}
	credentials := map[string]string{
		"password":       "pw",
		"passphrase":     "pp",
		"totpUri":        "otpauth://totp/x?secret=ABC",
		"jumpPassword":   "jpw",
		"jumpPassphrase": "jpp",
		"socksPassword":  "spw",
	}

	payload := cloudSSHPayload(profile, credentials, "cloud-terminal-0123456789abcdef", 100, 30)

	if payload.JumpHost == nil || payload.JumpHost.Host != "jump" || payload.JumpHost.Port != 2222 {
		t.Fatalf("jump host not passed: %+v", payload.JumpHost)
	}
	if payload.JumpHost.Password == nil || *payload.JumpHost.Password != "jpw" ||
		payload.JumpHost.Passphrase == nil || *payload.JumpHost.Passphrase != "jpp" {
		t.Fatal("jump host credentials not filled from local storage")
	}
	if payload.JumpHost.AlgorithmPolicy != "compatible" {
		t.Fatalf("jump host should inherit the profile algorithm policy, got %q", payload.JumpHost.AlgorithmPolicy)
	}
	if payload.SocksProxy == nil || payload.SocksProxy.Password == nil || *payload.SocksProxy.Password != "spw" {
		t.Fatal("socks proxy password not filled")
	}
	if payload.TotpURI == nil || *payload.TotpURI != "otpauth://totp/x?secret=ABC" {
		t.Fatal("TOTP secret not passed, so MFA could not be answered automatically")
	}
	if len(payload.PortForwards) != 1 || len(payload.PostConnectCommands) != 1 {
		t.Fatal("port forwards / post-connect commands not passed")
	}
	if payload.Password == nil || *payload.Password != "pw" || payload.InitialDirectory != "/srv" {
		t.Fatal("base credentials or initial directory missing")
	}
	if *payload.Cols != 100 || *payload.Rows != 30 {
		t.Fatal("terminal size not passed")
	}
	// The stored profile must not be mutated by filling credentials.
	if profile.JumpHost.Password != nil || profile.SocksProxy.Password != nil {
		t.Fatal("stored profile was mutated")
	}
}
