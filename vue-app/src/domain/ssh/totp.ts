export interface TotpConfiguration {
  algorithm: "SHA-1" | "SHA-256" | "SHA-512";
  digits: number;
  period: number;
  secret: Uint8Array;
}

const DEFAULT_PERIOD = 30;
const DEFAULT_DIGITS = 6;
const SUPPORTED_ALGORITHMS = new Map<string, TotpConfiguration["algorithm"]>([
  ["SHA1", "SHA-1"],
  ["SHA256", "SHA-256"],
  ["SHA512", "SHA-512"],
]);

/**
 * Parses an otpauth TOTP URI without retaining its source string. The caller must
 * keep the URI confidential because it contains the shared authentication secret.
 */
export function parseTotpUri(uri: string): TotpConfiguration {
  let parsed: URL;
  try {
    parsed = new URL(uri.trim().replace(/^optauth:\/\//i, "otpauth://"));
  } catch {
    throw invalidTotpConfiguration();
  }

  if (parsed.protocol !== "otpauth:" || parsed.hostname.toLocaleLowerCase() !== "totp") {
    throw invalidTotpConfiguration();
  }

  const secret = decodeBase32(parsed.searchParams.get("secret") ?? "");
  const algorithm = parseAlgorithm(parsed.searchParams.get("algorithm"));
  const digits = parseIntegerParameter(parsed.searchParams.get("digits"), DEFAULT_DIGITS, 4, 10);
  const period = parseIntegerParameter(parsed.searchParams.get("period"), DEFAULT_PERIOD, 1, 300);

  return { secret, algorithm, digits, period };
}

/**
 * Parses either an otpauth URI (including the common `optauth://` typo)
 * or a standalone Base32 shared secret.
 */
export function parseTotpInput(value: string): TotpConfiguration {
  const input = value.trim();
  if (!input) throw invalidTotpConfiguration();
  if (/^(?:otpauth|optauth):\/\//i.test(input)) return parseTotpUri(input);

  return {
    secret: decodeBase32(input),
    algorithm: "SHA-1",
    digits: DEFAULT_DIGITS,
    period: DEFAULT_PERIOD,
  };
}

/** Generates the current one-time password from an otpauth URI or Base32 secret. */
export async function generateTotp(uri: string, now = Date.now()): Promise<string> {
  const configuration = parseTotpInput(uri);
  const counter = Math.floor(now / 1000 / configuration.period);
  if (!Number.isSafeInteger(counter) || counter < 0) throw invalidTotpConfiguration();

  const counterBuffer = new ArrayBuffer(8);
  new DataView(counterBuffer).setBigUint64(0, BigInt(counter));
  const secretBuffer = new ArrayBuffer(configuration.secret.byteLength);
  new Uint8Array(secretBuffer).set(configuration.secret);
  const key = await crypto.subtle.importKey(
    "raw",
    secretBuffer,
    { name: "HMAC", hash: configuration.algorithm },
    false,
    ["sign"]
  );
  const signature = new Uint8Array(await crypto.subtle.sign("HMAC", key, counterBuffer));
  const offset = signature[signature.length - 1]! & 0x0f;
  const binary =
    ((signature[offset]! & 0x7f) << 24) |
    (signature[offset + 1]! << 16) |
    (signature[offset + 2]! << 8) |
    signature[offset + 3]!;

  return String(binary % 10 ** configuration.digits).padStart(configuration.digits, "0");
}

function parseAlgorithm(value: string | null): TotpConfiguration["algorithm"] {
  if (!value) return "SHA-1";
  const algorithm = SUPPORTED_ALGORITHMS.get(value.replace(/[-_\s]/g, "").toUpperCase());
  if (!algorithm) throw invalidTotpConfiguration();
  return algorithm;
}

function parseIntegerParameter(
  value: string | null,
  fallback: number,
  minimum: number,
  maximum: number
) {
  if (!value) return fallback;
  if (!/^\d+$/.test(value)) throw invalidTotpConfiguration();
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw invalidTotpConfiguration();
  }
  return parsed;
}

function decodeBase32(value: string) {
  const normalized = value.replace(/[\s-]/g, "").replace(/=+$/g, "").toUpperCase();
  if (!normalized || !/^[A-Z2-7]+$/.test(normalized)) throw invalidTotpConfiguration();

  let bits = 0;
  let bitCount = 0;
  const bytes: number[] = [];
  for (const character of normalized) {
    const digit = base32Digit(character);
    bits = (bits << 5) | digit;
    bitCount += 5;
    if (bitCount >= 8) {
      bytes.push((bits >>> (bitCount - 8)) & 0xff);
      bitCount -= 8;
    }
  }
  if (!bytes.length) throw invalidTotpConfiguration();
  return new Uint8Array(bytes);
}

function base32Digit(character: string) {
  const code = character.charCodeAt(0);
  return code >= 65 && code <= 90 ? code - 65 : code - 24;
}

function invalidTotpConfiguration() {
  return new Error("Invalid TOTP configuration.");
}
