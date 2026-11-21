import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import SshConnectionDialog from "@/components/ssh/SshConnectionDialog.vue";
import type { SshConnectionProfile } from "@/domain/ssh/types";

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
});

function savedProfile(): SshConnectionProfile {
  return {
    id: "saved",
    name: "geo-prod",
    host: "geo.example.com",
    port: 22,
    username: "deploy",
    authType: "password",
    group: "",
    tags: [],
    favorite: false,
    saveCredentials: false,
    portForwards: [],
    createdAt: 0,
    updatedAt: 0,
  } as SshConnectionProfile;
}

/** Name, host, username in the order the general tab renders them. */
function fieldValues(view: VueWrapper) {
  const [name, host, username] = view
    .findAll(".ssh-form-section--connection-fields .el-input input")
    .map((input) => (input.element as HTMLInputElement).value);
  const port = (view.get(".ssh-form-field--port input").element as HTMLInputElement).value;
  return { name, host, username, port };
}

describe("SshConnectionDialog initialAddress", () => {
  it("prefills a new host from a user@host:port address", async () => {
    wrapper = mount(SshConnectionDialog, {
      attachTo: document.body,
      props: { modelValue: true, initialAddress: "root@10.0.0.5:2222" },
    });
    await flushPromises();

    expect(fieldValues(wrapper)).toEqual({ name: "root@10.0.0.5", host: "10.0.0.5", username: "root", port: "2222" });
    expect((wrapper.get("#ssh-quick-connect").element as HTMLInputElement).value).toBe("root@10.0.0.5:2222");
  });

  it("lets an existing draft win over the address", async () => {
    wrapper = mount(SshConnectionDialog, {
      attachTo: document.body,
      props: { modelValue: true, initial: { profile: savedProfile() }, initialAddress: "root@10.0.0.5:2222" },
    });
    await flushPromises();

    expect(fieldValues(wrapper)).toEqual({ name: "geo-prod", host: "geo.example.com", username: "deploy", port: "22" });
  });
});
