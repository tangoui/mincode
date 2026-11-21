import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { expandPageScript, runPageScript } from "./run-page-script";

interface SnapshotElement {
  ref: string;
  tag: string;
  role: string;
  name: string;
  href?: string;
}

interface Snapshot {
  snapshotId: string;
  elements: SnapshotElement[];
}

describe("snapshot page script", () => {
  beforeEach(() => {
    // happy-dom does no layout, so every element would measure 0x0 and count as hidden.
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockReturnValue(
      new window.DOMRect(0, 0, 100, 20) as unknown as DOMRect,
    );
  });

  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  it("lists interactive elements and skips password inputs", () => {
    document.body.innerHTML = `<button>Go</button><input type=password><a href="https://x.test/a?q=1">L</a>`;

    const snapshot = runPageScript("snapshot", [12000, 200], window) as Snapshot;

    expect(snapshot.elements).toHaveLength(2);
    expect(snapshot.elements.map((item) => item.tag)).toEqual(["button", "a"]);
    const link = snapshot.elements.find((item) => item.tag === "a");
    expect(link?.href).toBe("https://x.test/a");
    const state = (window as unknown as { __mincodeAiRefs: { snapshotId: string; refs: Map<string, Element> } })
      .__mincodeAiRefs;
    expect(state.snapshotId).toBe(snapshot.snapshotId);
    expect(state.refs.get("e1")?.tagName).toBe("BUTTON");
  });
});

describe("snapshot file inputs", () => {
  beforeEach(() => {
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockReturnValue(
      new window.DOMRect(0, 0, 100, 20) as unknown as DOMRect,
    );
  });

  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  it("lists file inputs by existence only and still skips password inputs", () => {
    document.body.innerHTML =
      `<input type=password value=secret>` +
      `<label for=up>Upload report</label><input id=up type=file accept=".txt,image/*" multiple>` +
      `<input type=file style="display:none">`;

    const snapshot = runPageScript("snapshot", [12000, 200], window) as { elements: Record<string, unknown>[] };

    expect(snapshot.elements.filter((item) => item.type === "password")).toHaveLength(0);
    const files = snapshot.elements.filter((item) => item.type === "file");
    expect(files).toHaveLength(2);
    expect(files[0]).toMatchObject({ tag: "input", type: "file", accept: ".txt,image/*", multiple: true, selectedCount: 0 });
    expect(files[1]).toMatchObject({ tag: "input", type: "file", accept: "", multiple: false, selectedCount: 0 });
    for (const item of files) {
      expect(Object.keys(item).sort()).toEqual(
        ["accept", "disabled", "multiple", "name", "ref", "role", "selectedCount", "tag", "type"].sort(),
      );
      expect(item).not.toHaveProperty("value");
    }
    expect(files[0].name).toBe("Upload report");
    const refs = (window as unknown as { __mincodeAiRefs: { refs: Map<string, Element> } }).__mincodeAiRefs.refs;
    expect(refs.get(files[1].ref as string)?.getAttribute("type")).toBe("file");
  });

  it("reports how many files are selected without their names", () => {
    document.body.innerHTML = `<input type=file multiple>`;
    const input = document.querySelector("input") as HTMLInputElement;
    const fakeFiles = [new File(["a"], "secret-plan.txt"), new File(["b"], "other.txt")];
    Object.defineProperty(input, "files", { configurable: true, get: () => fakeFiles });

    const snapshot = runPageScript("snapshot", [12000, 200], window) as { elements: Record<string, unknown>[] };

    expect(snapshot.elements[0]).toMatchObject({ type: "file", selectedCount: 2 });
    expect(JSON.stringify(snapshot)).not.toContain("secret-plan");
  });
});

