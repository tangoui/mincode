/**
 * Streaming test runs (testProviders debugRun, host 0.3.0): re-validation of
 * the `plugin:test-run-event` payloads the Go host forwards
 * (backend/plugin/test_runs.go) and the pure merge of their events
 * into one run's state: a tree of nodes (seeded from discovery and grown by
 * `tree` events, e.g. parameterized invocations), per-node status, duration,
 * message, expected/actual values, trace and output, the run's output, a
 * summary and the ids to re-run after failures. Events may arrive repeated
 * or out of order (a result before its `tree` node creates a placeholder the
 * node later fills). A run ends with the host's `ended` event; without a
 * `finished` event before it, tests that never got a result are marked
 * terminated. The node count is capped (runNodesMax, also enforced by the
 * host), beyond which events are dropped and the run is marked truncated.
 * No DOM, Vue or IPC here: services/plugin-test-runs.ts and
 * stores/test-runs.ts hold the runtime wiring.
 */
import { isProjectRelativePath, isProviderItemId, isRecord, boundedLine, utf8Bytes } from "@/domain/plugins/provider-points";
import { sanitizeTestText, TEST_PROVIDER_LIMITS, type TestItem } from "@/domain/plugins/test-providers";

export const TEST_RUN_RESULT_TYPES = ["passed", "failed", "errored", "skipped"] as const;
export type TestRunResultType = (typeof TEST_RUN_RESULT_TYPES)[number];
const RESULT_TYPES: ReadonlySet<string> = new Set(TEST_RUN_RESULT_TYPES);

/** Why a run ended (host reasons; an unknown one is kept as text). */
export type TestRunEndReason = "finished" | "sessionEnded" | "superseded" | "stopped" | "pluginStopped" | "expired" | "failed" | "cancelled";

export type TestRunEvent =
  | { type: "tree"; id: string; parentId?: string; label: string; kind: "suite" | "test"; path?: string; line?: number }
  | { type: "started"; id: string }
  | { type: TestRunResultType; id: string; durationMs?: number; message?: string; expected?: string; actual?: string; trace?: string }
  | { type: "output"; id?: string; text: string }
  | { type: "finished"; durationMs?: number }
  | { type: "truncated" }
  | { type: "ended"; finished: boolean; reason: string };

export interface TestRunEventPayload {
  runId: string;
  pluginId: string;
  events: TestRunEvent[];
  /** Events dropped by this re-validation. */
  dropped: number;
}

/** Bounds of the frontend state (the host enforces the per-event ones first). */
export const TEST_RUN_LIMITS = {
  nodesMax: TEST_PROVIDER_LIMITS.runNodesMax,
  traceMaxBytes: TEST_PROVIDER_LIMITS.traceMaxBytes,
  outputTextMaxBytes: TEST_PROVIDER_LIMITS.outputTextMaxBytes,
  /** A node's collected output, and the run's (the newest characters are kept). */
  nodeOutputMaxChars: 65536,
  runOutputMaxChars: 1_000_000,
  runIdMaxLength: 64,
  eventsPerPayloadMax: 1000,
  /** Parent links followed when checking for a cycle (deeper trees are not re-parented). */
  depthMax: 256,
} as const;

const RUN_ID_PATTERN = /^run-[0-9a-f]{1,60}$/;

function boundedText(value: unknown, maxBytes: number): string | undefined | false {
  if (value === undefined) return undefined;
  if (typeof value !== "string") return false;
  const { text } = sanitizeTestText(value, maxBytes);
  if (utf8Bytes(text) <= maxBytes) return text;
  // Cut at a character boundary.
  let cut = text.slice(0, maxBytes);
  while (utf8Bytes(cut) > maxBytes) cut = cut.slice(0, -1);
  return cut;
}

function duration(value: unknown): number | undefined | false {
  if (value === undefined) return undefined;
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0 || value > TEST_PROVIDER_LIMITS.durationMsMax) return false;
  return Math.round(value);
}

