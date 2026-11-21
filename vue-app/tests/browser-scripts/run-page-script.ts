// Runs the page scripts that the Go backend injects into embedded browser tabs
// (backend/browser/scripts/*.js) inside the test DOM. Name resolution
// and placeholder expansion mirror pageScript/expandPageScript in
// backend/browser/page_scripts.go; keep the two in sync.

const sources = import.meta.glob<string>("../../../backend/browser/scripts/*.js", {
  query: "?raw",
  import: "default",
  eager: true,
});

const scripts = new Map(
  Object.entries(sources).map(([file, source]) => [file.replace(/^.*\/([^/]+)\.js$/, "$1"), source.trim()]),
);

const ACTIONS_FILE = "actions";
const PLACEHOLDER = /__ARG(\d+)__/g;

// Go's json.Marshal additionally escapes <, >, & and U+2028/U+2029 (which JavaScript treats as
// line terminators). These characters only occur inside JSON strings, so a global replace is safe.
const GO_JSON_ESCAPED = new RegExp(`[<>&${String.fromCharCode(0x2028, 0x2029)}]`, "g");

/** JSON-encodes `value` byte-for-byte like Go's json.Marshal (for JSON-compatible values). */
export function goJsonEncode(value: unknown): string | undefined {
  return JSON.stringify(value)?.replace(
    GO_JSON_ESCAPED,
    (char) => String.fromCharCode(92) + "u" + char.charCodeAt(0).toString(16).padStart(4, "0"),
  );
}

/**
 * Replaces each __ARGn__ with goJsonEncode(args[n]) in one pass, so __ARG10__
 * is never read as __ARG1__ and placeholder-like text inside an argument is not
 * expanded again. Throws when a placeholder has no argument or an argument is unused.
 */
export function expandPageScript(name: string, source: string, args: unknown[]): string {
  const used = args.map(() => false);
  const result = source.replace(PLACEHOLDER, (match, digits: string) => {
    const index = Number(digits);
    if (index >= args.length) {
      throw new Error(`page script ${name} placeholder ${match} has no argument (${args.length} given)`);
    }
    used[index] = true;
    const encoded = goJsonEncode(args[index]);
    if (encoded === undefined) throw new Error(`page script ${name} argument ${index} is not JSON-encodable`);
    return encoded;
  });
  const unused = used.indexOf(false);
  if (unused >= 0) throw new Error(`page script ${name} does not use argument ${unused} (__ARG${unused}__)`);
  return result;
}

/** Builds the script text for `name` exactly as the Go side would. */
export function pageScriptSource(name: string, args: unknown[]): string {
  const direct = name === ACTIONS_FILE ? undefined : scripts.get(name);
  if (direct !== undefined) return expandPageScript(name, direct, args);
  const actions = scripts.get(ACTIONS_FILE);
  if (actions === undefined) throw new Error("scripts/actions.js is missing");
  // actions.js validates the action name and its argument count itself.
  return expandPageScript(name, actions, [name, args]);
}

/** Evaluates the page script in `window`'s global scope and returns its value. */
export function runPageScript(name: string, args: unknown[], window: Window): unknown {
  const script = pageScriptSource(name, args);
  // Sloppy-mode `with` makes `document`, `location`, `getComputedStyle`... resolve on the given window.
  const evaluate = new Function("window", `with (window) { return (${script}\n); }`) as (target: Window) => unknown;
  return evaluate(window);
}
