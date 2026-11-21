import { invokeBackend } from "@/services/desktop-bridge";

/**
 * 字段级加解密的批量入口。
 *
 * 同一事件循环 tick 内发起的 encryptField / decryptField 会被合并为一次
 * encrypt_fields / decrypt_fields IPC，避免加载或保存大量连接凭据时
 * 每个字段各走一次桥接调用。解密逐项容错：一个损坏的密文只会让对应的 Promise 失败。
 */

const MAX_BATCH_FIELDS = 1000;

interface DecryptFieldResult {
  value: string;
  error?: string;
}

interface PendingField<T> {
  input: string;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
}

function createFieldBatcher<T>(
  flushChunk: (chunk: PendingField<T>[]) => Promise<void>
) {
  let pending: PendingField<T>[] = [];
  let scheduled = false;

  const flush = () => {
    scheduled = false;
    const queued = pending;
    pending = [];
    for (let start = 0; start < queued.length; start += MAX_BATCH_FIELDS) {
      const chunk = queued.slice(start, start + MAX_BATCH_FIELDS);
      void flushChunk(chunk).catch((error: unknown) => {
        for (const item of chunk) item.reject(error);
      });
    }
  };

  return (input: string) =>
    new Promise<T>((resolve, reject) => {
      pending.push({ input, resolve, reject });
      if (!scheduled) {
        scheduled = true;
        queueMicrotask(flush);
      }
    });
}

const enqueueDecrypt = createFieldBatcher<string>(async (chunk) => {
  const results = await invokeBackend<DecryptFieldResult[]>("decrypt_fields", {
    ciphers: chunk.map((item) => item.input),
  });
  if (!Array.isArray(results) || results.length !== chunk.length) {
    throw new Error("decrypt_fields returned an unexpected result");
  }
  chunk.forEach((item, index) => {
    const result = results[index];
    if (result?.error) item.reject(new Error(result.error));
    else item.resolve(result?.value ?? "");
  });
});

const enqueueEncrypt = createFieldBatcher<string>(async (chunk) => {
  const results = await invokeBackend<string[]>("encrypt_fields", {
    plains: chunk.map((item) => item.input),
  });
  if (!Array.isArray(results) || results.length !== chunk.length) {
    throw new Error("encrypt_fields returned an unexpected result");
  }
  chunk.forEach((item, index) => item.resolve(results[index]));
});

export function decryptField(cipher: string): Promise<string> {
  return enqueueDecrypt(cipher);
}

export function encryptField(plain: string): Promise<string> {
  return enqueueEncrypt(plain);
}
