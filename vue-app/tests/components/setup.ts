import { afterEach, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { config } from "@vue/test-utils";

beforeEach(() => {
  localStorage.clear();
  const pinia = createPinia();
  setActivePinia(pinia);
  config.global.plugins = [pinia];
});

afterEach(() => {
  document.body.innerHTML = "";
});