/** Re-validates one event; undefined drops it. */
export function parseTestRunEvent(raw: unknown): TestRunEvent | undefined {
  if (!isRecord(raw) || typeof raw.type !== "string") return undefined;
  const id = raw.id === undefined ? undefined : isProviderItemId(raw.id, TEST_PROVIDER_LIMITS.testIdMaxLength) ? raw.id : false;
  if (id === false) return undefined;
  switch (raw.type) {
    case "tree": {
      const label = boundedLine(raw.label, TEST_PROVIDER_LIMITS.labelMaxRunes, true);
      const parentId = raw.parentId === undefined ? undefined : isProviderItemId(raw.parentId, TEST_PROVIDER_LIMITS.testIdMaxLength) ? raw.parentId : false;
      const path = raw.path === undefined ? undefined : isProjectRelativePath(raw.path, TEST_PROVIDER_LIMITS.pathMaxLength) ? raw.path : false;
      const line = raw.line;
      const badLine = line !== undefined && (typeof line !== "number" || !Number.isInteger(line) || line < 1 || line > TEST_PROVIDER_LIMITS.lineMax || !path);
      if (!id || !label || parentId === false || parentId === id || path === false || badLine || (raw.kind !== "suite" && raw.kind !== "test")) return undefined;
      return { type: "tree", id, label, kind: raw.kind, ...(parentId ? { parentId } : {}), ...(path ? { path } : {}), ...(typeof line === "number" ? { line } : {}) };
    }
    case "started":
      return id ? { type: "started", id } : undefined;
    case "output": {
      const text = boundedText(raw.text, TEST_RUN_LIMITS.outputTextMaxBytes);
      return text ? { type: "output", text, ...(id ? { id } : {}) } : undefined;
    }
    case "finished": {
      const durationMs = duration(raw.durationMs);
      return durationMs === false ? undefined : { type: "finished", ...(durationMs !== undefined ? { durationMs } : {}) };
    }
    case "truncated":
      return { type: "truncated" };
    case "ended":
      return typeof raw.finished === "boolean" && typeof raw.reason === "string" && raw.reason.length <= 64
        ? { type: "ended", finished: raw.finished, reason: raw.reason }
        : undefined;
    default: {
      if (!RESULT_TYPES.has(raw.type) || !id) return undefined;
      const durationMs = duration(raw.durationMs);
      const message = boundedText(raw.message, TEST_PROVIDER_LIMITS.messageMaxRunes * 4);
      const expected = boundedText(raw.expected, TEST_RUN_LIMITS.traceMaxBytes);
      const actual = boundedText(raw.actual, TEST_RUN_LIMITS.traceMaxBytes);
      const trace = boundedText(raw.trace, TEST_RUN_LIMITS.traceMaxBytes);
      if (durationMs === false || message === false || expected === false || actual === false || trace === false) return undefined;
      return {
        type: raw.type as TestRunResultType,
        id,
        ...(durationMs !== undefined ? { durationMs } : {}),
        ...(message ? { message: sanitizeTestText(message, TEST_PROVIDER_LIMITS.messageMaxRunes).text } : {}),
        ...(expected !== undefined ? { expected } : {}),
        ...(actual !== undefined ? { actual } : {}),
        ...(trace ? { trace } : {}),
      };
    }
  }
}

/** Re-validates a `plugin:test-run-event` payload; null when its envelope is invalid. */
export function parseTestRunEventPayload(value: unknown): TestRunEventPayload | null {
  if (!isRecord(value) || typeof value.runId !== "string" || !RUN_ID_PATTERN.test(value.runId) ||
    typeof value.pluginId !== "string" || !value.pluginId || !Array.isArray(value.events)) return null;
  const payload: TestRunEventPayload = { runId: value.runId, pluginId: value.pluginId, events: [], dropped: 0 };
  for (const raw of value.events.slice(0, TEST_RUN_LIMITS.eventsPerPayloadMax)) {
    const event = parseTestRunEvent(raw);
    if (event) payload.events.push(event);
    else payload.dropped += 1;
  }
  payload.dropped += Math.max(0, value.events.length - TEST_RUN_LIMITS.eventsPerPayloadMax);
  return payload;
}

/** A node's own status; "terminated" means the run ended before its result. */
export type TestNodeStatus = "pending" | "running" | TestRunResultType | "terminated";

export interface TestRunNode {
  id: string;
  label: string;
  /** "unknown" until a tree event or the discovered tree says (a result may come first). */
  kind: "suite" | "test" | "unknown";
  parentId?: string;
  path?: string;
  line?: number;
  children: string[];
  status: TestNodeStatus;
  durationMs?: number;
  message?: string;
  expected?: string;
  actual?: string;
  trace?: string;
  output?: string;
}

export type TestRunPhase = "preparing" | "running" | "ended";

