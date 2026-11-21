// Opt-in native performance probe for the HTTP workspace (-tags e2e builds only).
// The harness serves JSON bodies of several sizes on localhost; the probe sends them through
// the real address bar (typed URL + Enter) and measures response rendering and scrolling.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { useWorkbenchStore } from "@/stores/workbench";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";

function visible<T extends HTMLElement>(selector: string) {
  return [...document.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

export async function perfHttpScenario(config: SmokeConfig, log: PerfLog) {
  const fixture = config.perfHttp!;
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const workbench = useWorkbenchStore();
  const tab = workbench.openView("http");
  if (!tab) throw new Error("Could not open HTTP workspace");

  const send = async (url: string) => {
    const input = await until(() => visible<HTMLInputElement>(".http-url-input input"), "address bar", wait);
    input.value = url;
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await nextFrame();
    input.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", code: "Enter", bubbles: true }));
  };
  // The body renders only the rows in view (VirtualTextViewer): read the text through the
  // viewer's scroller, scrolled to the end where each fixture puts its marker.
  const bodyScroller = () => visible<HTMLElement>(".http-response-body .app-virtual-text__viewport");
  // Rows that continue a wrapped line (no line number) join without a line break.
  const renderedText = () => [...document.querySelectorAll<HTMLElement>(".http-response-body .app-virtual-text__row")]
    .map((row, i) => (i > 0 && !row.querySelector(".app-virtual-text__number")?.textContent ? "" : i > 0 ? "\n" : "") + (row.querySelector(".app-virtual-text__text")?.textContent ?? ""))
    .join("");
  const renderedRows = () => document.querySelectorAll(".http-response-body .app-virtual-text__row").length;
  const tailText = async () => {
    const element = bodyScroller();
    if (!element) return "";
    element.scrollTop = element.scrollHeight;
    element.dispatchEvent(new Event("scroll"));
    await nextFrame();
    await nextFrame();
    return renderedText();
  };
  const headText = async () => {
    const element = bodyScroller();
    if (!element) return "";
    element.scrollTop = 0;
    element.dispatchEvent(new Event("scroll"));
    await nextFrame();
    await nextFrame();
    return renderedText();
  };
  const untilAsync = async (read: () => Promise<boolean>, label: string, timeoutMs: number) => {
    const end = performance.now() + timeoutMs;
    while (performance.now() < end) {
      if (await read()) return;
      await nextFrame();
    }
    throw new Error(`Timed out waiting for ${label}`);
  };

  try {
    await measure("进入 HTTP 页", async () => {
      await router.push({ name: "http" });
      await until(() => visible(".http-url-input input"), "address bar", wait);
    });
    for (const { label, path, marker } of fixture.bodies) {
      await measure(`发送并显示响应：${label}`, async () => {
        await send(`${fixture.baseUrl}${path}`);
        await untilAsync(async () => (await tailText()).includes(marker), `${label} response`, wait * 2);
        await headText();
        log.done(`HTTP-BODY ${JSON.stringify({ label, renderedRows: renderedRows(), scrollHeight: bodyScroller()?.scrollHeight ?? 0 })}`);
      });
      await measure(`滚动响应体：${label}`, async () => {
        const element = await until(bodyScroller, "response body scroller", wait);
        const span = element.scrollHeight - element.clientHeight;
        if (span <= 0 && label !== fixture.bodies[0].label) throw new Error(`${label}: nothing to scroll`);
        for (let i = 0; i <= 40; i++) { element.scrollTop = (span * i) / 40; await nextFrame(); }
        for (let i = 40; i >= 0; i--) { element.scrollTop = (span * i) / 40; await nextFrame(); }
      });
    }
    // A large JSON body is shown raw; the user can format it on demand.
    const large = fixture.bodies[fixture.bodies.length - 1];
    await measure(`手动格式化大响应：${large.label}`, async () => {
      const format = await until(() => visible<HTMLButtonElement>(".http-response-large-notice .el-button, .http-response-body-notice .el-button"), "format button", wait).catch(() => undefined);
      if (!format) throw new Error("no format button for the large response");
      format.click();
      // Formatted output is indented: the notice disappears and the body gains two-space lines.
      // Formatted output is indented: the second rendered row starts a two-space object.
      await until(() => !visible(".http-response-large-notice") && /^ {2}\{$/m.test(renderedText()), "formatted body", wait * 2);
      log.done(`HTTP-BODY ${JSON.stringify({ label: "formatted", renderedRows: renderedRows(), scrollHeight: bodyScroller()?.scrollHeight ?? 0 })}`);
    });
    await measure("连续发送 20 次小请求", async () => {
      const small = fixture.bodies[0];
      for (let i = 0; i < 20; i++) {
        await send(`${fixture.baseUrl}${small.path}?n=${i}`);
        await until(() => !visible(".http-request-tab__status.is-loading"), "small response sent", wait);
        await untilAsync(async () => (await tailText()).includes(small.marker), "small response", wait);
      }
    });
    await measure("空闲 5 秒（HTTP 页打开）", () => new Promise((resolve) => setTimeout(resolve, 5000)));
  } finally {
    workbench.closeTab(tab.id, true);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
  await measure("关闭 HTTP 页后空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
}
