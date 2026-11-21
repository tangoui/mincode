import assert from "node:assert/strict";
import test from "node:test";

// Browser-local `http_send_request`: fetch-based replacement of backend/http/http.go.
// globalThis.fetch is replaced per test; the handler must keep the Go response shape.

const {
  BROWSER_FORBIDDEN_HEADERS,
  BROWSER_HTTP_MAX_BODY,
  sendBrowserHttpRequest,
} = await import("../src/services/browser-runtime/http.ts");
const { browserFile, pickBrowserFiles, registerBrowserFile } = await import("../src/services/browser-runtime/files.ts");
const { browserLocalFeatures, getBrowserHandler } = await import("../src/services/browser-runtime/registry.ts");

const realFetch = globalThis.fetch;
test.afterEach(() => {
  globalThis.fetch = realFetch;
});

/** Records every fetch call and answers with `respond(url, init)`. */
function mockFetch(respond) {
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return respond(url, init);
  };
  return calls;
}

/** A fetch that never answers until its signal aborts, then rejects like a real fetch. */
function hangingFetch() {
  globalThis.fetch = (_url, init) => new Promise((_resolve, reject) => {
    init.signal.addEventListener("abort", () => reject(init.signal.reason ?? new DOMException("aborted", "AbortError")), { once: true });
  });
}

function payload(overrides = {}) {
  return { method: "GET", url: "https://api.example.test/items", headers: [], bodyType: "none", timeoutMs: 30_000, ...overrides };
}

test("registers the http_send_request handler and the http browser feature at import time", () => {
  assert.equal(typeof getBrowserHandler("http_send_request"), "function");
  assert.deepEqual(browserLocalFeatures.map((feature) => feature.id), ["http"]);
  assert.equal(browserLocalFeatures[0].titleKey, "browserLocal.feature.http.title");
  assert.equal(browserLocalFeatures[0].limitKey, "browserLocal.feature.http.limit");
});

test("GET maps status, headers, elapsed time and size like the Go handler", async () => {
  const calls = mockFetch(() => new Response("héllo", {
    status: 201,
    statusText: "Created",
    headers: { "content-type": "text/plain", "x-trace": "abc" },
  }));

  const result = await getBrowserHandler("http_send_request")({
    payload: payload({ headers: [["Accept", "text/plain"], ["  ", "ignored"]] }),
  });

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://api.example.test/items");
  assert.equal(calls[0].init.method, "GET");
  assert.equal(calls[0].init.redirect, "follow");
  assert.equal(calls[0].init.body, undefined);
  assert.deepEqual([...calls[0].init.headers], [["accept", "text/plain"]]);
  assert.equal(result.status, 201);
  assert.equal(result.status_text, "Created");
  assert.deepEqual(result.headers, [["content-type", "text/plain"], ["x-trace", "abc"]]);
  assert.equal(result.body, "héllo");
  assert.equal(result.size_bytes, 6, "size is the UTF-8 byte length, not the string length");
  assert.equal(typeof result.elapsed_ms, "number");
  assert.ok(result.elapsed_ms >= 0 && Number.isInteger(result.elapsed_ms));
  assert.deepEqual(result.browser_notes, []);
});

test("method defaults to GET and is upper-cased", async () => {
  const calls = mockFetch(() => new Response(""));
  await sendBrowserHttpRequest({ payload: payload({ method: " patch " }) });
  await sendBrowserHttpRequest({ payload: payload({ method: "" }) });
  assert.deepEqual(calls.map((call) => call.init.method), ["PATCH", "GET"]);
});

test("JSON and x-www-form-urlencoded bodies are sent as text with their content type", async () => {
  const calls = mockFetch(() => new Response("{}"));
  await sendBrowserHttpRequest({
    payload: payload({
      method: "POST",
      bodyType: "json",
      body: '{"a":1}',
      headers: [["Content-Type", "application/json"]],
    }),
  });
  await sendBrowserHttpRequest({
    payload: payload({
      method: "POST",
      bodyType: "x-www-form-urlencoded",
      body: "a=1&b=two",
      headers: [["Content-Type", "application/x-www-form-urlencoded"]],
    }),
  });

  assert.equal(calls[0].init.body, '{"a":1}');
  assert.equal(calls[0].init.headers.get("content-type"), "application/json");
  assert.equal(calls[1].init.body, "a=1&b=two");
  assert.equal(calls[1].init.headers.get("content-type"), "application/x-www-form-urlencoded");
});