export interface TestRunState {
  runId: string;
  pluginId: string;
  providerId: string;
  projectRoot: string;
  /** Debugging (true) or a run without debugging. */
  debug: boolean;
  /** The requested ids ([] = all tests). */
  testIds: string[];
  sessionId?: string;
  phase: TestRunPhase;
  /** The user stopped the run; it ends once the host's grace period is over. */
  stopping: boolean;
  /** The plugin reported finished. */
  finished: boolean;
  endReason?: string;
  durationMs?: number;
  nodes: Record<string, TestRunNode>;
  /** Number of nodes (kept so adding a node is O(1)). */
  nodeCount: number;
  /**
   * Nodes created from the discovered tree (seedTestRunNodes). Only the
   * others count against nodesMax, like the host, which budgets only the ids
   * the plugin publishes; discovery has its own limit.
   */
  seededCount: number;
  /** Top-level node ids in order (recomputed once per merged batch). */
  roots: string[];
  output: string;
  outputTruncated: boolean;
  /** Nodes were dropped over the cap (by the host or here). */
  truncated: boolean;
  /** Events the frontend dropped as invalid. */
  dropped: number;
  startedAt: number;
  endedAt?: number;
}

export interface TestRunInit {
  runId: string;
  pluginId: string;
  providerId: string;
  projectRoot: string;
  debug: boolean;
  testIds: readonly string[];
  startedAt: number;
}

export function createTestRunState(init: TestRunInit): TestRunState {
  return {
    runId: init.runId,
    pluginId: init.pluginId,
    providerId: init.providerId,
    projectRoot: init.projectRoot,
    debug: init.debug,
    testIds: [...init.testIds],
    phase: "preparing",
    stopping: false,
    finished: false,
    nodes: {},
    nodeCount: 0,
    seededCount: 0,
    roots: [],
    output: "",
    outputTruncated: false,
    truncated: false,
    dropped: 0,
    startedAt: init.startedAt,
  };
}

/** The node with id, created as a root placeholder when new (undefined over the cap of nodes not seeded). */
function ensureNode(state: TestRunState, id: string, seeded = false): TestRunNode | undefined {
  const existing = state.nodes[id];
  if (existing) return existing;
  if (!seeded && state.nodeCount - state.seededCount >= TEST_RUN_LIMITS.nodesMax) {
    state.truncated = true;
    return undefined;
  }
  const node: TestRunNode = { id, label: id, kind: "unknown", children: [], status: "pending" };
  state.nodes[id] = node;
  state.nodeCount += 1;
  if (seeded) state.seededCount += 1;
  state.roots.push(id);
  return node;
}

function isAncestor(state: TestRunState, candidate: string, of: string): boolean {
  let current: string | undefined = of;
  for (let guard = 0; current !== undefined; guard += 1) {
    // A chain deeper than depthMax counts as a cycle: never attach below it.
    if (current === candidate || guard > TEST_RUN_LIMITS.depthMax) return true;
    current = state.nodes[current]?.parentId;
  }
  return false;
}

/**
 * Moves node under parentId unless it already has a parent or that would make
 * a cycle (or the chain is deeper than depthMax). state.roots keeps the id
 * until pruneRoots runs at the end of the batch.
 */
function attach(state: TestRunState, node: TestRunNode, parentId: string) {
  if (node.parentId !== undefined || isAncestor(state, node.id, parentId)) return;
  const parent = ensureNode(state, parentId);
  if (!parent) return;
  if (parent.kind === "unknown") parent.kind = "suite";
  node.parentId = parentId;
  parent.children.push(node.id);
}

/** Drops ids that got a parent from state.roots (once per batch, O(n)). */
function pruneRoots(state: TestRunState) {
  if (state.roots.some((id) => state.nodes[id]?.parentId !== undefined)) {
    state.roots = state.roots.filter((id) => state.nodes[id]?.parentId === undefined);
  }
}

function appendCapped(current: string, text: string, max: number): { text: string; cut: boolean } {
  const next = current + text;
  return next.length <= max ? { text: next, cut: false } : { text: next.slice(next.length - max), cut: true };
}

const TERMINAL: ReadonlySet<TestNodeStatus> = new Set(["passed", "failed", "errored", "skipped", "terminated"]);

/**
 * Seeds the run's tree from a discovered (sub)tree: nodes are pending
 * until results arrive. Existing nodes keep their state.
 */
export function seedTestRunNodes(state: TestRunState, items: readonly TestItem[]): TestRunState {
  seedLevel(state, items, undefined, 0);
  pruneRoots(state);
  return state;
}

