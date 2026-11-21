import assert from "node:assert/strict";
import test from "node:test";
import { projectDirname } from "../src/domain/projects/types.ts";

test("preserves absolute Unix paths when resolving a parent directory", () => {
  assert.equal(
    projectDirname(
      "/Volumes/LHD/code/sass/claw-specs/01 设计思路/00-产品设计思路-总览.md"
    ),
    "/Volumes/LHD/code/sass/claw-specs/01 设计思路"
  );
  assert.equal(projectDirname("/README.md"), "/");
});

test("keeps relative and Windows project paths compatible", () => {
  assert.equal(projectDirname("claw-specs/README.md"), "claw-specs");
  assert.equal(projectDirname("C:\\workspace\\claw-specs\\README.md"), "C:/workspace/claw-specs");
});
