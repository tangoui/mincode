import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";
import { parse, compileScript, compileTemplate } from "vue/compiler-sfc";

const packages = new URL("../../official-plugins/", import.meta.url);
const panels = {
  "consul-manager": ["ConsulManager", "NodesPanel"],
  "etcd-manager": ["EtcdManager", "ClusterPanel"],
  "docker-manager": ["DockerManager", "SystemPanel"],
  "zookeeper-manager": ["ZooKeeperManager", "ServerStatsPanel"],
  "object-storage": ["ObjectStorage", "MetadataPanel"],
  "mind-map": ["MindMap", "MapInspector"],
  "http-load-tester": ["HttpLoadTester", "ProbePanel"],
  "devtools": ["DevToolsWorkbench", "ToolOutput"],
};

test("extracted display panels leave transport and native action ownership in their parent", async () => {
  for (const [name, [parent, panel]] of Object.entries(panels)) {
    const main = await readFile(new URL(`${name}/ui-src/${parent}.vue`, packages), "utf8");
    const source = await readFile(new URL(`${name}/ui-src/${panel}.vue`, packages), "utf8");
    assert.match(main, new RegExp(`<${panel}\\b`), name);
    assert.doesNotMatch(source, /createTransport|transport\.|localStorage|sessionStorage|navigator\.clipboard|v-html|innerHTML/, name);
    assert.match(source, /defineEmits/, name);
    assert.doesNotMatch(source, /\bany\b|@ts-ignore|@ts-nocheck/, name);
  }
});

test("all refactored plugin components compile without unresolved bindings", async () => {
  for (const name of Object.keys(panels)) {
    const dir = new URL(`${name}/ui-src/`, packages);
    for (const file of (await readdir(dir)).filter(file => file.endsWith(".vue"))) {
      const source = await readFile(new URL(file, dir), "utf8");
      const { descriptor, errors } = parse(source, { filename: file });
      assert.deepEqual(errors, [], `${name}/${file}`);
      const script = compileScript(descriptor, { id: file });
      const template = compileTemplate({ source: descriptor.template.content, filename: file, id: file, compilerOptions: { bindingMetadata: script.bindings } });
      assert.deepEqual(template.errors, [], `${name}/${file}`);
      const unresolved = [...new Set([...template.code.matchAll(/_ctx\.(\w+)/g)].map(match => match[1]))].filter(key => !["$slots", "$attrs", "$emit"].includes(key));
      assert.deepEqual(unresolved, [], `${name}/${file}`);
    }
  }
});

test("five main pages group notices without merging their dismissal or security conditions", async () => {
  for (const name of ["consul-manager", "zookeeper-manager", "docker-manager", "object-storage", "pulsar-inspector"]) {
    const file = panels[name]?.[0] ?? "PulsarInspector";
    const source = await readFile(new URL(`${name}/ui-src/${file}.vue`, packages), "utf8");
    assert.match(source, /<PluginStatusArea :locale="language">/, name);
    assert.match(source, /<ElAlert[^>]*type="warning"/, name);
  }
});