describe("page script placeholders", () => {
  it("never reads __ARG10__ as __ARG1__ followed by 0", () => {
    const args = [0, "one", 2, 3, 4, 5, 6, 7, 8, 9, "ten"];
    const template = args.map((_, index) => `__ARG${index}__`).join(",");
    expect(expandPageScript("test", `[${template},__ARG1__]`, args)).toBe(
      `[0,"one",2,3,4,5,6,7,8,9,"ten","one"]`,
    );
  });

  it("does not expand placeholder text inside argument values", () => {
    expect(expandPageScript("test", "__ARG0__+__ARG1__", ["__ARG1__", "x"])).toBe(`"__ARG1__"+"x"`);
  });

  it("encodes strings exactly like Go json.Marshal", () => {
    expect(expandPageScript("test", "__ARG0__", ["a'b\"c\u2028\u2029</script>&"])).toBe(
      String.raw`"a'b\"c\u2028\u2029\u003c/script\u003e\u0026"`,
    );
  });

  it("keeps a U+2028 value as data even inside a line comment", () => {
    const value = "x\u2028globalThis.__pwned=1;//";
    const script = expandPageScript("test", "(() => { // __ARG0__\n return 1 })()", [value]);
    const evaluate = new Function(`return (${script});`) as () => unknown;
    expect(evaluate()).toBe(1);
    expect((globalThis as Record<string, unknown>).__pwned).toBeUndefined();
  });

  it("rejects missing and extra arguments like the Go side", () => {
    expect(() => expandPageScript("test", "__ARG0__ __ARG1__", [1])).toThrow();
    expect(() => expandPageScript("test", "__ARG0__", [1, 2])).toThrow();
    expect(() => runPageScript("snapshot", [12000], window)).toThrow();
  });

});

type Item = Record<string, unknown>;
type FrameState = { snapshotId: string; refs: Map<string, Element> };

function takeSnapshot(maxElements = 200, maxText = 12000): { snapshotId: string; text: string; elements: Item[]; truncatedElements: boolean; truncatedText: boolean } {
  return runPageScript("snapshot", [maxText, maxElements], window) as ReturnType<typeof takeSnapshot>;
}

function byName(snapshot: { elements: Item[] }, name: string): Item {
  const item = snapshot.elements.find((element) => element.name === name);
  if (!item) throw new Error(`no snapshot element named ${name}: ${JSON.stringify(snapshot.elements)}`);
  return item;
}

function frameDocument(frame: Element | null | undefined): Document {
  const doc = (frame as HTMLIFrameElement | null)?.contentDocument;
  if (!doc) throw new Error("iframe has no document");
  return doc;
}