test("form-data sends text fields and browser-file:// files; the browser sets the multipart content type", async () => {
  const avatar = registerBrowserFile(new File(["PNGDATA"], "avatar.png", { type: "image/png" }));
  const doc = registerBrowserFile(new File(["plain"], "notes.txt"));
  assert.match(avatar, /^browser-file:\/\/[0-9a-f-]{36}\/avatar\.png$/);
  assert.equal(browserFile(avatar).name, "avatar.png");
  const calls = mockFetch(() => new Response("ok"));

  await sendBrowserHttpRequest({
    payload: payload({
      method: "POST",
      bodyType: "form-data",
      headers: [["Content-Type", "multipart/form-data"], ["X-Kept", "1"]],
      formFields: [
        { name: "title", value: "hello", fieldType: "text" },
        { name: "avatar", value: avatar, fieldType: "file" },
        { name: "doc", value: doc, fieldType: "file", fileName: "renamed.md", contentType: "text/markdown" },
        { name: "  ", value: "skipped", fieldType: "text" },
      ],
    }),
  });

  const body = calls[0].init.body;
  assert.ok(body instanceof FormData);
  assert.deepEqual([...body.keys()], ["title", "avatar", "doc"]);
  assert.equal(body.get("title"), "hello");
  assert.equal(body.get("avatar").name, "avatar.png");
  assert.equal(await body.get("avatar").text(), "PNGDATA");
  assert.equal(body.get("doc").name, "renamed.md");
  assert.equal(body.get("doc").type, "text/markdown");
  assert.equal(calls[0].init.headers.has("content-type"), false, "fetch must generate the boundary itself");
  assert.equal(calls[0].init.headers.get("x-kept"), "1");
});

test("binary body sends the picked file with application/octet-stream when no content type is set", async () => {
  const path = registerBrowserFile(new File([new Uint8Array([1, 2, 3])], "blob.bin"));
  const calls = mockFetch(() => new Response("ok"));
  await sendBrowserHttpRequest({ payload: payload({ method: "PUT", bodyType: "binary", binaryFilePath: path }) });
  assert.equal(calls[0].init.body, browserFile(path));
  assert.equal(calls[0].init.headers.get("content-type"), "application/octet-stream");
});

test("a file path that is not in this tab's picked files is rejected before fetching", async () => {
  const calls = mockFetch(() => new Response("ok"));
  await assert.rejects(
    sendBrowserHttpRequest({
      payload: payload({
        method: "POST",
        bodyType: "form-data",
        formFields: [{ name: "f", value: "/Users/me/secret.txt", fieldType: "file" }],
      }),
    }),
    { message: "http.browser.fileUnavailable" },
  );
  await assert.rejects(
    sendBrowserHttpRequest({
      payload: payload({ method: "PUT", bodyType: "binary", binaryFilePath: "browser-file://gone/x.bin" }),
    }),
    { message: "http.browser.fileUnavailable" },
  );
  assert.equal(calls.length, 0);
});

test("forbidden request headers are dropped and reported in browser_notes, not mixed into response headers", async () => {
  assert.ok(BROWSER_FORBIDDEN_HEADERS.has("cookie"));
  assert.ok(BROWSER_FORBIDDEN_HEADERS.has("host"));
  const calls = mockFetch(() => new Response(null, { headers: { "x-server": "1" } }));

  const result = await sendBrowserHttpRequest({
    payload: payload({
      headers: [
        ["Cookie", "sid=1"],
        ["Host", "evil.test"],
        ["Content-Length", "10"],
        ["Sec-Fetch-Mode", "cors"],
        ["Proxy-Authorization", "x"],
        ["Authorization", "Bearer t"],
        ["cookie", "dup=1"],
      ],
    }),
  });

  assert.deepEqual([...calls[0].init.headers], [["authorization", "Bearer t"]]);
  assert.deepEqual(result.headers, [["x-server", "1"]]);
  assert.equal(result.browser_notes.length, 1);
  for (const name of ["Cookie", "Host", "Content-Length", "Sec-Fetch-Mode", "Proxy-Authorization"]) {
    assert.ok(result.browser_notes[0].includes(name), `note mentions ${name}`);
  }
  assert.equal(result.browser_notes[0].match(/cookie/gi).length, 1, "duplicate header names are listed once");
});

test("a GET body is dropped with a note instead of failing as a fake CORS error", async () => {
  const calls = mockFetch(() => new Response("ok"));
  const result = await sendBrowserHttpRequest({ payload: payload({ bodyType: "json", body: '{"q":1}' }) });
  assert.equal(calls[0].init.body, undefined);
  assert.equal(result.browser_notes.length, 1);
  assert.match(result.browser_notes[0], /GET/);
});

test("GET / HEAD bodies are not built at all; only real content produces a note", async () => {
  const calls = mockFetch(() => new Response("ok"));
  const empty = await sendBrowserHttpRequest({ payload: payload({ bodyType: "json", body: "" }) });
  assert.deepEqual(empty.browser_notes, []);
  // A stale browser-file:// path would throw fileUnavailable if the body were built first.
  const stale = await sendBrowserHttpRequest({
    payload: payload({ method: "HEAD", bodyType: "binary", binaryFilePath: "browser-file://gone/x.bin" }),
  });
  assert.equal(calls.length, 2);
  assert.equal(calls[1].init.body, undefined);
  assert.equal(stale.browser_notes.length, 1);
  assert.match(stale.browser_notes[0], /HEAD/);
});

