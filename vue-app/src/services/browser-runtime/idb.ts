/**
 * 极简 IndexedDB 封装（浏览器本地模式专用，不引入依赖）。
 *
 * 只暴露功能处理器需要的几种操作，并以「一个回调一个事务」的方式使用：回调里只能 await 本事务的
 * 操作（IndexedDB 事务在没有待处理请求时会自动提交），回调抛错则整个事务回滚。
 * 复合主键的第一段约定为作用域 id（如 scopeId），`getAll(store, scope)` 按它取一段键范围。
 */

export interface IdbTransaction {
  get<T>(store: string, key: IDBValidKey): Promise<T | undefined>;
  /** All records of `store`, or with `scope` only those whose compound key starts with it; in key order. */
  getAll<T>(store: string, scope?: string): Promise<T[]>;
  put(store: string, value: unknown): Promise<void>;
  delete(store: string, key: IDBValidKey): Promise<void>;
}

export interface IdbDatabase {
  transaction<T>(stores: string[], mode: IDBTransactionMode, run: (tx: IdbTransaction) => Promise<T>): Promise<T>;
}

/** Store name → keyPath (a field, or the fields of a compound key). */
export type IdbSchema = Readonly<Record<string, string | readonly string[]>>;

function settle<T>(request: IDBRequest<T>) {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

/**
 * Opens (lazily, once) a database whose object stores follow `schema`. Bump `version` when the
 * schema changes; stores missing from an older version are created on upgrade.
 */
export function openIdbDatabase(name: string, version: number, schema: IdbSchema): IdbDatabase {
  let opening: Promise<IDBDatabase> | undefined;
  const open = () => {
    opening ??= new Promise<IDBDatabase>((resolve, reject) => {
      if (typeof indexedDB === "undefined") {
        reject(new Error("IndexedDB is not available in this browser"));
        return;
      }
      const request = indexedDB.open(name, version);
      request.onupgradeneeded = () => {
        const db = request.result;
        for (const [store, keyPath] of Object.entries(schema)) {
          if (!db.objectStoreNames.contains(store)) db.createObjectStore(store, { keyPath: keyPath as string | string[] });
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error ?? new Error(`Failed to open IndexedDB "${name}"`));
      request.onblocked = () => reject(new Error(`IndexedDB "${name}" is blocked by another tab`));
    });
    // A failed open is retried on the next call instead of failing forever.
    opening.catch(() => {
      opening = undefined;
    });
    return opening;
  };

  return {
    async transaction(stores, mode, run) {
      const db = await open();
      const tx = db.transaction(stores, mode);
      const completed = new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error ?? new Error("IndexedDB transaction failed"));
        tx.onabort = () => reject(tx.error ?? new DOMException("IndexedDB transaction aborted", "AbortError"));
      });
      void completed.catch(() => undefined);
      const wrapper: IdbTransaction = {
        get: <T>(store: string, key: IDBValidKey) => settle(tx.objectStore(store).get(key)) as Promise<T | undefined>,
        getAll: <T>(store: string, scope?: string) => settle(tx.objectStore(store).getAll(
          scope === undefined ? undefined : IDBKeyRange.bound([scope], [scope, []]),
        )) as Promise<T[]>,
        put: async (store, value) => {
          await settle(tx.objectStore(store).put(value));
        },
        delete: async (store, key) => {
          await settle(tx.objectStore(store).delete(key));
        },
      };
      let result: Awaited<ReturnType<typeof run>>;
      try {
        result = await run(wrapper);
      } catch (error) {
        try {
          tx.abort();
        } catch {
          // Already finished or aborted.
        }
        throw error;
      }
      await completed;
      return result;
    },
  };
}
