export const COMPUTER_USE_ACTION_TYPES = [
  "screenshot",
  "click",
  "double_click",
  "scroll",
  "type",
  "keypress",
  "move",
  "drag",
  "wait",
] as const;

export type ComputerUseActionType = (typeof COMPUTER_USE_ACTION_TYPES)[number];

const coordinate = { type: "number", minimum: 0 } as const;
const mouseButton = { type: "string", enum: ["left", "right", "wheel"] } as const;
const modifierKeys = {
  type: "array",
  maxItems: 4,
  items: { type: "string", enum: ["CTRL", "SHIFT", "ALT", "META"] },
} as const;

function actionSchema(actionTypes: readonly string[]) {
  return {
    type: "object",
    additionalProperties: false,
    required: ["type"],
    properties: {
      type: { type: "string", enum: [...actionTypes] },
      x: coordinate,
      y: coordinate,
      button: mouseButton,
      text: { type: "string", maxLength: 20_000 },
      keys: {
        type: "array",
        minItems: 1,
        maxItems: 16,
        items: { type: "string", minLength: 1, maxLength: 80 },
      },
      modifiers: modifierKeys,
      scroll_x: { type: "number", minimum: -100_000, maximum: 100_000 },
      scroll_y: { type: "number", minimum: -100_000, maximum: 100_000 },
      path: {
        type: "array",
        minItems: 2,
        maxItems: 128,
        items: {
          type: "object",
          additionalProperties: false,
          required: ["x", "y"],
          properties: { x: coordinate, y: coordinate },
        },
      },
      durationMs: { type: "number", minimum: 0, maximum: 10_000 },
    },
  } as const;
}

function batchSchema(actionItems: object) {
  return {
    actions: {
      type: "array",
      minItems: 1,
      maxItems: 32,
      items: actionItems,
    },
    captureAfter: {
      type: "boolean",
      description: "Capture and attach the updated screen after all actions. Defaults to true.",
    },
    timeoutMs: { type: "number", minimum: 1_000, maximum: 30_000 },
  } as const;
}

export const computerUseActionSchema = actionSchema(COMPUTER_USE_ACTION_TYPES);
export const computerUseBatchSchema = batchSchema(computerUseActionSchema);
export interface ComputerUseActionSummary {
  count: number;
  labels: string[];
}

function actionRecord(value: unknown) {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

export function summarizeComputerUseActions(value: unknown): ComputerUseActionSummary {
  if (!Array.isArray(value)) return { count: 0, labels: [] };
  const labels = value.flatMap((item) => {
    const action = actionRecord(item);
    const type = typeof action?.type === "string" ? action.type.trim() : "";
    return type ? [type] : [];
  });
  return { count: labels.length, labels };
}
