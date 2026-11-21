import assert from "node:assert/strict";
import test from "node:test";
import { normalizeTencentSkillHubDocsUrl } from "../src/domain/ai/skillhub-repository.ts";

test("maps stored Tencent SkillHub documentation links to the public skill page", () => {
  assert.equal(
    normalizeTencentSkillHubDocsUrl("https://skillhub.cloud.tencent.com/demo/code-review"),
    "https://skillhub.cn/skills/demo/code-review"
  );
  assert.equal(
    normalizeTencentSkillHubDocsUrl("https://api.skillhub.cn/demo/code-review"),
    "https://skillhub.cn/skills/demo/code-review"
  );
  assert.equal(normalizeTencentSkillHubDocsUrl("https://docs.example.com/skill"), "https://docs.example.com/skill");
  assert.equal(normalizeTencentSkillHubDocsUrl("https://api.skillhub.cn/a/b/c"), "");
  assert.equal(normalizeTencentSkillHubDocsUrl("javascript:alert(1)"), "");
});
