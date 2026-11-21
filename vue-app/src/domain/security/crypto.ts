/**
 * 文本编码与 AES-GCM 口令加密的纯逻辑。
 *
 * Base64 供 AI 文本转换能力使用；AES 用于配置同步等需要口令加密的持久化通道，
 * 密文格式为 `mc-aes-v1:<salt>:<iv>:<cipher>`（PBKDF2-SHA256 / 150k 迭代）。
 */

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function base64ToBytes(value: string): Uint8Array {
  const normalized = value.replace(/\s+/g, "");
  const binary = atob(normalized);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

export function encodeBase64(value: string): string {
  return bytesToBase64(new TextEncoder().encode(value));
}

export function decodeBase64(value: string): string {
  return new TextDecoder("utf-8", { fatal: true }).decode(base64ToBytes(value));
}

const aesPrefix = "mc-aes-v1";

async function deriveAesKey(password: string, salt: Uint8Array, usage: KeyUsage[]): Promise<CryptoKey> {
  const material = await crypto.subtle.importKey("raw", new TextEncoder().encode(password), "PBKDF2", false, ["deriveKey"]);
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", hash: "SHA-256", salt: salt as BufferSource, iterations: 150_000 },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    usage,
  );
}

export async function encryptAes(value: string, password: string): Promise<string> {
  if (!password) throw new Error("Password is required");
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const key = await deriveAesKey(password, salt, ["encrypt"]);
  const cipher = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(value));
  return [aesPrefix, bytesToBase64(salt), bytesToBase64(iv), bytesToBase64(new Uint8Array(cipher))].join(":");
}

export async function decryptAes(value: string, password: string): Promise<string> {
  if (!password) throw new Error("Password is required");
  const [prefix, saltText, ivText, cipherText, extra] = value.trim().split(":");
  if (prefix !== aesPrefix || !saltText || !ivText || !cipherText || extra !== undefined) throw new Error("Unsupported encrypted payload format");
  const salt = base64ToBytes(saltText);
  const iv = base64ToBytes(ivText);
  if (salt.length !== 16 || iv.length !== 12) throw new Error("Encrypted payload has invalid salt or IV");
  const key = await deriveAesKey(password, salt, ["decrypt"]);
  try {
    const plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: iv as BufferSource }, key, base64ToBytes(cipherText) as BufferSource);
    return new TextDecoder("utf-8", { fatal: true }).decode(plain);
  } catch {
    throw new Error("Decryption failed: password or payload is invalid");
  }
}