function seedLevel(state: TestRunState, items: readonly TestItem[], parentId: string | undefined, depth: number) {
  if (depth > TEST_RUN_LIMITS.depthMax) return;
  for (const item of items) {
    const existed = item.id in state.nodes;
    const node = ensureNode(state, item.id, true);
    if (!node) return;
    if (!existed || node.kind === "unknown") {
      node.label = item.label;
      node.kind = item.children?.length ? "suite" : "test";
      if (item.path) node.path = item.path;
      if (item.line) node.line = item.line;
    }
    if (parentId) attach(state, node, parentId);
    if (item.children?.length) seedLevel(state, item.children, item.id, depth + 1);
  }
}

/** Merges events into the run (in place) and returns it. Events after `ended` are ignored. */
export function applyTestRunEvents(state: TestRunState, events: readonly TestRunEvent[]): TestRunState {
  for (const event of events) {
    if (state.phase === "ended") break;
    switch (event.type) {
      case "tree": {
        const node = ensureNode(state, event.id);
        if (!node) break;
        // A known node keeps its first (discovered) label: a runtime may spell
        // the same test differently. A placeholder (labelled by its id) takes it.
        if (node.kind === "unknown" || node.label === node.id) node.label = event.label;
        node.kind = event.kind;
        if (event.path) node.path = event.path;
        if (event.line) node.line = event.line;
        if (event.parentId) attach(state, node, event.parentId);
        break;
      }
      case "started": {
        const node = ensureNode(state, event.id);
        if (node && !TERMINAL.has(node.status)) node.status = "running";
        break;
      }
      case "passed":
      case "failed":
      case "errored":
      case "skipped": {
        const node = ensureNode(state, event.id);
        if (!node) break;
        node.status = event.type;
        node.durationMs = event.durationMs;
        node.message = event.message;
        node.expected = event.expected;
        node.actual = event.actual;
        node.trace = event.trace;
        break;
      }
      case "output": {
        const run = appendCapped(state.output, event.text, TEST_RUN_LIMITS.runOutputMaxChars);
        state.output = run.text;
        state.outputTruncated ||= run.cut;
        const node = event.id ? state.nodes[event.id] : undefined;
        if (node) node.output = appendCapped(node.output ?? "", event.text, TEST_RUN_LIMITS.nodeOutputMaxChars).text;
        break;
      }
      case "finished":
        state.finished = true;
        if (event.durationMs !== undefined) state.durationMs = event.durationMs;
        break;
      case "truncated":
        state.truncated = true;
        break;
      case "ended":
        endTestRun(state, event.reason, event.finished);
        break;
    }
  }
  pruneRoots(state);
  return state;
}

/**
 * Ends the run: without a finished report, every node still pending or
 * running is marked terminated (also when the frontend ends a run itself,
 * e.g. the launch was declined or failed).
 */
export function endTestRun(state: TestRunState, reason: string, finished = false, now = Date.now()): TestRunState {
  if (state.phase === "ended") return state;
  state.finished ||= finished;
  state.phase = "ended";
  state.stopping = false;
  state.endReason = reason;
  state.endedAt = now;
  if (!state.finished) {
    for (const node of Object.values(state.nodes)) {
      if (node.status === "pending" || node.status === "running") node.status = "terminated";
    }
  }
  return state;
}

/** A leaf: a test, or a node of unknown kind without children. */
function isLeaf(node: TestRunNode) {
  return node.kind === "test" || (node.kind === "unknown" && node.children.length === 0);
}

export interface TestRunSummary {
  total: number;
  passed: number;
  failed: number;
  errored: number;
  skipped: number;
  terminated: number;
  running: number;
  pending: number;
}

/** Counts of the run's leaf tests by status. */
export function summarizeTestRun(state: TestRunState): TestRunSummary {
  const summary: TestRunSummary = { total: 0, passed: 0, failed: 0, errored: 0, skipped: 0, terminated: 0, running: 0, pending: 0 };
  for (const node of Object.values(state.nodes)) {
    if (!isLeaf(node)) continue;
    summary.total += 1;
    summary[node.status] += 1;
  }
  return summary;
}

const AGGREGATE_ORDER: TestNodeStatus[] = ["running", "errored", "failed", "terminated", "pending", "passed", "skipped"];

/**
 * Every node's displayed status in one pass: a node's own failure, else the
 * most significant status of itself and its subtree (running, errored,
 * failed, terminated, pending, passed, skipped).
 */
export function aggregateTestRunStatuses(state: TestRunState): Map<string, TestNodeStatus> {
  const out = new Map<string, TestNodeStatus>();
  for (const id of state.roots) aggregateNode(state, id, 0, out);
  return out;
}

