import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const watermarkCss = await readFile(
  new URL("../src/styles/shared/license-watermark.css", import.meta.url),
  "utf8",
);
const monacoOverflowCss = await readFile(
  new URL("../src/styles/shared/monaco-overflow.css", import.meta.url),
  "utf8",
);

function zIndexOf(css, selector) {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escapedSelector}\\s*\\{[^}]*?z-index:\\s*(\\d+)`, "s"));
  return match ? Number(match[1]) : undefined;
}

function opacityOf(css, selector) {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escapedSelector}\\s*\\{[^}]*?opacity:\\s*(0(?:\\.\\d+)?|1)`, "s"));
  return match ? Number(match[1]) : undefined;
}

test("unlicensed watermark remains clearly visible without obscuring content", () => {
  const opacity = opacityOf(watermarkCss, ".app-license-watermark__pattern");

  assert.ok(Number.isFinite(opacity), "watermark pattern must have an explicit opacity");
  assert.ok(opacity >= 0.2, "watermark must be darker than the previous low-contrast treatment");
  assert.ok(opacity <= 0.26, "watermark must not obscure workspace content");
});

test("Element drawers and dialogs stay above the unlicensed watermark", () => {
  const watermarkZIndex = zIndexOf(watermarkCss, ".app-license-watermark");

  assert.ok(Number.isFinite(watermarkZIndex), "watermark must have an explicit layer");
  assert.ok(
    watermarkZIndex < 2000,
    "watermark must stay below Element Plus teleported overlays and must not cover drawer actions",
  );
});

test("Monaco overlays stay above the unlicensed watermark", () => {
  const watermarkZIndex = zIndexOf(watermarkCss, ".app-license-watermark");
  const overflowHostZIndex = zIndexOf(monacoOverflowCss, ".monaco-overflow-widgets-host");
  const contextViewZIndex = zIndexOf(monacoOverflowCss, ".monaco-editor .context-view");

  assert.ok(Number.isFinite(watermarkZIndex), "watermark must have an explicit layer");
  assert.ok(Number.isFinite(overflowHostZIndex), "Monaco overflow host must have an explicit layer");
  assert.ok(Number.isFinite(contextViewZIndex), "Monaco context menu must have an explicit layer");
  assert.ok(
    overflowHostZIndex > watermarkZIndex,
    "Monaco hover and definition overlays must render above the watermark",
  );
  assert.ok(
    contextViewZIndex > watermarkZIndex,
    "Monaco context menu must render above the watermark instead of looking transparent",
  );
});
