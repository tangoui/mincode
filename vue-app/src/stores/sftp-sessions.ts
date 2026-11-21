import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { FileTransferProtocol } from "@/domain/ssh/types";
import { useSshConnectionsStore } from "@/stores/ssh-connections";

export interface ConnectedSftpSession {
  id: string;
  name: string;
  profileId: string;
  protocol: FileTransferProtocol;
}

type ConnectedSftpSessionInput = Omit<ConnectedSftpSession, "protocol"> & {
  protocol?: FileTransferProtocol;
};

export const useSftpSessionsStore = defineStore("sftp-sessions", () => {
  const sessions = ref<ConnectedSftpSession[]>([]);

  function connected(session: ConnectedSftpSessionInput) {
    const profile = useSshConnectionsStore().findProfile(session.profileId);
    const next: ConnectedSftpSession = {
      ...session,
      protocol: session.protocol ?? profile?.fileTransfer?.protocol ?? "sftp",
    };
    const index = sessions.value.findIndex((item) => item.id === next.id);
    if (index >= 0) sessions.value.splice(index, 1, next);
    else sessions.value.push(next);
  }

  function disconnected(sessionId: string) {
    sessions.value = sessions.value.filter((session) => session.id !== sessionId);
  }

  return { sessions, connected, disconnected };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSftpSessionsStore, import.meta.hot));
}