function aggregateNode(state: TestRunState, id: string, depth: number, out: Map<string, TestNodeStatus>): TestNodeStatus {
  const node = state.nodes[id]!;
  const own = node.status;
  if (own === "failed" || own === "errored" || node.children.length === 0 || depth > TEST_RUN_LIMITS.nodesMax) {
    for (const child of node.children) aggregateNode(state, child, depth + 1, out);
    out.set(id, own);
    return own;
  }
  const statuses = new Set<TestNodeStatus>(node.children.map((child) => aggregateNode(state, child, depth + 1, out)));
  // A suite's own "pending" only means it has no result of its own.
  if (own !== "pending") statuses.add(own);
  const status = AGGREGATE_ORDER.find((candidate) => statuses.has(candidate)) ?? own;
  out.set(id, status);
  return status;
}

/**
 * One node's displayed status (as aggregateTestRunStatuses, but walking only
 * its subtree); undefined when the run has no such node.
 */
export function testNodeDisplayStatus(state: TestRunState, id: string): TestNodeStatus | undefined {
  return state.nodes[id] ? aggregateNode(state, id, 0, new Map()) : undefined;
}

/** A row of the Tests panel's tree for a run. */
export interface TestRunTreeRow {
  id: string;
  label: string;
  status: TestNodeStatus;
  durationMs?: number;
  children?: TestRunTreeRow[];
}

/** The run as nested tree rows with displayed statuses, and the ids of rows that have children. */
export function testRunTreeRows(state: TestRunState): { rows: TestRunTreeRow[]; parentIds: string[] } {
  const statuses = aggregateTestRunStatuses(state);
  const parentIds: string[] = [];
  const build = (id: string, depth: number): TestRunTreeRow | undefined => {
    const node = state.nodes[id];
    if (!node) return undefined;
    const row: TestRunTreeRow = { id, label: node.label, status: statuses.get(id) ?? node.status };
    if (node.durationMs !== undefined) row.durationMs = node.durationMs;
    if (node.children.length && depth < TEST_RUN_LIMITS.depthMax) {
      parentIds.push(id);
      row.children = node.children.map((child) => build(child, depth + 1)).filter((child): child is TestRunTreeRow => Boolean(child));
    }
    return row;
  };
  const rows = state.roots.map((id) => build(id, 0)).filter((row): row is TestRunTreeRow => Boolean(row));
  return { rows, parentIds };
}

/**
 * The ids to re-run after failures, in tree order: each failed or errored
 * node without a failed or errored descendant (a test, or a suite whose
 * setup failed). More than `max` (a run's test id limit) collapse into their
 * parents, largest sibling groups first (e.g. thousands of failed
 * parameterized invocations rerun as their test), so no failure is left out.
 */
export function failedTestIds(state: TestRunState, max: number = TEST_PROVIDER_LIMITS.runTestsMax): string[] {
  return collapseIntoParents(state, mostSpecificFailures(state), max);
}

function collapseIntoParents(state: TestRunState, ids: string[], max: number): string[] {
  let current = ids;
  while (current.length > max) {
    const groups = new Map<string, string[]>();
    for (const id of current) {
      const parentId = state.nodes[id]?.parentId;
      if (parentId && state.nodes[parentId]) groups.set(parentId, [...(groups.get(parentId) ?? []), id]);
    }
    const largest = [...groups].filter(([, members]) => members.length > 1).sort((a, b) => b[1].length - a[1].length);
    if (!largest.length) break;
    const into = new Map<string, string>();
    let count = current.length;
    for (const [parentId, members] of largest) {
      if (count <= max) break;
      for (const member of members) into.set(member, parentId);
      count -= members.length - 1;
    }
    const next: string[] = [];
    const seen = new Set<string>();
    for (const id of current) {
      const kept = into.get(id) ?? id;
      if (!seen.has(kept)) {
        seen.add(kept);
        next.push(kept);
      }
    }
    current = next;
  }
  return current;
}

function mostSpecificFailures(state: TestRunState): string[] {
  const ids: string[] = [];
  const visit = (id: string, depth: number): boolean => {
    const node = state.nodes[id];
    if (!node || depth > TEST_RUN_LIMITS.nodesMax) return false;
    let below = false;
    for (const child of node.children) below = visit(child, depth + 1) || below;
    const own = node.status === "failed" || node.status === "errored";
    if (own && !below) ids.push(id);
    return own || below;
  };
  for (const id of state.roots) visit(id, 0);
  return ids;
}
