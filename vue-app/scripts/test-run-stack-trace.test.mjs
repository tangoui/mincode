import assert from "node:assert/strict";
import test from "node:test";
import {
  fileReferenceName,
  fileReferenceSuffix,
  matchProjectFile,
  parseStackTrace,
  parseStackTraceLine,
  STACK_TRACE_LIMITS,
} from "../src/domain/testing/stack-trace.ts";

test("stack trace: a qualified frame's file reference gets the qualifier's directories", () => {
  const segments = parseStackTraceLine("\tat com.acme.CalcTest.adds(CalcTest.kt:42)");
  assert.deepEqual(segments, [
    { kind: "text", text: "\tat com.acme.CalcTest.adds(" },
    { kind: "file", text: "CalcTest.kt:42", file: "CalcTest.kt", line: 42, qualifier: "com.acme.CalcTest.adds" },
    { kind: "text", text: ")" },
  ]);
  assert.equal(fileReferenceSuffix(segments[1], "/p"), "com/acme/CalcTest.kt");
  assert.equal(fileReferenceName(segments[1]), "CalcTest.kt");
});

test("stack trace: paths with directories, columns and absolute paths", () => {
  const [, file] = parseStackTraceLine("    at Object.<anonymous> (src/calc.test.ts:12:7)");
  assert.equal(file.kind, "file");
  assert.equal(file.file, "src/calc.test.ts");
  assert.equal(file.line, 12);
  assert.equal(fileReferenceSuffix(file, "/p"), "src/calc.test.ts");

  const [, inside] = parseStackTraceLine('  File "/p/tests/test_calc.py:8"');
  assert.equal(fileReferenceSuffix(inside, "/p/"), "tests/test_calc.py");
  const [, outside] = parseStackTraceLine("at /usr/lib/x.py:8");
  assert.equal(fileReferenceSuffix(outside, "/p"), undefined);
  const [, dotted] = parseStackTraceLine("at ../x.py:8");
  assert.equal(fileReferenceSuffix(dotted, "/p"), undefined);
  const [, windows] = parseStackTraceLine("at C:\\work\\p\\src\\a.ts:3");
  assert.equal(windows.file, "C:\\work\\p\\src\\a.ts");
  assert.equal(fileReferenceSuffix(windows, "C:\\work\\p"), "src/a.ts");
});

test("stack trace: debugger addresses are links, file: URLs and plain text are not", () => {
  const segments = parseStackTraceLine("at lib (acme://contents/lib.jar/pkg/Lib.class?x=1).");
  assert.deepEqual(segments[1], { kind: "address", text: "acme://contents/lib.jar/pkg/Lib.class?x=1", address: "acme://contents/lib.jar/pkg/Lib.class?x=1" });
  assert.equal(segments.length, 3);
  assert.ok(parseStackTraceLine("file:///p/a.ts").every((segment) => segment.kind !== "address"));
  assert.deepEqual(parseStackTraceLine("expected: <3> but was: <4>"), [{ kind: "text", text: "expected: <3> but was: <4>" }]);
  assert.deepEqual(parseStackTraceLine(""), []);
});

test("stack trace: long input stays bounded and plain", () => {
  const long = `${"x".repeat(STACK_TRACE_LIMITS.lineCharsMax)} a.ts:1`;
  assert.deepEqual(parseStackTraceLine(long), [{ kind: "text", text: long }]);
  const lines = Array.from({ length: STACK_TRACE_LIMITS.linesMax + 2 }, () => "at a.ts:1");
  const parsed = parseStackTrace(lines.join("\n"));
  assert.equal(parsed.length, lines.length);
  assert.equal(parsed[0][1].kind, "file");
  assert.deepEqual(parsed.at(-1), [{ kind: "text", text: "at a.ts:1" }]);
});

test("stack trace: a suffix matches exactly one project path", () => {
  const paths = ["src/test/com/acme/CalcTest.kt", "src/main/com/acme/Calc.kt", "other/Calc.kt"];
  assert.equal(matchProjectFile("com/acme/CalcTest.kt", paths), "src/test/com/acme/CalcTest.kt");
  assert.equal(matchProjectFile("Calc.kt", paths), undefined);
  assert.equal(matchProjectFile("com/acme/Calc.kt", paths), "src/main/com/acme/Calc.kt");
  assert.equal(matchProjectFile("acme/Calc.kt", ["x/ACME/calc.kt"], true), "x/ACME/calc.kt");
  assert.equal(matchProjectFile("me/Calc.kt", paths), undefined);
});
