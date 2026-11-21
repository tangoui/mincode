package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func TestCSharpSDKVersions(t *testing.T) {
	for input, want := range map[string]bool{"8.0.426 [C:/sdk]": false, "10.0.402 [C:/sdk]": true, "11.0.100 [C:/sdk]": true, "10.0.100-preview [C:/sdk]": false, "": false} {
		if got := hasCSharpSDK(input); got != want {
			t.Fatalf("%q: got %v", input, got)
		}
	}
}

func TestWindowsCSharpStagedInstall(t *testing.T) {
	for _, scenario := range []string{"installed", "no-upgrade", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			sdkCalls, toolCalls, wingetCalls := 0, 0, 0
			lookup := func(name string) (string, error) { return name, nil }
			run := func(exe string, args ...string) (string, error) {
				if exe == "winget" {
					wingetCalls++
					return "no upgrade available\n", errors.New("no upgrade")
				}
				if args[0] == "--list-sdks" {
					sdkCalls++
					if scenario == "installed" || (scenario == "no-upgrade" && sdkCalls > 1) {
						return "10.0.402 [sdk]\n", nil
					}
					return "8.0.426 [sdk]\n", nil
				}
				if args[0] == "--list-runtimes" {
					return "Microsoft.NETCore.App 10.0.6 [runtime]\n", nil
				}
				toolCalls++
				if len(args) != 8 || strings.Join(args[:7], " ") != "tool update --global csharp-ls --version 0.28.0 --configfile" {
					t.Fatalf("unexpected tool command: %v", args)
				}
				config, err := os.ReadFile(args[7])
				if err != nil || !strings.Contains(string(config), `<clear/><add key="nuget.org" value="https://api.nuget.org/v3/index.json"`) {
					t.Fatalf("must use isolated active NuGet source: %s %v", config, err)
				}
				return "tool ready\n", nil
			}
			_, err := installWindowsCSharp(lookup, run)
			if scenario == "failed" {
				if err == nil || toolCalls != 0 {
					t.Fatalf("must stop before tool: %v calls=%d", err, toolCalls)
				}
			} else if err != nil || toolCalls != 1 {
				t.Fatalf("tool should run: %v calls=%d", err, toolCalls)
			}
			if scenario == "installed" && wingetCalls != 0 {
				t.Fatal("must skip installed SDK")
			}
		})
	}
}

func TestWindowsFSharpStagedInstall(t *testing.T) {
	for _, scenario := range []string{"existing", "newer-sdk", "no-upgrade", "missing-runtime", "tool-failed"} {
		t.Run(scenario, func(t *testing.T) {
			checks, tools, wingets := 0, 0, 0
			var configPath string
			run := func(exe string, args ...string) (string, error) {
				if exe == "winget" {
					wingets++
					if !strings.Contains(strings.Join(args, " "), "--id Microsoft.DotNet.SDK.8 --exact") {
						t.Fatalf("wrong SDK: %v", args)
					}
					return "no upgrade available", errors.New("no upgrade")
				}
				if args[0] == "--list-sdks" {
					checks++
					if scenario == "no-upgrade" && checks == 1 {
						return "", nil
					}
					if scenario == "newer-sdk" {
						return "10.0.402 [sdk]", nil
					}
					return "8.0.426 [sdk]", nil
				}
				if args[0] == "--list-runtimes" {
					if scenario == "newer-sdk" {
						return "Microsoft.NETCore.App 10.0.6 [runtime]", nil
					}
					if scenario == "missing-runtime" {
						return "Microsoft.NETCore.App 7.0.20 [runtime]", nil
					}
					return "Microsoft.NETCore.App 8.0.26 [runtime]", nil
				}
				tools++
				if strings.Join(args[:7], " ") != "tool update --global fsautocomplete --version 0.85.4 --configfile" {
					t.Fatalf("wrong tool: %v", args)
				}
				configPath = args[7]
				if scenario == "tool-failed" {
					return "feed unavailable", errors.New("network error")
				}
				return "installed", nil
			}
			_, err := installWindowsDotnetTool("fsautocomplete", "0.85.4", 8, func(name string) (string, error) { return name, nil }, run)
			wantFailure := scenario == "missing-runtime" || scenario == "tool-failed"
			if (err != nil) != wantFailure {
				t.Fatalf("unexpected error: %v", err)
			}
			if (scenario == "existing" || scenario == "newer-sdk") && wingets != 0 {
				t.Fatal("installed SDK must not call winget")
			}
			if scenario == "missing-runtime" && tools != 0 {
				t.Fatal("must not install unusable tool without its runtime")
			}
			if configPath != "" {
				if _, err := os.Stat(configPath); !os.IsNotExist(err) {
					t.Fatalf("temporary config not cleaned: %v", err)
				}
			}
		})
	}
}

