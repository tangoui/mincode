/**
 * Delivers items in arrival order when some need an asynchronous step first
 * (App.vue's plugin:notify listener loads the action parser on demand only
 * for notifications that carry actions). With nothing pending, an item
 * without a step is delivered synchronously; otherwise every item waits for
 * the ones before it. A failed step delivers the item with `undefined`.
 */
export function createOrderedDelivery() {
  let pending = 0;
  let chain: Promise<unknown> = Promise.resolve();
  return function deliver<T>(step: Promise<T> | undefined, push: (value: T | undefined) => void): void {
    if (!step && pending === 0) {
      push(undefined);
      return;
    }
    pending += 1;
    const ready = (step ?? Promise.resolve(undefined)).catch(() => undefined);
    chain = chain
      .then(() => ready)
      .then((value) => push(value))
      .catch(() => undefined)
      .finally(() => {
        pending -= 1;
      });
  };
}
