import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import SshKnownHostsDialog from "@/components/ssh/SshKnownHostsDialog.vue";
import { listKnownHosts } from "@/services/ssh-security-service";

vi.mock("@/services/ssh-security-service", () => ({
  listKnownHosts: vi.fn().mockResolvedValue({
    path: "/test/.ssh/known_hosts",
    entries: [
      { id: "1", hosts: ["alpha.example"], algorithm: "ssh-ed25519", fingerprint: "SHA256:alpha", line: 1 },
      { id: "2", hosts: ["beta.example"], algorithm: "ssh-rsa", fingerprint: "SHA256:beta", line: 2 },
    ],
  }),
  removeKnownHost: vi.fn(),
}));

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { wrapper?.unmount(); vi.clearAllMocks(); });

describe("SshKnownHostsDialog header", () => {
  it("keeps search and refresh beside the heading, and still filters and refreshes", async () => {
    wrapper = mount(SshKnownHostsDialog, { props: { modelValue: true }, attachTo: document.body });
    await flushPromises();
    const header = document.querySelector(".ssh-security-dialog .el-dialog__header");
    expect(header?.querySelector(".ssh-security-dialog__heading code")?.textContent).toBe("/test/.ssh/known_hosts");
    expect(header?.querySelector(".ssh-security-dialog__toolbar .app-search-field")).toBeTruthy();
    expect(document.querySelector(".ssh-security-dialog__body .ssh-security-dialog__toolbar")).toBeNull();
    const input = header?.querySelector("input");
    expect(input).toBeTruthy();
    input!.value = "beta";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    const rows = [...document.querySelectorAll(".el-table__body-wrapper .el-table__row")];
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain("beta.example");
    const refresh = header?.querySelector<HTMLButtonElement>(".ssh-security-dialog__toolbar > button");
    refresh?.click();
    await flushPromises();
    expect(listKnownHosts).toHaveBeenCalledTimes(2);
    expect(input!.value).toBe("beta");
  });
});