test("redirects: plain safelisted GET follows; custom headers or a body switch to manual", async () => {
  const calls = mockFetch(() => new Response("ok"));
  await sendBrowserHttpRequest({
    payload: payload({ headers: [["Accept", "application/json"], ["Accept-Language", "en"], ["Content-Type", "text/plain"]] }),
  });
  await sendBrowserHttpRequest({ payload: payload({ headers: [["X-Api-Key", "secret"]] }) });
  await sendBrowserHttpRequest({ payload: payload({ headers: [["Authorization", "Bearer t"]] }) });
  await sendBrowserHttpRequest({
    payload: payload({ method: "POST", bodyType: "json", body: "{}", headers: [["Content-Type", "application/json"]] }),
  });
  assert.deepEqual(calls.map((call) => call.init.redirect), ["follow", "manual", "manual", "manual"]);
});

test("a redirect on a request with custom headers is blocked, not followed or shown as success", async () => {
  const calls = mockFetch(() => ({ type: "opaqueredirect", status: 0, statusText: "", url: "", redirected: false, headers: new Headers(), body: null }));
  await assert.rejects(
    sendBrowserHttpRequest({ payload: payload({ headers: [["X-Api-Key", "secret"]] }) }),
    { message: "http.browser.redirectBlocked" },
  );
  assert.equal(calls[0].init.redirect, "manual");
});

test("a followed redirect that downgrades https to http adds a warning note", async () => {
  mockFetch(() => {
    const response = new Response("ok");
    Object.defineProperty(response, "redirected", { value: true });
    Object.defineProperty(response, "url", { value: "http://api.example.test/plain" });
    return response;
  });
  const downgraded = await sendBrowserHttpRequest({ payload: payload() });
  assert.equal(downgraded.browser_notes.length, 1);
  assert.match(downgraded.browser_notes[0], /http:\/\/api\.example\.test\/plain/);

  mockFetch(() => {
    const response = new Response("ok");
    Object.defineProperty(response, "redirected", { value: true });
    Object.defineProperty(response, "url", { value: "https://api.example.test/other" });
    return response;
  });
  assert.deepEqual((await sendBrowserHttpRequest({ payload: payload() })).browser_notes, []);
});

test("an empty statusText (HTTP/2) falls back to Go's http.StatusText", async () => {
  mockFetch(() => new Response(null, { status: 404, statusText: "" }));
  assert.equal((await sendBrowserHttpRequest({ payload: payload() })).status_text, "Not Found");
  mockFetch(() => new Response(null, { status: 299, statusText: "" }));
  assert.equal((await sendBrowserHttpRequest({ payload: payload() })).status_text, "");
});

test("URL credentials become a Basic Authorization header and are stripped from the URL", async () => {
  const calls = mockFetch(() => new Response("ok"));
  await sendBrowserHttpRequest({ payload: payload({ url: "https://us%20er:p%40ss%C3%A9@api.example.test/items?q=1" }) });
  assert.equal(calls[0].url, "https://api.example.test/items?q=1");
  const expected = Buffer.from("us er:p@ssé", "utf8").toString("base64");
  assert.equal(calls[0].init.headers.get("authorization"), `Basic ${expected}`);
  assert.equal(calls[0].init.redirect, "manual");

  await sendBrowserHttpRequest({
    payload: payload({ url: "https://u:p@api.example.test/", headers: [["Authorization", "Bearer mine"]] }),
  });
  assert.equal(calls[1].url, "https://api.example.test/");
  assert.equal(calls[1].init.headers.get("authorization"), "Bearer mine", "an explicit Authorization header wins");
});

test("URL credentials with a malformed % escape are an invalid URL, not a raw URIError", async () => {
  const calls = mockFetch(() => new Response("ok"));
  for (const url of ["https://us%zzer:p@api.example.test/", "https://u:p%E0%A4%A@api.example.test/", "https://%@api.example.test/"]) {
    await assert.rejects(sendBrowserHttpRequest({ payload: payload({ url }) }), (error) => {
      assert.equal(error.message, "http.browser.invalidUrl", url);
      assert.equal(error instanceof URIError, false);
      return true;
    });
  }
  assert.equal(calls.length, 0);
});

test("timeout uses payload.timeoutMs and rejects with the timeout error", async () => {
  hangingFetch();
  const started = Date.now();
  await assert.rejects(
    sendBrowserHttpRequest({ payload: payload({ timeoutMs: 30 }) }),
    { message: "http.browser.timeout" },
  );
  assert.ok(Date.now() - started < 2_000);
});

