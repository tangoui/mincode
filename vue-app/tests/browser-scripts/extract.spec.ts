import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { runPageScript } from "./run-page-script";

interface Extract {
  title: string;
  titleTruncated?: boolean;
  url: string;
  content: string;
  truncated: boolean;
}

function extract(format: "markdown" | "text" = "markdown", maxChars = 20000, snapshotId = "", ref = ""): Extract {
  return runPageScript("extract", [format, maxChars, snapshotId, ref], window) as Extract;
}

// happy-dom recomputes getComputedStyle along the whole ancestor chain on every
// call, so 400–600 levels take several seconds (about 2 s alone, more when the
// suite runs in parallel). Real browsers cache computed style; this bound only
// covers the test DOM.
const DEEP_TREE_TIMEOUT = 30_000;

function frameDocument(frame: Element | null | undefined): Document {
  const doc = (frame as HTMLIFrameElement | null)?.contentDocument;
  if (!doc) throw new Error("iframe has no document");
  return doc;
}

describe("extract page script", () => {
  beforeEach(() => {
    // Only the snapshot needs layout (it skips 0x0 elements); extraction never measures.
    vi.spyOn(window.Element.prototype, "getBoundingClientRect").mockReturnValue(
      new window.DOMRect(0, 0, 100, 20) as unknown as DOMRect,
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.title = "";
    document.head.innerHTML = "";
    document.body.innerHTML = "";
    delete (window as unknown as Record<string, unknown>).__mincodeAiRefs;
  });

  it("extracts only the article when the page also has navigation, footer and aside", () => {
    document.title = "Guide";
    document.body.innerHTML =
      `<nav><a href="/home">Home</a><a href="/about">About</a></nav>` +
      `<aside>Sponsored</aside>` +
      `<article><h1>Title</h1><p>Body text of the article.</p><footer>Article footer</footer></article>` +
      `<footer>Copyright</footer><script>var leaked = "script text";</script>`;

    const result = extract();

    expect(result).toEqual({ title: "Guide", url: location.href, content: "# Title\n\nBody text of the article.", truncated: false });
  });

  it("prefers main and role=main over articles", () => {
    document.body.innerHTML = `<article>Teaser</article><div role=main><p>Main text</p><article><p>Inner article</p></article></div>`;

    expect(extract().content).toBe("Main text\n\nInner article");
    document.body.innerHTML = `<div role=main>Role main</div><main><p>Main element</p></main>`;
    expect(extract().content).toBe("Role main");
  });

  it("falls back to the block with the densest non-link text", () => {
    document.body.innerHTML =
      `<div class=menu><p><a href="/a">A link that is fairly long</a> <a href="/b">Another link that is long too</a></p></div>` +
      `<div class=content><p>The first paragraph of the real content, which is long enough.</p>` +
      `<p>The second paragraph of the real content keeps going for a while.</p></div>` +
      `<div class=footer-ish><p>Short</p></div>`;

    const content = extract().content;

    expect(content).toBe(
      "The first paragraph of the real content, which is long enough.\n\nThe second paragraph of the real content keeps going for a while.",
    );
  });

  it("maps h1 to h3 to # to ### and keeps lower levels", () => {
    document.body.innerHTML = `<main><h1>One</h1><h2>Two</h2><h3>Three <em>em</em></h3><h4>Four</h4><p>Text</p></main>`;

    expect(extract().content).toBe("# One\n\n## Two\n\n### Three *em*\n\n#### Four\n\nText");
  });

  it("converts unordered, ordered and nested lists", () => {
    document.body.innerHTML =
      `<main><ul><li>Apple</li><li>Pear<ol><li>Seed</li><li>Stem</li></ol></li></ul>` +
      `<ol start=3><li>Third</li><li>Fourth</li></ol></main>`;

    expect(extract().content).toBe("- Apple\n- Pear\n  1. Seed\n  2. Stem\n\n3. Third\n4. Fourth");
  });

  it("converts tables to Markdown tables with a header separator", () => {
    document.body.innerHTML =
      `<main><table><thead><tr><th>Name</th><th>Price</th></tr></thead>` +
      `<tbody><tr><td>Tea | green</td><td>3</td></tr><tr><td>Coffee</td></tr></tbody></table></main>`;

    expect(extract().content).toBe("| Name | Price |\n| --- | --- |\n| Tea \\| green | 3 |\n| Coffee |  |");
  });

  it("renders layout tables as blocks, not Markdown tables", () => {
    document.body.innerHTML = `<main><table role=presentation><tr><td><p>Left column</p></td><td><p>Right column</p></td></tr></table></main>`;

    expect(extract().content).toBe("Left column\n\nRight column");
  });

  it("writes links as [text](absolute URL) without query, fragment or credentials", () => {
    document.body.innerHTML =
      `<main><p>See <a href="/docs/page?token=secret#part">the docs</a> and ` +
      `<a href="https://user:pw@other.test/x?y=1">other</a>, ` +
      `<a href="javascript:alert(1)">script link</a>, <a href="/a(b)">parens</a>.</p></main>`;

    const content = extract().content;

    expect(content).toBe(
      "See [the docs](http://localhost:3000/docs/page) and [other](https://other.test/x), script link, [parens](http://localhost:3000/a%28b%29).",
    );
    expect(content).not.toContain("secret");
    expect(content).not.toContain("pw");
  });

  it("writes a link around block content as one link paragraph", () => {
    document.body.innerHTML = `<main><a href="/post"><h2>Card title</h2><p>Card summary</p></a></main>`;

    expect(extract().content).toBe("[Card title Card summary](http://localhost:3000/post)");
  });

  // Nesting is counted from the chosen root, so a deep densest block is still read.
  it("reads a densest block nested deeper than the nesting limit", () => {
    const root = document.createElement("div");
    let parent = root;
    for (let i = 0; i < 500; i++) parent = parent.appendChild(document.createElement("div"));
    parent.appendChild(document.createElement("p")).textContent = "A deep paragraph that is long enough to score.";
    document.body.replaceChildren(root);

    const result = extract();

    expect(result).toMatchObject({ content: "A deep paragraph that is long enough to score.", truncated: false });
  }, DEEP_TREE_TIMEOUT);

  it("outputs image alt text only", () => {
    document.body.innerHTML = `<main><p>Logo: <img src="/logo.png?sig=1" alt="Company logo"><img src="/spacer.gif"></p></main>`;

    const content = extract().content;

    expect(content).toBe("Logo: Company logo");
    expect(content).not.toContain("logo.png");
  });

  it("truncates to maxChars and reports truncated", () => {
    document.body.innerHTML = `<main>${Array.from({ length: 200 }, (_, i) => `<p>Paragraph ${i} with some filler text.</p>`).join("")}</main>`;

    const result = extract("markdown", 1000);

    expect(result.truncated).toBe(true);
    expect(result.content.length).toBeLessThanOrEqual(1000);
    expect(result.content.length).toBeGreaterThan(900);
    expect(result.content.startsWith("Paragraph 0 with some filler text.")).toBe(true);
  });

  it("never splits a surrogate pair when truncating", () => {
    document.body.innerHTML = `<main><p>${"a".repeat(999)}\u{1F600}${"b".repeat(1000)}</p></main>`;

    const result = extract("text", 1000);

    expect(result.truncated).toBe(true);
    expect(result.content).toBe("a".repeat(999));
  });

  it("uses no Markdown syntax in text format", () => {
    document.body.innerHTML =
      `<main><h1>Heading</h1><p>A <strong>bold</strong> <a href="/x?q=1">link</a> and <code>code</code>.</p>` +
      `<ul><li>One</li><li>Two<ul><li>Nested</li></ul></li></ul><ol><li>First</li></ol>` +
      `<table><tr><th>K</th><th>V</th></tr><tr><td>a</td><td>1</td></tr></table>` +
      `<blockquote>Quoted</blockquote><hr><pre>let x = 1;</pre></main>`;

    const content = extract("text").content;

    expect(content).toBe("Heading\n\nA bold link and code.\n\nOne\nTwo\n  Nested\n\nFirst\n\nK\tV\na\t1\n\nQuoted\n\nlet x = 1;");
    expect(content).not.toMatch(/^#|^- |^\d+\. |^>|\]\(|\*\*|`|\|/m);
  });

  it("keeps Markdown for emphasis, code, blockquotes and preformatted text", () => {
    document.body.innerHTML =
      "<main><p>A <strong>bold</strong> and <code>x</code> word.</p><blockquote><p>Quote one</p><p>Quote two</p></blockquote>" +
      "<pre>line 1\n  line 2</pre></main>";

    expect(extract().content).toBe("A **bold** and `x` word.\n\n> Quote one\n>\n> Quote two\n\n```\nline 1\n  line 2\n```");
  });

  it("drops hidden elements", () => {
    document.head.innerHTML = `<style>.gone{display:none}</style>`;
    document.body.innerHTML =
      `<main><p>Visible</p><p style="display:none">Display none</p><p hidden>Hidden attribute</p>` +
      `<p class=gone>Stylesheet hidden</p><div style="visibility:hidden">Invisible <span style="visibility:visible">but this shows</span></div>` +
      `<details><summary>Summary</summary>Collapsed body</details><noscript>No script</noscript><template>Template</template></main>`;

    const content = extract().content;

    expect(content).toBe("Visible\n\nbut this shows\n\nSummary");
  });

  it("never extracts form values or option text", () => {
    document.body.innerHTML =
      `<main><p>Form:</p><input value="typed value"><input type=password value="hunter2"><textarea>draft text</textarea>` +
      `<select><option>Option text</option></select><button>Submit</button><label>Label text</label></main>`;

    const content = extract().content;

    expect(content).toBe("Form:\n\nLabel text");
  });

  it("caps the title at 300 code points", () => {
    document.title = "t".repeat(299) + "\u{1F600}" + "tail";
    document.body.innerHTML = `<main>x</main>`;

    const result = extract();

    expect(result.title).toBe("t".repeat(299) + "\u{1F600}");
    expect(result.titleTruncated).toBe(true);
    document.title = "Short";
    expect(extract()).not.toHaveProperty("titleTruncated");
  });

  it("includes open shadow roots with their slotted light content", () => {
    document.body.innerHTML = `<main><p>Before</p><x-card><span>Light child</span></x-card></main>`;
    const host = document.querySelector("x-card") as HTMLElement;
    host.attachShadow({ mode: "open" }).innerHTML = `<style>p{color:red}</style><h2>Card title</h2><p><slot></slot></p>`;

    expect(extract().content).toBe("Before\n\n## Card title\n\nLight child");
  });

  it("appends the main content of same-origin iframes after the top document", () => {
    document.body.innerHTML = `<main><p>Top text</p><iframe></iframe><p>After frame</p><iframe style="display:none"></iframe></main>`;
    const [visible, hidden] = Array.from(document.querySelectorAll("iframe"));
    frameDocument(visible).body.innerHTML = `<nav>Frame nav</nav><article><h2>Frame article</h2></article>`;
    frameDocument(hidden).body.innerHTML = `<p>Hidden frame</p>`;

    expect(extract().content).toBe("Top text\n\nAfter frame\n\n## Frame article");
  });

  it("skips cross-origin iframes", () => {
    document.body.innerHTML = `<main><p>Top</p><iframe></iframe></main>`;
    Object.defineProperty(document.querySelector("iframe"), "contentDocument", { get: () => { throw new Error("cross-origin"); } });

    expect(extract().content).toBe("Top");
  });

  it("restricts extraction to a snapshot ref, including navigation it names", () => {
    document.body.innerHTML = `<main><p>Main</p></main><nav aria-label=Menu><a href="/one">One</a> <a href="/two">Two</a></nav>`;
    const snapshot = runPageScript("snapshot", [12000, 200], window) as { snapshotId: string; elements: { ref: string; tag: string }[] };
    const nav = document.querySelector("nav") as HTMLElement;
    nav.setAttribute("role", "navigation");
    const link = snapshot.elements.find((element) => element.tag === "a");
    if (!link) throw new Error("no link in snapshot");
    const state = (window as unknown as { __mincodeAiRefs: { refs: Map<string, Element> } }).__mincodeAiRefs;
    state.refs.set("nav", nav);

    expect(extract("markdown", 20000, snapshot.snapshotId, "nav").content).toBe(
      "[One](http://localhost:3000/one) [Two](http://localhost:3000/two)",
    );
    expect(extract("text", 20000, snapshot.snapshotId, link.ref).content).toBe("One");
  });

  it("applies the snapshot guards to ref scoping", () => {
    document.body.innerHTML = `<main><input type=password value=hunter2><input type=file><textarea>draft</textarea></main>`;
    const state = { snapshotId: "s1", refs: new Map<string, Element>() };
    const [password, file, textarea] = Array.from(document.querySelectorAll("input,textarea"));
    state.refs.set("p", password);
    state.refs.set("f", file);
    state.refs.set("t", textarea);
    state.refs.set("gone", document.createElement("div"));
    (window as unknown as { __mincodeAiRefs: unknown }).__mincodeAiRefs = state;

    expect(() => extract("markdown", 20000, "other", "p")).toThrow("stale snapshot; call browser.get_page_snapshot again");
    expect(() => extract("markdown", 20000, "s1", "gone")).toThrow("element reference is stale");
    expect(() => extract("markdown", 20000, "s1", "p")).toThrow("sensitive input interaction is blocked");
    expect(() => extract("markdown", 20000, "s1", "f")).toThrow("sensitive input interaction is blocked");
    expect(() => extract("markdown", 20000, "s1", "")).toThrow(/both snapshotId and ref/);
    expect(extract("markdown", 20000, "s1", "t").content).toBe("");
  });

  it("stops descending past the nesting limit and reports truncated", () => {
    let html = "<p>Top level</p>";
    for (let i = 0; i < 600; i++) html = `<div>${html}</div>`;
    document.body.innerHTML = `<main><p>Shallow</p>${html}</main>`;

    const result = extract();

    expect(result.content).toBe("Shallow");
    expect(result.truncated).toBe(true);
  }, DEEP_TREE_TIMEOUT);
});
