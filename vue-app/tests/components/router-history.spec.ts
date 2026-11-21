// The desktop app's router must not drive the browser History API: WebKit rejects more than
// 100 pushState/replaceState calls per 10 seconds, after which vue-router falls back to
// location.replace/assign and every page switch stalls ~80 ms (rapid tab switching hits it).
import { afterEach, describe, expect, it, vi } from "vitest";
import router from "@/router";

afterEach(() => vi.restoreAllMocks());

describe("router history", () => {
  it("keeps navigation in memory instead of the browser History API", () => {
    const pushState = vi.spyOn(window.history, "pushState");
    const replaceState = vi.spyOn(window.history, "replaceState");
    const history = router.options.history;
    for (let i = 0; i < 200; i++) {
      history.push(i % 2 ? "/projects" : "/database");
      history.replace("/plugins");
    }
    expect(history.location).toBe("/plugins");
    expect(pushState).not.toHaveBeenCalled();
    expect(replaceState).not.toHaveBeenCalled();
  });
});
