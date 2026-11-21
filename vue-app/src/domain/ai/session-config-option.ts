// An ACP Agent declares its own config options, so there is no fixed model catalog to validate against.
export interface AcpConfigSelectChoice {
  value: string;
  label: string;
  description?: string;
}

export interface AcpConfigSelectOption {
  id: string;
  name: string;
  category?: string;
  currentValue: string;
  choices: AcpConfigSelectChoice[];
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function trimmedString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function parseChoice(value: unknown): AcpConfigSelectChoice | null {
  const record = asRecord(value);
  const choiceValue = trimmedString(record.value);
  if (!choiceValue) return null;
  const description = trimmedString(record.description);
  return {
    value: choiceValue,
    label: trimmedString(record.name) || choiceValue,
    ...(description ? { description } : {}),
  };
}

// `options` is either a flat choice list or a list of `{group, name, options}` headers.
function parseChoices(value: unknown): AcpConfigSelectChoice[] {
  if (!Array.isArray(value)) return [];
  const choices: AcpConfigSelectChoice[] = [];
  for (const entry of value) {
    const record = asRecord(entry);
    if (Array.isArray(record.options)) {
      for (const nested of record.options) {
        const choice = parseChoice(nested);
        if (choice) choices.push(choice);
      }
      continue;
    }
    const choice = parseChoice(entry);
    if (choice) choices.push(choice);
  }
  return choices;
}

/**
 * Keeps only the `select` options the drawer can render. Boolean options are
 * still unstable in the ACP schema and have no UI yet, so they are dropped
 * rather than shown as a broken control.
 */
export function parseAcpConfigSelectOptions(value: unknown): AcpConfigSelectOption[] {
  if (!Array.isArray(value)) return [];
  const options: AcpConfigSelectOption[] = [];
  for (const entry of value) {
    const record = asRecord(entry);
    if (trimmedString(record.type) !== "select") continue;
    const id = trimmedString(record.id);
    const choices = parseChoices(record.options);
    if (!id || !choices.length) continue;
    const category = trimmedString(record.category);
    options.push({
      id,
      name: trimmedString(record.name) || id,
      ...(category ? { category } : {}),
      currentValue: trimmedString(record.currentValue),
      choices,
    });
  }
  return options;
}

/**
 * The model and thought-level pickers belong to one decision, so the composer
 * shows them as sections of a single dropdown instead of two adjacent selects.
 * Merging a lone option would just make it harder to reach, so it stays flat.
 */
export function groupAcpConfigSelectOptions(options: AcpConfigSelectOption[]) {
  const merged = options.filter(
    (option) => option.category === "model" || option.category === "thought_level",
  );
  if (merged.length < 2) return { merged: [], standalone: options };
  return { merged, standalone: options.filter((option) => !merged.includes(option)) };
}

export function acpConfigCurrentLabel(option: AcpConfigSelectOption) {
  return option.choices.find((choice) => choice.value === option.currentValue)?.label ?? "";
}
