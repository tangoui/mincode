import type { AiCapability } from "@/domain/ai/capability-registry";
import {
  computerCaptureScreenSchema,
  computerOpenAppSchema,
  computerOpenURLSchema,
  desktopComputerUseBatchSchema,
  summarizeComputerUseActions,
} from "@/domain/ai/computer-use";
import { tool, type AiToolDependencies } from "./shared";

function permissionNames(input: Record<string, unknown>) {
  const requested: string[] = [];
  if (input.accessibility !== false) requested.push("Accessibility");
  if (input.screenCapture !== false) requested.push("Screen Recording");
  return requested.length ? requested.join(" and ") : "no desktop permissions";
}

export function computerCapabilities(dependencies: AiToolDependencies): AiCapability[] {
  return [
    {
      module: "computer",
      target: "none",
      discovery: true,
      tool: tool(
        "computer.get_status",
        "Read whether desktop Computer Use is supported on macOS or Windows and whether any platform permissions are ready. This does not prompt for permissions."
      ),
      mode: "read",
      risk: "read",
      execute: () => dependencies.invoke("computer_get_status", {}),
    },
    {
      module: "computer",
      target: "none",
      tool: tool("computer.request_permissions", "Request any platform permissions needed for desktop Computer Use. macOS may open System Settings; Windows normally reports ready without prompting.", {
        accessibility: { type: "boolean", description: "Request desktop input access when the platform requires it. Defaults to true." },
        screenCapture: { type: "boolean", description: "Request screen capture access when the platform requires it. Defaults to true." },
      }),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) => `Request ${permissionNames(input)} permission for MinCode`,
      execute: ({ input }) => dependencies.invoke("computer_request_permissions", input),
    },
    {
      module: "computer",
      target: "none",
      tool: tool(
        "computer.capture_screen",
        "Capture a macOS or Windows display as a PNG (the primary display unless `display` is set to an ID from computer.status). Coordinates in the returned screenshot define the coordinate frame for later computer.execute actions.",
        computerCaptureScreenSchema
      ),
      mode: "read",
      risk: "dangerous",
      approvalPreview: ({ input }) => typeof input.display === "number" && input.display > 0
        ? `Capture the current contents of desktop display ${input.display}`
        : "Capture the current contents of the primary desktop display",
      execute: ({ input }) => dependencies.invoke("computer_capture_screen", input),
    },
    {
      module: "computer",
      target: "none",
      tool: tool(
        "computer.open_app",
        "Launch or activate an installed macOS or Windows application by its application name or identifier. Use this instead of Spotlight, Start menu, taskbar, or Dock coordinates when the user explicitly asks to open a local desktop application. After activation, inspect the attached screenshot and continue with computer.execute only when visual interaction is needed.",
        computerOpenAppSchema,
        ["application"]
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) => {
        const application = typeof input.application === "string"
          ? input.application.trim()
          : "the requested application";
        return `Open local application ${application}`;
      },
      execute: ({ input }) => dependencies.invoke("computer_open_app", input),
    },
    {
      module: "computer",
      target: "none",
      tool: tool(
        "computer.open_url",
        "Open an absolute http or https URL in a named local desktop browser and activate that browser. Prefer this over Spotlight, mouse clicks, or address-bar keypresses when the user explicitly asks to open a URL in desktop Chrome, Safari (macOS), Firefox, Edge, or the default browser. The result includes an updated screenshot when Screen Recording permission is available.",
        computerOpenURLSchema,
        ["url"]
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) => {
        const browser = typeof input.browser === "string" && input.browser.trim()
          ? input.browser.trim()
          : "default browser";
        const url = typeof input.url === "string" ? input.url.trim() : "the requested URL";
        return `Open ${url} in local ${browser}`;
      },
      execute: ({ input }) => dependencies.invoke("computer_open_url", input),
    },
    {
      module: "computer",
      target: "none",
      tool: tool(
        "computer.execute",
        "Run a small approved batch of mouse or keyboard actions on the macOS or Windows display shown in the latest screenshot. Call computer.capture_screen first, use screenshot pixel coordinates, keep batches small, and inspect the attached updated screenshot after each batch. key_down/key_up hold keys across actions (held keys are released when the batch ends) and cursor_position reports the pointer in screenshot coordinates. Prefer computer.open_app for launching an installed local application and computer.open_url for opening a URL in a local browser. Never guess taskbar or Dock coordinates or type into an unverified window.",
        desktopComputerUseBatchSchema,
        ["actions"]
      ),
      mode: "write",
      risk: "dangerous",
      // Each batch has new coordinates and keys; a parameter-bound grant would equal "allow once".
      sessionGrant: "tool",
      approvalPreview: ({ input }) => {
        const summary = summarizeComputerUseActions(input.actions);
        const labels = summary.labels.slice(0, 5).join(", ");
        return summary.count
          ? `Run ${summary.count} desktop Computer Use action${summary.count === 1 ? "" : "s"}${labels ? `: ${labels}` : ""}`
          : "Run desktop Computer Use actions";
      },
      execute: ({ input }) => dependencies.invoke("computer_execute", input),
    },
  ];
}