test("aborting the caller's signal cancels the request and rejects with the abort reason", async () => {
  hangingFetch();
  const controller = new AbortController();
  const pending = sendBrowserHttpRequest({ payload: payload() }, controller.signal);
  const reason = new DOMException("stopped by user", "AbortError");
  controller.abort(reason);
  await assert.rejects(pending, (error) => error === reason);
});

test("an already-aborted signal never calls fetch", async () => {
  const calls = mockFetch(() => new Response("ok"));
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(sendBrowserHttpRequest({ payload: payload() }, controller.signal), { name: "AbortError" });
  assert.equal(calls.length, 0);
});

test("fetch TypeError (CORS / network) becomes the corsError key, never an empty success", async () => {
  mockFetch(() => {
    throw new TypeError("Failed to fetch");
  });
  await assert.rejects(sendBrowserHttpRequest({ payload: payload() }), (error) => {
    assert.equal(error.message, "http.browser.corsError");
    assert.equal(error.cause.message, "Failed to fetch");
    return true;
  });
});

test("only http and https URLs are accepted", async () => {
  const calls = mockFetch(() => new Response("ok"));
  for (const url of ["file:///etc/passwd", "javascript:alert(1)", "not a url", ""]) {
    await assert.rejects(sendBrowserHttpRequest({ payload: payload({ url }) }), { message: "http.browser.invalidUrl" });
  }
  assert.equal(calls.length, 0);
});

test("an 11 MB response is truncated at 10 MB with the Go truncation marker", async () => {
  // 11.5 MB in 512 KiB chunks: the cap is crossed before the stream ends, so the rest must be cancelled.
  const size = 11.5 * 1024 * 1024;
  let pulled = 0;
  const chunk = new Uint8Array(512 * 1024).fill(0x61);
  let cancelled = false;
  mockFetch(() => new Response(new ReadableStream({
    pull(streamController) {
      if (pulled >= size) {
        streamController.close();
        return;
      }
      pulled += chunk.byteLength;
      streamController.enqueue(chunk);
    },
    cancel() {
      cancelled = true;
    },
  })));

  const result = await sendBrowserHttpRequest({ payload: payload() });

  assert.equal(BROWSER_HTTP_MAX_BODY, 10 * 1024 * 1024);
  assert.equal(result.size_bytes, BROWSER_HTTP_MAX_BODY + 1, "same as Go: bytes read, capped at limit + 1");
  assert.ok(result.body.endsWith("\n... (response truncated)"));
  assert.equal(result.body.length, BROWSER_HTTP_MAX_BODY + "\n... (response truncated)".length);
  assert.ok(cancelled, "the rest of the stream is cancelled once the cap is exceeded");
});

test("a response of exactly 10 MB is not truncated", async () => {
  mockFetch(() => new Response(new Uint8Array(BROWSER_HTTP_MAX_BODY).fill(0x62)));
  const result = await sendBrowserHttpRequest({ payload: payload() });
  assert.equal(result.size_bytes, BROWSER_HTTP_MAX_BODY);
  assert.equal(result.body.length, BROWSER_HTTP_MAX_BODY);
  assert.ok(!result.body.endsWith("(response truncated)"));
});

test("pickBrowserFiles registers chosen files under browser-file:// paths and resolves [] on cancel", async () => {
  const appended = [];
  const makeInput = () => {
    const listeners = {};
    const input = {
      type: "",
      multiple: false,
      accept: "",
      style: {},
      files: null,
      addEventListener: (type, listener) => {
        listeners[type] = listener;
      },
      remove: () => {
        input.removed = true;
      },
      click: () => {
        input.clicked = true;
      },
      fire: (type) => listeners[type]?.(),
    };
    return input;
  };
  globalThis.document = {
    createElement: (tag) => {
      assert.equal(tag, "input");
      const input = makeInput();
      appended.push(input);
      return input;
    },
    body: { append: () => {} },
  };
  try {
    const picking = pickBrowserFiles({ multiple: true, accept: [".png", "image/*"] });
    const input = appended[0];
    assert.equal(input.type, "file");
    assert.equal(input.multiple, true);
    assert.equal(input.accept, ".png,image/*");
    assert.equal(input.clicked, true);
    input.files = [new File(["a"], "a.png"), new File(["bb"], "b.png")];
    input.fire("change");
    const picked = await picking;
    assert.deepEqual(picked.map(({ name, size }) => ({ name, size })), [{ name: "a.png", size: 1 }, { name: "b.png", size: 2 }]);
    assert.equal(browserFile(picked[1].path).name, "b.png");
    assert.equal(input.removed, true);

    const cancelling = pickBrowserFiles({});
    appended[1].fire("cancel");
    assert.deepEqual(await cancelling, []);
    assert.equal(appended[1].multiple, false);
  } finally {
    delete globalThis.document;
  }
});