describe("snapshot frames, shadow roots, viewport and options", () => {
  // Element boxes by element; everything else gets a visible 100x20 box at the top-left.
  let rects: Map<Element, [number, number, number, number]>;

  beforeEach(() => {
    rects = new Map();
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const [x, y, width, height] = rects.get(this) ?? [0, 0, 100, 20];
      return new window.DOMRect(x, y, width, height) as unknown as DOMRect;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    for (const el of [document.documentElement, document.body]) {
      el.removeAttribute("style");
      for (const key of ["scrollWidth", "scrollHeight", "clientWidth", "clientHeight"]) delete (el as unknown as Record<string, unknown>)[key];
    }
    document.body.innerHTML = "";
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  it("lists a button inside a same-origin iframe with its frame path and clicks it", () => {
    document.body.innerHTML = `<button>Top</button><iframe srcdoc="<p>Frame text</p><button>Inside</button>"></iframe>`;
    const inside = frameDocument(document.querySelector("iframe")).querySelector("button") as HTMLButtonElement;
    const onClick = vi.fn();
    inside.addEventListener("click", onClick);

    const snapshot = takeSnapshot();

    expect(byName(snapshot, "Top")).not.toHaveProperty("frame");
    const item = byName(snapshot, "Inside");
    expect(item).toMatchObject({ tag: "button", role: "button", frame: "0", inViewport: true });
    expect(snapshot.elements.some((element) => element.tag === "iframe")).toBe(false);
    expect(snapshot.text).toContain("Frame text");
    expect(runPageScript("click", [snapshot.snapshotId, item.ref], window)).toEqual({ clicked: true, ref: item.ref });
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("numbers nested iframes and translates their boxes to the top viewport", () => {
    document.body.innerHTML = `<iframe></iframe>`;
    const outer = document.querySelector("iframe") as HTMLIFrameElement;
    const outerDoc = frameDocument(outer);
    outerDoc.body.innerHTML =
      `<iframe></iframe>` +
      `<iframe style="border-left-width:2px;border-top-width:2px;padding-left:3px;padding-top:4px" srcdoc="<button>Deep</button><button>Below</button>"></iframe>`;
    const inner = outerDoc.querySelectorAll("iframe")[1];
    const [deep, below] = Array.from(frameDocument(inner).querySelectorAll("button"));
    rects.set(outer, [100, 50, 400, 300]);
    rects.set(inner, [10, 20, 200, 100]);
    rects.set(deep, [5, 6, 50, 10]);
    // Inside the inner frame's own viewport box (about 195x94) only the first button is visible.
    rects.set(below, [5, 150, 50, 10]);

    const snapshot = takeSnapshot();

    expect(byName(snapshot, "Deep")).toMatchObject({ frame: "0.1", inViewport: true });
    expect(byName(snapshot, "Below")).toMatchObject({ frame: "0.1", inViewport: false });
    expect(runPageScript("inspect", [snapshot.snapshotId, byName(snapshot, "Deep").ref], window)).toMatchObject({
      rect: { x: 120, y: 82, width: 50, height: 10 },
    });
  });

  it("marks elements outside the viewport", () => {
    document.body.innerHTML = `<button>Here</button><button>Far below</button><button>Far right</button>`;
    const [, below, right] = Array.from(document.querySelectorAll("button"));
    rects.set(below, [0, window.innerHeight + 500, 100, 20]);
    rects.set(right, [window.innerWidth + 10, 0, 100, 20]);

    const snapshot = takeSnapshot();

    expect(byName(snapshot, "Here").inViewport).toBe(true);
    expect(byName(snapshot, "Far below").inViewport).toBe(false);
    expect(byName(snapshot, "Far right").inViewport).toBe(false);
  });

  it("lists and fills inputs in open shadow roots and never reaches closed ones", () => {
    document.body.innerHTML = `<div id=open></div><div id=closed></div>`;
    const open = (document.getElementById("open") as HTMLElement).attachShadow({ mode: "open" });
    open.innerHTML = `<input aria-label="Shadow field"><input type=password aria-label="Shadow password">`;
    (document.getElementById("closed") as HTMLElement).attachShadow({ mode: "closed" }).innerHTML = `<button>Secret</button>`;

    const snapshot = takeSnapshot();

    const item = byName(snapshot, "Shadow field");
    expect(item).toMatchObject({ tag: "input", shadow: true, inViewport: true });
    expect(item).not.toHaveProperty("frame");
    expect(JSON.stringify(snapshot)).not.toContain("Secret");
    expect(JSON.stringify(snapshot)).not.toContain("Shadow password");
    expect(runPageScript("fill", [snapshot.snapshotId, item.ref, "hi"], window)).toMatchObject({ filled: true });
    expect((open.querySelector("input") as HTMLInputElement).value).toBe("hi");
  });

  it("lists up to 50 select options and flags the rest as truncated", () => {
    const many = Array.from({ length: 60 }, (_, i) => `<option value=v${i}>Option  ${i}</option>`).join("");
    document.body.innerHTML =
      `<select aria-label=Many>${many}</select>` +
      `<select aria-label=Few><option value=a>A</option><option value=b disabled>B</option></select>`;
    (document.querySelector("select") as HTMLSelectElement).value = "v3";

    const snapshot = takeSnapshot();

    const manyItem = byName(snapshot, "Many");
    const options = manyItem.options as Item[];
    expect(options).toHaveLength(50);
    expect(options[3]).toEqual({ value: "v3", label: "Option 3", selected: true, disabled: false });
    expect(manyItem.optionsTruncated).toBe(true);
    const few = byName(snapshot, "Few");
    expect(few.options).toEqual([
      { value: "a", label: "A", selected: true, disabled: false },
      { value: "b", label: "B", selected: false, disabled: true },
    ]);
    expect(few).not.toHaveProperty("optionsTruncated");
  });

  it("lists a cross-origin iframe without entering it", () => {
    document.body.innerHTML = `<iframe title=Ad></iframe><iframe></iframe>`;
    const [crossOrigin, sandboxed] = Array.from(document.querySelectorAll("iframe"));
    Object.defineProperty(crossOrigin, "src", { configurable: true, get: () => "https://ads.other.test:8443/slot?id=1#x" });
    Object.defineProperty(crossOrigin, "contentDocument", {
      configurable: true,
      get: () => {
        throw new window.DOMException("Blocked a frame with origin", "SecurityError");
      },
    });
    Object.defineProperty(sandboxed, "src", { configurable: true, get: () => "about:blank" });
    Object.defineProperty(sandboxed, "contentDocument", { configurable: true, get: () => null });
    rects.set(sandboxed, [0, window.innerHeight + 10, 300, 200]);

    const snapshot = takeSnapshot();

    const frames = snapshot.elements.filter((element) => element.tag === "iframe");
    expect(frames).toEqual([
      { ref: frames[0].ref, tag: "iframe", crossOrigin: true, origin: "https://ads.other.test:8443", inViewport: true },
      { ref: frames[1].ref, tag: "iframe", crossOrigin: true, origin: "", inViewport: false },
    ]);
    expect((window as unknown as { __mincodeAiRefs: FrameState }).__mincodeAiRefs.refs.get(frames[0].ref as string)).toBe(crossOrigin);
  });

  it("counts maxElements and maxText across frames", () => {
    document.body.innerHTML = `<p>top words</p><button>One</button><iframe srcdoc="<p>frame words</p><button>Two</button><button>Three</button>"></iframe>`;

    const snapshot = takeSnapshot(2, 1000);

    expect(snapshot.elements.map((element) => element.name)).toEqual(["One", "Two"]);
    expect(snapshot.truncatedElements).toBe(true);
    expect(snapshot.text).toContain("frame words");
    expect(takeSnapshot(3).truncatedElements).toBe(false);
  });

  it("caps option labels and values at 300 characters and still selects a truncated option", () => {
    const long = "x".repeat(299) + "\u{1F600}" + "tail".repeat(1000);
    document.body.innerHTML = `<select aria-label=Long><option value=short>Short</option><option></option><option></option></select>`;
    const [, byLabel, byValue] = Array.from((document.querySelector("select") as HTMLSelectElement).options);
    byLabel.value = "plain";
    byLabel.textContent = long;
    byValue.value = long;
    byValue.textContent = "Value option";

    const snapshot = takeSnapshot();

    const select = byName(snapshot, "Long");
    const [short, labelled, valued] = select.options as Item[];
    expect(short).toEqual({ value: "short", label: "Short", selected: true, disabled: false });
    const cut = "x".repeat(299) + "\u{1F600}";
    expect(labelled).toEqual({ value: "plain", label: cut, labelTruncated: true, selected: false, disabled: false });
    expect(valued).toEqual({ value: cut, valueTruncated: true, label: "Value option", selected: false, disabled: false });
    expect(runPageScript("select_option", [snapshot.snapshotId, select.ref, [], [cut]], window)).toMatchObject({ values: ["plain"] });
    expect(runPageScript("select_option", [snapshot.snapshotId, select.ref, [cut], []], window)).toMatchObject({ labels: ["Value option"] });
    // Only an exactly 300-character string is read as a truncated prefix.
    expect(() => runPageScript("select_option", [snapshot.snapshotId, select.ref, [], ["x".repeat(299)]], window)).toThrow(/no option matches/);
  });

  it("refuses a truncated label that several options share and bounds the options in the error", () => {
    const prefix = "y".repeat(300);
    document.body.innerHTML = `<select aria-label=Twins><option></option><option></option></select>`;
    const [first, second] = Array.from((document.querySelector("select") as HTMLSelectElement).options);
    first.textContent = prefix + " one";
    second.textContent = prefix + " two";
    const snapshot = takeSnapshot();
    const ref = byName(snapshot, "Twins").ref;

    expect(() => runPageScript("select_option", [snapshot.snapshotId, ref, [], [prefix]], window)).toThrow(/several options/);
    let message = "";
    try {
      runPageScript("select_option", [snapshot.snapshotId, ref, [], ["missing"]], window);
    } catch (error) {
      message = String(error);
    }
    expect(message).toContain("no option matches");
    expect(message).not.toContain(" one");
    expect(message.length).toBeLessThan(1500);
  });

  it("caps link hrefs at 300 characters", () => {
    document.body.innerHTML = `<a href="https://x.test/${"p".repeat(500)}?secret=1">Long</a><a href="https://x.test/ok">Ok</a>`;

    const snapshot = takeSnapshot();

    const long = byName(snapshot, "Long");
    expect(long.href).toBe(`https://x.test/${"p".repeat(500)}`.slice(0, 300));
    expect(long.hrefTruncated).toBe(true);
    expect(byName(snapshot, "Ok")).not.toHaveProperty("hrefTruncated");
  });

  it("locates file inputs in iframes and shadow roots without adding inViewport", () => {
    document.body.innerHTML = `<div></div><iframe srcdoc="<input type=file aria-label=FrameUpload>"></iframe>`;
    (document.querySelector("div") as HTMLElement).attachShadow({ mode: "open" }).innerHTML = `<input type=file aria-label=ShadowUpload>`;

    const snapshot = takeSnapshot();

    const keys = ["accept", "disabled", "multiple", "name", "ref", "role", "selectedCount", "tag", "type"];
    expect(Object.keys(byName(snapshot, "ShadowUpload")).sort()).toEqual([...keys, "shadow"].sort());
    expect(byName(snapshot, "ShadowUpload").shadow).toBe(true);
    expect(Object.keys(byName(snapshot, "FrameUpload")).sort()).toEqual([...keys, "frame"].sort());
    expect(byName(snapshot, "FrameUpload").frame).toBe("0");
  });

  it("stops descending after 32 nested shadow roots and flags the snapshot as truncated", () => {
    document.body.innerHTML = `<div id=host></div><button>After</button>`;
    let host = document.getElementById("host") as HTMLElement;
    for (let level = 1; level <= 40; level++) {
      const root = host.attachShadow({ mode: "open" });
      root.innerHTML = `<button>L${level}</button><div></div>`;
      host = root.querySelector("div") as HTMLElement;
    }

    const snapshot = takeSnapshot();

    const names = snapshot.elements.map((element) => element.name);
    expect(names).toContain("L32");
    expect(names).not.toContain("L33");
    expect(names).toContain("After");
    expect(snapshot.truncatedElements).toBe(true);
  });

  it("caps the page title at 300 code points", () => {
    document.title = "t".repeat(299) + "\u{1F600}" + "tail".repeat(100);

    const snapshot = takeSnapshot() as unknown as { title: string; titleTruncated?: boolean };

    expect(snapshot.title).toBe("t".repeat(299) + "\u{1F600}");
    expect(snapshot.titleTruncated).toBe(true);
    document.title = "Short";
    expect(takeSnapshot()).not.toHaveProperty("titleTruncated");
    document.title = "";
  });

  it("refuses an exactly 300-character label that is exact for one option and a prefix of another", () => {
    const exact = "z".repeat(300);
    document.body.innerHTML = `<select aria-label=Near><option></option><option></option></select>`;
    const [first, second] = Array.from((document.querySelector("select") as HTMLSelectElement).options);
    first.value = exact;
    first.textContent = exact;
    second.value = exact + "-longer";
    second.textContent = exact + " longer";
    const snapshot = takeSnapshot();
    const ref = byName(snapshot, "Near").ref;

    // The snapshot lists both as the same cut text, so neither the label nor the value identifies one option.
    expect(() => runPageScript("select_option", [snapshot.snapshotId, ref, [], [exact]], window)).toThrow(
      "the truncated label matches several options; select by values instead",
    );
    expect(() => runPageScript("select_option", [snapshot.snapshotId, ref, [exact], []], window)).toThrow(
      "the truncated value matches several options; select by labels instead",
    );
    expect(runPageScript("select_option", [snapshot.snapshotId, ref, [exact + "-longer"], []], window)).toMatchObject({ values: [exact + "-longer"] });
  });

  it("skips hidden inputs", () => {
    document.body.innerHTML = `<input type=hidden name=csrf value=token><input aria-label=Shown>`;

    const snapshot = takeSnapshot();

    expect(snapshot.elements.map((element) => element.name)).toEqual(["Shown"]);
  });

  it("marks scrollable containers so browser.scroll can use them as containerRef", () => {
    document.body.innerHTML =
      `<div id=feed style="overflow-y:auto" aria-label=Feed><p>a</p></div>` +
      `<div id=wide style="overflow-x:scroll"><p>b</p></div>` +
      `<div id=clipped style="overflow:hidden"><p>c</p></div>` +
      `<div id=short style="overflow-y:auto"><p>d</p></div>` +
      `<div id=menu role=listbox aria-label=Menu style="overflow-y:scroll"></div>`;
    const metrics = (target: string | HTMLElement, scroll: [number, number], client: [number, number]) => {
      const el = typeof target === "string" ? (document.getElementById(target) as HTMLElement) : target;
      Object.defineProperty(el, "scrollWidth", { configurable: true, value: scroll[0] });
      Object.defineProperty(el, "scrollHeight", { configurable: true, value: scroll[1] });
      Object.defineProperty(el, "clientWidth", { configurable: true, value: client[0] });
      Object.defineProperty(el, "clientHeight", { configurable: true, value: client[1] });
      return el;
    };
    const feed = metrics("feed", [100, 500], [100, 100]);
    metrics("wide", [900, 100], [300, 100]);
    metrics("clipped", [100, 500], [100, 100]);
    metrics("short", [100, 100], [100, 100]);
    metrics("menu", [100, 400], [100, 100]);
    // The page itself scrolls through browser.scroll without a containerRef; it is never listed.
    for (const el of [document.documentElement, document.body]) {
      el.style.overflowY = "auto";
      metrics(el, [100, 5000], [100, 100]);
    }

    const snapshot = takeSnapshot();

    expect(snapshot.elements).toEqual([
      { ref: "e1", tag: "div", role: "", name: "Feed", scrollable: true, inViewport: true },
      { ref: "e2", tag: "div", role: "", name: "b", scrollable: true, inViewport: true },
      expect.objectContaining({ ref: "e3", tag: "div", role: "listbox", name: "Menu", scrollable: true, inViewport: true }),
    ]);
    const scrollTo = vi.fn((options: ScrollToOptions) => { feed.scrollTop = Number(options.top); });
    feed.scrollTo = scrollTo as unknown as typeof feed.scrollTo;
    expect(runPageScript("scroll", [snapshot.snapshotId, "", "down", "page", "e1"], window)).toMatchObject({ scrollY: 100 });
    expect(scrollTo).toHaveBeenCalledWith({ left: 0, top: 100, behavior: "instant" });
  });
});
