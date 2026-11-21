import {
  describeAiAgentEventActivity,
  formatAiAgentEventDetail,
  type AiAgentEventActivity,
  type AiAgentEventActivityInput,
} from "@/domain/ai/agent-event-activity";
import { parseAiEventPresentation, type AiEventPresentation } from "@/domain/ai/event-presentations";

export interface AiAgentEventView {
  presentation: AiEventPresentation;
  activity: AiAgentEventActivity;
  hasPresentationDetail: boolean;
  /** Pretty-printed result text; computed on first read because only expanded items need it. */
  readonly formattedDetail: string;
}

interface CacheEntry {
  input: AiAgentEventActivityInput;
  view: AiAgentEventView;
}

// Keyed by event object so entries die with the event; the input snapshot guards against
// in-place updates (streamed detail, status transitions) returning a stale view.
const cache = new WeakMap<object, CacheEntry>();

function snapshotOf(event: AiAgentEventActivityInput): AiAgentEventActivityInput {
  return {
    kind: event.kind,
    title: event.title,
    detail: event.detail,
    status: event.status,
    toolName: event.toolName,
    toolInput: event.toolInput,
  };
}

function sameInput(a: AiAgentEventActivityInput, b: AiAgentEventActivityInput) {
  return a.kind === b.kind
    && a.title === b.title
    && a.detail === b.detail
    && a.status === b.status
    && a.toolName === b.toolName
    && a.toolInput === b.toolInput;
}

function createView(event: AiAgentEventActivityInput): AiAgentEventView {
  const presentation = parseAiEventPresentation(event.detail);
  let formatted: string | undefined;
  return {
    presentation,
    activity: describeAiAgentEventActivity(event),
    hasPresentationDetail: Boolean(
      presentation.text
      || presentation.steps.length
      || presentation.sources.length
      || presentation.artifacts.length,
    ),
    get formattedDetail() {
      formatted ??= formatAiAgentEventDetail(presentation.text);
      return formatted;
    },
  };
}

export function aiAgentEventView(event: AiAgentEventActivityInput): AiAgentEventView {
  const input = snapshotOf(event);
  const cached = cache.get(event);
  if (cached && sameInput(cached.input, input)) return cached.view;
  const view = createView(input);
  cache.set(event, { input, view });
  return view;
}
