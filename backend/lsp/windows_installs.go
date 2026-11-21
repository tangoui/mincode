package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
)

type lspInstallRunner func(string, ...string) (string, error)

func runLSPInstallCommand(executable string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = core.ExpandedEnv()
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return decodeInstallOutput([]byte(output.String())), err
}

func hasCSharpSDK(output string) bool { return hasDotnetSDK(output, 10) }

func hasDotnetSDK(output string, minimum int) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		major, err := strconv.Atoi(strings.SplitN(fields[0], ".", 2)[0])
		if err == nil && major >= minimum && !strings.Contains(fields[0], "-") {
			return true
		}
	}
	return false
}

func hasDotnetRuntime(output string, major int, allowNewer bool) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Microsoft.NETCore.App" || strings.Contains(fields[1], "-") {
			continue
		}
		installed, err := strconv.Atoi(strings.SplitN(fields[1], ".", 2)[0])
		if err == nil && (installed == major || (allowNewer && installed > major)) {
			return true
		}
	}
	return false
}

// Check each stage independently: winget's already-installed exit code is not
// success by itself. Only a verified SDK permits the following tool operation.
func installWindowsCSharp(lookup func(string) (string, error), run lspInstallRunner) (string, error) {
	return installWindowsDotnetTool("csharp-ls", "0.28.0", 10, lookup, run)
}

func installWindowsDotnetTool(tool, version string, minimumSDK int, lookup func(string) (string, error), run lspInstallRunner) (string, error) {
	var log strings.Builder
	appendLog := func(output string) {
		log.WriteString(output)
		if output != "" && !strings.HasSuffix(output, "\n") {
			log.WriteByte('\n')
		}
	}
	check := func() (string, bool) {
		dotnet, err := lookup("dotnet")
		if err != nil {
			return "", false
		}
		output, err := run(dotnet, "--list-sdks")
		appendLog(output)
		if err != nil || !hasDotnetSDK(output, minimumSDK) {
			return dotnet, false
		}
		runtimes, err := run(dotnet, "--list-runtimes")
		appendLog(runtimes)
		// FsAutoComplete 0.85.4 ships net8/net9/net10 assets with major
		// roll-forward; do not reinstall SDK 8 on an SDK/runtime 10 machine.
		return dotnet, err == nil && hasDotnetRuntime(runtimes, minimumSDK, tool == "fsautocomplete")
	}
	dotnet, ready := check()
	if !ready {
		winget, err := lookup("winget")
		if err != nil {
			return log.String(), err
		}
		output, installErr := run(winget, "install", "--id", fmt.Sprintf("Microsoft.DotNet.SDK.%d", minimumSDK), "--exact", "--source", "winget", "--no-upgrade", "--accept-source-agreements", "--accept-package-agreements", "--disable-interactivity")
		appendLog(output)
		// Rediscover: this process inherited PATH before the SDK installation.
		dotnet, ready = check()
		if !ready {
			return log.String(), fmt.Errorf(".NET %d+ SDK or the required runtime is still unavailable after installation (installer error: %v); tool installation was not attempted", minimumSDK, installErr)
		}
	}
	// Do not inherit empty/disabled feeds or package source mapping from the
	// workspace or user profile. Never alter the user's NuGet configuration.
	config, err := os.CreateTemp("", "mincode-lsp-nuget-*.config")
	if err != nil {
		return log.String(), fmt.Errorf("create temporary NuGet configuration: %w", err)
	}
	defer os.Remove(config.Name())
	_, writeErr := config.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<configuration><packageSources><clear/><add key="nuget.org" value="https://api.nuget.org/v3/index.json" /></packageSources></configuration>`)
	closeErr := config.Close()
	if writeErr != nil {
		return log.String(), writeErr
	}
	if closeErr != nil {
		return log.String(), closeErr
	}
	args := []string{"tool", "update", "--global", tool}
	if version != "" {
		args = append(args, "--version", version)
	}
	args = append(args, "--configfile", config.Name())
	appendLog("Installing " + tool + " using an isolated nuget.org source (existing NuGet settings are unchanged).")
	output, err := run(dotnet, args...)
	appendLog(output)
	if err != nil {
		return log.String(), fmt.Errorf("%s installation from nuget.org failed: %w", tool, err)
	}
	return log.String(), nil
}
