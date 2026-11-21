import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runPageScript } from "./run-page-script";

type RefState = { snapshotId: string; refs: Map<string, Element> };

function refState(): RefState {
  return (window as unknown as { __mincodeAiRefs: RefState }).__mincodeAiRefs;
}

function snapshot(): string {
  return (runPageScript("snapshot", [12000, 200], window) as { snapshotId: string }).snapshotId;
}

describe("element action page scripts", () => {
  beforeEach(() => {
    // happy-dom does no layout; give every element a visible box.
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockReturnValue(
      new window.DOMRect(0, 0, 100, 20) as unknown as DOMRect,
    );
  });

  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  it("clicks a snapshot ref and rejects stale snapshots and wrong arity", () => {
    document.body.innerHTML = `<button>Go</button>`;
    const onClick = vi.fn();
    document.querySelector("button")?.addEventListener("click", onClick);
    const snapshotId = snapshot();

    expect(runPageScript("click", [snapshotId, "e1"], window)).toEqual({ clicked: true, ref: "e1" });
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(() => runPageScript("click", ["stale", "e1"], window)).toThrow(/stale snapshot/);
    expect(() => runPageScript("click", [snapshotId, "e9"], window)).toThrow(/element reference is stale/);
    expect(() => runPageScript("click", [snapshotId], window)).toThrow(/expects 2 arguments/);
    expect(() => runPageScript("no_such_action", [], window)).toThrow(/unknown page action/);
  });

  it("fills an input and fires input and change events", () => {
    document.body.innerHTML = `<input type=text>`;
    const input = document.querySelector("input") as HTMLInputElement;
    const events: string[] = [];
    input.addEventListener("input", () => events.push("input"));
    input.addEventListener("change", () => events.push("change"));
    const snapshotId = snapshot();

    expect(runPageScript("fill", [snapshotId, "e1", "hello"], window)).toEqual({ filled: true, ref: "e1", length: 5 });
    expect(input.value).toBe("hello");
    expect(events).toEqual(["input", "change"]);
  });

  it("blocks click, fill, press_key and inspect on password inputs and all but click on file inputs", () => {
    document.body.innerHTML = `<input type=password><input type=file>`;
    const snapshotId = snapshot();
    const [password, file] = Array.from(document.querySelectorAll("input"));
    refState().refs.set("p", password);
    refState().refs.set("f", file);

    expect(() => runPageScript("click", [snapshotId, "p"], window)).toThrow(/sensitive input/);
    for (const ref of ["p", "f"]) {
      expect(() => runPageScript("fill", [snapshotId, ref, "x"], window)).toThrow(/sensitive input/);
      expect(() => runPageScript("press_key", [snapshotId, ref, "Enter"], window)).toThrow(/sensitive input/);
      expect(() => runPageScript("inspect", [snapshotId, ref], window)).toThrow(/sensitive input/);
    }
  });

  it("clicks a file input so the page can ask the user to choose files", () => {
    document.body.innerHTML = `<input type=file><label>Pick <input type=file></label><label for=x>Other</label><input id=x type=file>`;
    const snapshotId = snapshot();
    const [direct, nested, target] = Array.from(document.querySelectorAll("input"));
    const clicks: string[] = [];
    direct.addEventListener("click", () => clicks.push("direct"));
    nested.addEventListener("click", () => clicks.push("nested"));
    target.addEventListener("click", () => clicks.push("target"));
    const labels = Array.from(document.querySelectorAll("label"));
    refState().refs.set("f", direct);
    refState().refs.set("l1", labels[0]);
    refState().refs.set("l2", labels[1]);

    expect(runPageScript("click", [snapshotId, "f"], window)).toEqual({ clicked: true, ref: "f", fileInput: true });
    expect(runPageScript("click", [snapshotId, "l1"], window)).toEqual({ clicked: true, ref: "l1", fileInput: true });
    expect(runPageScript("click", [snapshotId, "l2"], window)).toEqual({ clicked: true, ref: "l2", fileInput: true });
    expect(clicks).toEqual(["direct", "nested", "target"]);
  });

  it("presses a key on the focused element when no ref is given", () => {
    document.body.innerHTML = `<textarea></textarea>`;
    const textarea = document.querySelector("textarea") as HTMLTextAreaElement;
    textarea.focus();
    const keys: string[] = [];
    textarea.addEventListener("keydown", (event) => keys.push(`down:${event.key}`));
    textarea.addEventListener("keyup", (event) => keys.push(`up:${event.key}`));

    expect(runPageScript("press_key", ["", "", "Enter"], window)).toEqual({ pressed: "Enter", ref: "" });
    expect(keys).toEqual(["down:Enter", "up:Enter"]);
  });

  it("waits for text and for a visible ref", () => {
    document.body.innerHTML = `<p>hello world</p><button>Go</button>`;
    const snapshotId = snapshot();

    expect(runPageScript("wait_for", ["world", "", "", ""], window)).toMatchObject({ matched: true, reason: "text" });
    expect(runPageScript("wait_for", ["absent", "", "", ""], window)).toMatchObject({ matched: false });
    expect(runPageScript("wait_for", ["", "", snapshotId, "e1"], window)).toMatchObject({ matched: true, reason: "element" });
    expect(runPageScript("wait_for", ["", "", "stale", "e1"], window)).toMatchObject({ matched: false });
  });

  it("selects an option by label and fires change once", () => {
    document.body.innerHTML = `<select><option value="a">Apple</option><option value="b">Banana</option></select>`;
    const select = document.querySelector("select") as HTMLSelectElement;
    const events: string[] = [];
    select.addEventListener("input", () => events.push("input"));
    select.addEventListener("change", () => events.push("change"));
    const snapshotId = snapshot();

    expect(runPageScript("select_option", [snapshotId, "e1", [], ["Banana"]], window)).toEqual({
      selected: true,
      ref: "e1",
      changed: true,
      values: ["b"],
      labels: ["Banana"],
    });
    expect(select.value).toBe("b");
    expect(events.filter((event) => event === "change")).toHaveLength(1);
    // Selecting what is already selected does not fire change again.
    expect(runPageScript("select_option", [snapshotId, "e1", ["b"], []], window)).toMatchObject({ changed: false, values: ["b"] });
    expect(events.filter((event) => event === "change")).toHaveLength(1);
  });

  it("selects several values in a multiple select", () => {
    document.body.innerHTML = `<select multiple><option value="a">A</option><option value="b" selected>B</option><option value="c">C</option></select>`;
    const select = document.querySelector("select") as HTMLSelectElement;
    const snapshotId = snapshot();

    expect(runPageScript("select_option", [snapshotId, "e1", ["a", "c"], []], window)).toMatchObject({ values: ["a", "c"], changed: true });
    expect(Array.from(select.selectedOptions).map((option) => option.value)).toEqual(["a", "c"]);
  });

  it("rejects unmatched labels with the available options, and several values for a single select", () => {
    const many = Array.from({ length: 60 }, (_, index) => `<option value="v${index}">Item ${index}</option>`).join("");
    document.body.innerHTML = `<select>${many}</select><button>x</button>`;
    const snapshotId = snapshot();

    let message = "";
    try {
      runPageScript("select_option", [snapshotId, "e1", [], ["Durian"]], window);
    } catch (error) {
      message = String((error as Error).message);
    }
    expect(message).toMatch(/no option matches label "Durian"/);
    expect(message).toContain("Item 0");
    expect(message).toContain("Item 49");
    expect(message).not.toContain("Item 50");
    expect(message).toMatch(/10 more/);
    expect(() => runPageScript("select_option", [snapshotId, "e1", ["v1", "v2"], []], window)).toThrow(/only one option/);
    expect(() => runPageScript("select_option", [snapshotId, "e1", ["v1"], ["Item 1"]], window)).toThrow(/either values or labels/);
    expect(() => runPageScript("select_option", [snapshotId, "e1", [], []], window)).toThrow(/either values or labels/);
    expect(() => runPageScript("select_option", [snapshotId, "e2", ["v1"], []], window)).toThrow(/not a <select>/);
  });

  it("leaves an already checked checkbox alone and checks an unchecked one", () => {
    document.body.innerHTML = `<input type=checkbox checked><input type=checkbox>`;
    const [checked, unchecked] = Array.from(document.querySelectorAll("input"));
    const clicks = vi.fn();
    checked.addEventListener("click", clicks);
    const snapshotId = snapshot();

    expect(runPageScript("set_checked", [snapshotId, "e1", true], window)).toEqual({ changed: false, ref: "e1", checked: true });
    expect(clicks).not.toHaveBeenCalled();
    expect(runPageScript("set_checked", [snapshotId, "e2", true], window)).toEqual({ changed: true, ref: "e2", checked: true });
    expect(unchecked.checked).toBe(true);
    expect(runPageScript("set_checked", [snapshotId, "e1", false], window)).toEqual({ changed: true, ref: "e1", checked: false });
    expect(checked.checked).toBe(false);
  });

  it("toggles a role=switch through aria-checked and refuses to uncheck a radio", () => {
    document.body.innerHTML = `<div role="switch" aria-checked="false" tabindex="0">Wi-Fi</div><input type=radio checked>`;
    const toggle = document.querySelector("[role=switch]") as HTMLElement;
    toggle.addEventListener("click", () => toggle.setAttribute("aria-checked", String(toggle.getAttribute("aria-checked") !== "true")));
    const snapshotId = snapshot();

    expect(runPageScript("set_checked", [snapshotId, "e1", true], window)).toEqual({ changed: true, ref: "e1", checked: true });
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    expect(runPageScript("set_checked", [snapshotId, "e1", true], window)).toEqual({ changed: false, ref: "e1", checked: true });
    expect(() => runPageScript("set_checked", [snapshotId, "e2", false], window)).toThrow(/cannot be unchecked/);
  });

  it("reports a pending ARIA toggle that the page updates in a microtask", async () => {
    document.body.innerHTML = `<div role="switch" aria-checked="false">Sync</div>`;
    const toggle = document.querySelector("[role=switch]") as HTMLElement;
    const clicks = vi.fn(() => queueMicrotask(() => toggle.setAttribute("aria-checked", "true")));
    toggle.addEventListener("click", clicks);
    const snapshotId = snapshot();

    expect(runPageScript("set_checked", [snapshotId, "e1", true], window)).toEqual({ changed: true, ref: "e1", checked: true, pending: true });
    expect(runPageScript("checked_state", [snapshotId, "e1"], window)).toEqual({ ref: "e1", checked: false });
    await Promise.resolve();
    expect(runPageScript("checked_state", [snapshotId, "e1"], window)).toEqual({ ref: "e1", checked: true });
    expect(clicks).toHaveBeenCalledTimes(1);
  });

  it("reports a pending ARIA toggle that the page updates after a timeout", async () => {
    vi.useFakeTimers();
    try {
      document.body.innerHTML = `<div role="checkbox" aria-checked="true">Later</div>`;
      const box = document.querySelector("[role=checkbox]") as HTMLElement;
      const clicks = vi.fn(() => setTimeout(() => box.setAttribute("aria-checked", "false"), 100));
      box.addEventListener("click", clicks);
      const snapshotId = snapshot();

      expect(runPageScript("set_checked", [snapshotId, "e1", false], window)).toEqual({ changed: true, ref: "e1", checked: false, pending: true });
      vi.advanceTimersByTime(50);
      expect(runPageScript("checked_state", [snapshotId, "e1"], window)).toEqual({ ref: "e1", checked: true });
      vi.advanceTimersByTime(60);
      expect(runPageScript("checked_state", [snapshotId, "e1"], window)).toEqual({ ref: "e1", checked: false });
      expect(clicks).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reads the checked state without clicking and keeps the guards", () => {
    document.body.innerHTML = `<label>Agree <input type=checkbox checked></label><input type=password><button>x</button>`;
    const box = document.querySelector("input[type=checkbox]") as HTMLInputElement;
    const clicks = vi.fn();
    box.addEventListener("click", clicks);
    const snapshotId = snapshot();
    refState().refs.set("label", document.querySelector("label") as Element);
    refState().refs.set("p", document.querySelector("input[type=password]") as Element);

    expect(runPageScript("checked_state", [snapshotId, "label"], window)).toEqual({ ref: "label", checked: true });
    expect(clicks).not.toHaveBeenCalled();
    expect(() => runPageScript("checked_state", [snapshotId, "p"], window)).toThrow(/sensitive input/);
    expect(() => runPageScript("checked_state", ["stale", "label"], window)).toThrow(/stale snapshot/);
    const buttonRef = [...refState().refs.entries()].find(([, el]) => el.tagName === "BUTTON")?.[0] as string;
    expect(() => runPageScript("checked_state", [snapshotId, buttonRef], window)).toThrow(/not a checkbox/);
  });

  it("fails when the page refuses the checked state", () => {
    document.body.innerHTML = `<input type=checkbox>`;
    const box = document.querySelector("input") as HTMLInputElement;
    box.addEventListener("click", (event) => event.preventDefault());
    const snapshotId = snapshot();

    expect(() => runPageScript("set_checked", [snapshotId, "e1", true], window)).toThrow(/did not become checked/);
  });

  it("hovers with pointer and mouse events in browser order", () => {
    document.body.innerHTML = `<button>Menu</button>`;
    const button = document.querySelector("button") as HTMLButtonElement;
    const events: string[] = [];
    for (const type of ["pointerover", "pointerenter", "mouseover", "mouseenter", "pointermove", "mousemove", "click"]) {
      button.addEventListener(type, () => events.push(type));
    }
    const snapshotId = snapshot();

    expect(runPageScript("hover", [snapshotId, "e1"], window)).toEqual({ hovered: true, ref: "e1" });
    expect(events).toEqual(["pointerover", "pointerenter", "mouseover", "mouseenter", "pointermove", "mousemove"]);
  });

  it("hover enters every ancestor, outermost first, like a real pointer", () => {
    document.body.innerHTML = `<nav id="menu"><span id="wrap"><button>Menu</button></span><ul id="items" hidden><li>Item</li></ul></nav>`;
    const order: string[] = [];
    for (const id of ["menu", "wrap"]) {
      const node = document.getElementById(id) as HTMLElement;
      node.addEventListener("mouseenter", () => order.push(`mouseenter:${id}`));
      node.addEventListener("pointerenter", () => order.push(`pointerenter:${id}`));
    }
    const menu = document.getElementById("menu") as HTMLElement;
    menu.addEventListener("mouseenter", () => { (document.getElementById("items") as HTMLElement).hidden = false; });
    const snapshotId = snapshot();

    expect(runPageScript("hover", [snapshotId, "e1"], window)).toEqual({ hovered: true, ref: "e1" });
    expect(order).toEqual(["pointerenter:menu", "pointerenter:wrap", "mouseenter:menu", "mouseenter:wrap"]);
    expect((document.getElementById("items") as HTMLElement).hidden).toBe(false);
  });

  it("hover inside a shadow root also enters the shadow host", () => {
    document.body.innerHTML = `<x-menu></x-menu>`;
    const host = document.querySelector("x-menu") as HTMLElement;
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<button>Open</button>`;
    const entered: string[] = [];
    host.addEventListener("mouseenter", () => entered.push("host"));
    const snapshotId = snapshot();
    const ref = [...refState().refs].find(([, el]) => el.tagName === "BUTTON")?.[0] as string;

    runPageScript("hover", [snapshotId, ref], window);
    expect(entered).toEqual(["host"]);
  });

  it("scrolls the page down by one screen and reports the position", () => {
    document.body.innerHTML = `<p>long</p>`;
    const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
    Object.defineProperty(document.documentElement, "scrollHeight", { configurable: true, value: window.innerHeight * 3 });

    const result = runPageScript("scroll", ["", "", "down", "page", ""], window) as Record<string, unknown>;
    expect(scrollTo).toHaveBeenCalledWith({ left: 0, top: window.innerHeight, behavior: "instant" });
    expect(result).toEqual({ scrollX: 0, scrollY: 0, atStart: true, atEnd: false });
    runPageScript("scroll", ["", "", "right", 120, ""], window);
    expect(scrollTo).toHaveBeenLastCalledWith({ left: 120, top: 0, behavior: "instant" });
    expect(() => runPageScript("scroll", ["", "", "sideways", "page", ""], window)).toThrow(/direction/);
    expect(() => runPageScript("scroll", ["", "", "", "page", ""], window)).toThrow(/either ref or direction/);
    delete (document.documentElement as unknown as Record<string, unknown>).scrollHeight;
  });

  it("scrolls a container ref and scrolls an element ref into view", () => {
    document.body.innerHTML = `<div id=list><p>a</p></div><button>Down here</button>`;
    const list = document.getElementById("list") as HTMLElement;
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 150 });
    list.setAttribute("role", "list");
    const snapshotId = snapshot();
    const containerScroll = vi.fn((options: ScrollToOptions) => { list.scrollTop = Number(options.top); });
    list.scrollTo = containerScroll as unknown as typeof list.scrollTo;
    const button = document.querySelector("button") as HTMLButtonElement;
    const intoView = vi.fn();
    button.scrollIntoView = intoView;

    expect(runPageScript("scroll", [snapshotId, "", "down", "page", "e1"], window)).toMatchObject({ scrollY: 100, atStart: false });
    expect(containerScroll).toHaveBeenCalledWith({ left: 0, top: 100, behavior: "instant" });
    runPageScript("scroll", [snapshotId, "e2", "", "page", ""], window);
    expect(intoView).toHaveBeenCalledWith({ block: "center", inline: "center", behavior: "instant" });
    expect(() => runPageScript("scroll", [snapshotId, "e2", "down", "page", ""], window)).toThrow(/either ref or direction/);
  });

  it("blocks select, check, hover and scroll on password inputs and all but scroll-to on file inputs", () => {
    document.body.innerHTML = `<input type=password><input type=file>`;
    const snapshotId = snapshot();
    const [password, file] = Array.from(document.querySelectorAll("input"));
    refState().refs.set("p", password);
    refState().refs.set("f", file);
    file.scrollIntoView = vi.fn();

    for (const ref of ["p", "f"]) {
      expect(() => runPageScript("select_option", [snapshotId, ref, ["x"], []], window)).toThrow(/sensitive input/);
      expect(() => runPageScript("set_checked", [snapshotId, ref, true], window)).toThrow(/sensitive input/);
      expect(() => runPageScript("hover", [snapshotId, ref], window)).toThrow(/sensitive input/);
      expect(() => runPageScript("scroll", [snapshotId, "", "down", "page", ref], window)).toThrow(/sensitive input/);
    }
    expect(() => runPageScript("scroll", [snapshotId, "p", "", "page", ""], window)).toThrow(/sensitive input/);
    expect(runPageScript("scroll", [snapshotId, "f", "", "page", ""], window)).toMatchObject({ atStart: true });
    expect(() => runPageScript("hover", ["stale", "f"], window)).toThrow(/stale snapshot/);
  });

  it("inspects a snapshot ref", () => {
    document.body.innerHTML = `<button aria-label="Send">Go</button>`;
    const snapshotId = snapshot();

    expect(runPageScript("inspect", [snapshotId, "e1"], window)).toMatchObject({
      ref: "e1",
      tag: "button",
      name: "Send",
      rect: { x: 0, y: 0, width: 100, height: 20 },
    });
  });
});

describe("element actions inside iframes and shadow roots", () => {
  beforeEach(() => {
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockReturnValue(
      new window.DOMRect(0, 0, 100, 20) as unknown as DOMRect,
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  // A same-origin iframe whose body holds html; returns its window and document.
  function frame(html: string): { win: Window & typeof globalThis; doc: Document; iframe: HTMLIFrameElement } {
    document.body.innerHTML = `<iframe></iframe>`;
    const iframe = document.querySelector("iframe") as HTMLIFrameElement;
    const doc = iframe.contentDocument as Document;
    doc.body.innerHTML = html;
    return { win: iframe.contentWindow as Window & typeof globalThis, doc, iframe };
  }

  // Replaces win[name] with a subclass that records the event types it constructs.
  function recordConstructed(win: Window, name: "Event" | "InputEvent" | "MouseEvent" | "PointerEvent" | "KeyboardEvent"): string[] {
    const types: string[] = [];
    const Base = (win as unknown as Record<string, typeof Event>)[name];
    Object.defineProperty(win, name, {
      configurable: true,
      writable: true,
      value: class extends Base {
        constructor(type: string, init?: EventInit) {
          super(type, init);
          types.push(type);
        }
      },
    });
    return types;
  }

  it("fills and selects inside an iframe with that frame's event constructors", () => {
    const { win, doc } = frame(`<input aria-label=Name><select aria-label=Size><option>S</option><option>L</option></select>`);
    const inputs = recordConstructed(win, "InputEvent");
    const events = recordConstructed(win, "Event");
    const snapshotId = snapshot();
    const refs = Array.from(refState().refs.entries());
    const ref = (el: Element) => refs.find(([, value]) => value === el)?.[0] as string;
    const input = doc.querySelector("input") as HTMLInputElement;
    const select = doc.querySelector("select") as HTMLSelectElement;

    expect(runPageScript("fill", [snapshotId, ref(input), "Ada"], window)).toMatchObject({ filled: true });
    expect(input.value).toBe("Ada");
    expect(runPageScript("select_option", [snapshotId, ref(select), [], ["L"]], window)).toMatchObject({ changed: true, labels: ["L"] });
    expect(select.value).toBe("L");
    expect(inputs).toEqual(["input"]);
    expect(events).toEqual(["change", "input", "change"]);
  });

  it("hovers an iframe element with that frame's pointer and mouse events", () => {
    const { win, doc } = frame(`<button>Menu</button>`);
    const mouse = recordConstructed(win, "MouseEvent");
    const pointer = typeof win.PointerEvent === "function" ? recordConstructed(win, "PointerEvent") : mouse;
    const snapshotId = snapshot();
    const button = doc.querySelector("button") as HTMLButtonElement;
    button.scrollIntoView = vi.fn();

    expect(runPageScript("hover", [snapshotId, "e1"], window)).toEqual({ hovered: true, ref: "e1" });
    // Enter events go to the frame's html, body and button; all built with the frame's constructors.
    const enters = ["pointerenter", "pointerenter", "pointerenter", "mouseenter", "mouseenter", "mouseenter"];
    expect([...pointer, ...mouse].sort()).toEqual(
      ["pointerover", "mouseover", "pointermove", "mousemove", ...enters].sort(),
    );
  });

  it("waits for an iframe ref using that frame's computed style", () => {
    const { win } = frame(`<button>Ready</button>`);
    const snapshotId = snapshot();
    const style = vi.spyOn(win, "getComputedStyle");

    expect(runPageScript("wait_for", ["", "", snapshotId, "e1"], window)).toMatchObject({ matched: true, reason: "element" });
    expect(style).toHaveBeenCalled();
  });

  it("presses keys on the element focused inside an iframe and still blocks a focused password", () => {
    const { win, doc, iframe } = frame(`<input aria-label=Search><input type=password>`);
    const keys = recordConstructed(win, "KeyboardEvent");
    const [search, password] = Array.from(doc.querySelectorAll("input"));
    const seen: string[] = [];
    search.addEventListener("keydown", (event) => seen.push((event as KeyboardEvent).key));
    // A browser reports the iframe as the top document's active element while focus is inside it.
    Object.defineProperty(document, "activeElement", { configurable: true, get: () => iframe });
    search.focus();

    expect(runPageScript("press_key", ["", "", "Enter"], window)).toEqual({ pressed: "Enter", ref: "" });
    expect(seen).toEqual(["Enter"]);
    expect(keys).toEqual(["keydown", "keyup"]);
    password.focus();
    expect(() => runPageScript("press_key", ["", "", "a"], window)).toThrow(/sensitive input/);
    delete (document as unknown as Record<string, unknown>).activeElement;
  });

  it("checks a checkbox inside an open shadow root", () => {
    document.body.innerHTML = `<div></div>`;
    const root = (document.querySelector("div") as HTMLElement).attachShadow({ mode: "open" });
    root.innerHTML = `<input type=checkbox aria-label=Agree>`;
    const snapshotId = snapshot();
    const box = root.querySelector("input") as HTMLInputElement;
    box.scrollIntoView = vi.fn();

    expect(runPageScript("set_checked", [snapshotId, "e1", true], window)).toEqual({ changed: true, ref: "e1", checked: true });
    expect(box.checked).toBe(true);
  });

  it("treats refs into a removed iframe as stale", () => {
    const { iframe } = frame(`<button>Gone</button>`);
    const snapshotId = snapshot();
    iframe.remove();

    expect(() => runPageScript("click", [snapshotId, "e1"], window)).toThrow(/element reference is stale/);
    expect(() => runPageScript("inspect", [snapshotId, "e1"], window)).toThrow(/element reference is stale/);
  });
});
