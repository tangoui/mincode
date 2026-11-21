const encoder = new TextEncoder();

export function utf8ByteLength(value: string) {
  return encoder.encode(value).byteLength;
}

/** Truncates UTF-8 text without splitting a Unicode code point. */
export function truncateUtf8(value: string, maxBytes: number, suffix = "") {
  if (maxBytes <= 0) return "";
  if (utf8ByteLength(value) <= maxBytes) return value;
  const suffixBytes = utf8ByteLength(suffix);
  const contentBudget = Math.max(0, maxBytes - suffixBytes);
  let used = 0;
  let output = "";
  for (const character of value) {
    const bytes = utf8ByteLength(character);
    if (used + bytes > contentBudget) break;
    output += character;
    used += bytes;
  }
  return output + (suffixBytes <= maxBytes ? suffix : "");
}

/** Retains newest items while enforcing both count and serialized UTF-8 budgets. */
export function retainWithinJsonBudget<T>(
  values: readonly T[],
  options: {
    maxItems: number;
    maxTotalBytes: number;
    normalize?: (value: T) => T;
  },
) {
  const retained: T[] = [];
  let totalBytes = 2; // []
  for (const value of values.slice(0, Math.max(0, options.maxItems))) {
    const normalized = options.normalize?.(value) ?? value;
    const itemBytes = utf8ByteLength(JSON.stringify(normalized)) + (retained.length ? 1 : 0);
    if (totalBytes + itemBytes > options.maxTotalBytes) break;
    retained.push(normalized);
    totalBytes += itemBytes;
  }
  return retained;
}
