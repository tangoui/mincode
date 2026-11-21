// debugErrorText: what the debug UI shows for a failed host or DAP request.
// The host wraps an adapter's failure response as "dap <command> failed:
// <adapter text>" (backend/debug/session.go RequestError) and its
// own failures with a code ("timeout: …", "cancelled: …"; commands.go); the
// user sees the adapter's own text and localized copy for the codes.
import { describe, expect, it } from "vitest";
import { debugErrorText } from "@/domain/debug/errors";

describe("debugErrorText", () => {
  it("shows the adapter's own text without the host's request prefix", () => {
    expect(debugErrorText(new Error("dap restartFrame failed: Current stack frame doesn't support restart."))).toBe(
      "Current stack frame doesn't support restart.",
    );
    expect(debugErrorText(new Error("dap evaluate failed: NameError: name 'missing' is not defined"))).toBe(
      "NameError: name 'missing' is not defined",
    );
  });

  it("drops the host's context before an embedded request failure too", () => {
    expect(debugErrorText(new Error("debug begin: dap launch failed: Failed to launch debuggee VM."))).toBe("Failed to launch debuggee VM.");
    expect(debugErrorText(new Error("internal: debug begin: dap attach failed: Connection refused"))).toBe("Connection refused");
    // Only a whole `dap <command> failed: ` after a `: ` counts.
    expect(debugErrorText(new Error("internal: mydap x failed: y"))).toBe("internal: mydap x failed: y");
  });

  it("keeps an adapter text that itself mentions a failure", () => {
    expect(debugErrorText(new Error("dap setVariable failed: dap x failed: y"))).toBe("dap x failed: y");
  });

  it("localizes the host's timeout and cancellation codes", () => {
    expect(debugErrorText(new Error("timeout: dap evaluate did not respond within 15s"))).toMatch(
      /^(调试器没有及时响应，请稍后重试|The debugger did not respond in time\. Try again)$/,
    );
    expect(debugErrorText(new Error("cancelled: dap stackTrace: context canceled"))).toMatch(/^(请求已取消|The request was cancelled)$/);
  });

  it("keeps other host messages and non-Error values as they are", () => {
    expect(debugErrorText(new Error("unavailable: debug session ended"))).toBe("unavailable: debug session ended");
    expect(debugErrorText("forbidden: restartFrame")).toBe("forbidden: restartFrame");
  });
});
