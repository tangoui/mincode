import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const ROOT = "/Users/a/java-demo";
const STALE_JDT = `${ROOT}/jdt://contents/java.base/java.lang/Class.class?=java-demo/%3Cjava.lang(Class.class`;
const STALE_SPRING = `${ROOT}/jdt://contents/spring-aop-6.1.14.jar/org.springframework.aop.framework/JdkDynamicAopProxy.class?=x`;
const FILE = `${ROOT}/src/main/java/com/example/demo/controller/TodoController.java`;

const stored = vi.hoisted(() => ({ value: null as unknown }));
vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn(async (key: string) => (key === "app_local_project_workspaces_v1" ? stored.value : null)),
  savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));

import { useLocalProjectStore } from "@/stores/local-project";

beforeEach(() => setActivePinia(createPinia()));

describe("restoring saved project tabs", () => {
  it("drops stale debugger tabs that older versions saved", async () => {
    stored.value = {
      version: 1,
      workspaces: {
        "workspace-a": {
          version: 6, projectRoot: ROOT, projects: [{ root: ROOT, name: "java-demo" }],
          openPaths: [FILE, STALE_JDT, STALE_SPRING], activePath: STALE_JDT,
          editorViewStates: { [STALE_JDT]: { cursor: { lineNumber: 536, column: 1 } } },
          documentEditors: {},
        },
      },
    };
    const project = useLocalProjectStore();
    await project.hydrate("workspace-a");
    expect(project.documents.map((document) => document.path)).toEqual([FILE]);
    expect(project.activePath).toBe(FILE);
    expect(Object.keys(project.editorViewStates)).not.toContain(STALE_JDT);
  });
});