// Explicitly opt in: this test installs tools, unlike normal machine smoke.
func TestMachineWindowsReportedLSPInstall(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("MINCODE_LSP_INSTALL_SMOKE") != "1" {
		t.Skip("native install verification is opt-in")
	}
	for _, id := range []string{"csharp-ls", "fsautocomplete", "dart-ls"} {
		t.Run(id, func(t *testing.T) {
			output, err := installHandler("", `{"serverId":"`+id+`"}`)
			t.Log(output)
			if err != nil || strings.Contains(output, `"error":`) {
				t.Fatalf("installation failed: %v", err)
			}
			definition, _ := definitionByID(id)
			status := executableStatus(definition)
			if !status.Installed {
				t.Fatalf("installed executable not detected: %+v", status)
			}
			t.Logf("DETECTED %s: %s", id, status.Executable)
			smokeHandshake(t, definition)
		})
	}
}

func TestWindowsDartPathCandidates(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths")
	}
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	dartBin := filepath.Join(base, "Microsoft", "WinGet", "Packages", "Google.DartSDK_test", "dart-sdk", "bin")
	if err := os.MkdirAll(dartBin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range core.WindowsToolPathCandidates() {
		if candidate == dartBin {
			return
		}
	}
	t.Fatal("fresh portable Dart install not detected without restarting GUI")
}

func TestWindowsLSPInstallDefinitions(t *testing.T) {
	dart, _ := definitionByID("dart-ls")
	if !strings.Contains(dart.InstallOS["windows"].InstallHint, "--id Google.DartSDK --exact") {
		t.Fatal("Dart must use the official verified winget package ID")
	}
	for _, id := range []string{"fsautocomplete", "csharp-ls"} {
		definition, _ := definitionByID(id)
		if definition.InstallOS["windows"].Prerequisite != "" || strings.Contains(definition.InstallOS["windows"].InstallHint, "&&") {
			t.Fatal("staged dotnet install must handle prerequisites itself")
		}
	}
	terraform, _ := definitionByID("terraform-ls")
	if !strings.Contains(terraform.InstallOS["windows"].InstallHint, "--id Hashicorp.TerraformLanguageServer --exact") || terraform.InstallOS["windows"].Prerequisite != "winget" {
		t.Fatal("Terraform must use the verified winget package ID")
	}
	java, _ := definitionByID("jdtls")
	if java.InstallOS["windows"].Installable || strings.Contains(java.InstallOS["windows"].InstallHint, "Eclipse.JDT.LanguageServer") {
		t.Fatal("unverified Java winget package offered")
	}
	lua, _ := definitionByID("lua-ls")
	if !strings.Contains(lua.InstallOS["windows"].InstallHint, "--id LuaLS.lua-language-server --exact") {
		t.Fatal("ambiguous Lua package")
	}
	r, _ := definitionByID("r-languageserver")
	if strings.Contains(r.InstallOS["windows"].InstallHint, "winget") || r.InstallOS["windows"].Prerequisite != "R" {
		t.Fatal("R package install must not reinstall runtime")
	}
	for _, required := range []string{"--vanilla", "R_LIBS_USER", "lib=lib", "requireNamespace", "quit(status=1)"} {
		if !strings.Contains(r.InstallOS["windows"].InstallHint, required) {
			t.Fatalf("R package install must use writable user library and validate success: %s", required)
		}
	}
}
