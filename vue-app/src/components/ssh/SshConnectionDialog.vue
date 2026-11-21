<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from "vue";
import { Close, Delete, Plus, Refresh, Star } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type {
  FileTransferProtocol,
  SshAgentStatus,
  SshAuthType,
  SshConnectionDraft,
  SshConnectionProfile,
  SshPortForwardRule,
  SshRuntimeCredentials,
} from "@/domain/ssh/types";
import { createSshProfileId, sanitizeSshProfile } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import {
  chooseNativeFileOrDirectory,
  invokeBackend,
  isDesktopRuntime,
} from "@/services/desktop-bridge";
import { getSshAgentStatus } from "@/services/ssh-agent-service";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useSshMfaStore } from "@/stores/ssh-mfa";
import AppChevron from "@/components/ui/AppChevron.vue";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    initial?: SshConnectionDraft;
    /** `user@host[:port]` to prefill a new host with; ignored when `initial` is given. */
    initialAddress?: string;
    groupOptions?: string[];
    tagOptions?: string[];
    connectAfterSave?: boolean;
  }>(),
  {
    groupOptions: () => [],
    tagOptions: () => [],
    connectAfterSave: true,
  }
);

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  connect: [draft: SshConnectionDraft];
  saved: [profile: SshConnectionProfile];
}>();

interface EditableForward {
  id: string;
  type: "L" | "R" | "D";
  bindPort: number | undefined;
  targetHost: string;
  targetPort: number | undefined;
  label: string;
}

interface ConnectionForm {
  profileId: string;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: SshAuthType;
  password: string;
  keyFile: string;
  passphrase: string;
  totpUri: string;
  description: string;
  defaultPath: string;
  postConnectCommands: string;
  group: string;
  tags: string[];
  favorite: boolean;
  algorithmPolicy: "modern" | "legacy";
  keepAliveEnabled: boolean;
  keepAliveIntervalSeconds: number;
  keepAliveMaxMisses: number;
  reconnectEnabled: boolean;
  reconnectMaxAttempts: number;
  saveCredentials: boolean;
  fileTransferEnabled: boolean;
  fileTransferProtocol: FileTransferProtocol;
  ftpHost: string;
  ftpPort: number;
  ftpUsername: string;
  ftpPassword: string;
  ftpViaCurrentSsh: boolean;
  ftpTlsServerName: string;
  useJumpHost: boolean;
  jumpHost: string;
  jumpPort: number;
  jumpUsername: string;
  jumpAuthType: "password" | "key" | "agent";
  jumpPassword: string;
  jumpKeyFile: string;
  jumpPassphrase: string;
  useSocksProxy: boolean;
  socksHost: string;
  socksPort: number;
  socksUsername: string;
  socksPassword: string;
  portForwards: EditableForward[];
}

const { t } = useVueI18n();
const connections = useSshConnectionsStore();
const sshMfa = useSshMfaStore();
const testing = ref(false);
const quickConnect = ref("");
const activeTab = ref<"general" | "file-transfer" | "network" | "advanced">("general");
const agentStatus = ref<SshAgentStatus | null>(null);
const agentStatusLoading = ref(false);
const form = reactive<ConnectionForm>(emptyForm());
let agentStatusRequestId = 0;

function emptyForm(): ConnectionForm {
  return {
    profileId: createSshProfileId(),
    name: "",
    host: "",
    port: 22,
    username: "",
    authType: "password",
    password: "",
    keyFile: "",
    passphrase: "",
    totpUri: "",
    description: "",
    defaultPath: "",
    postConnectCommands: "",
    group: "",
    tags: [],
    favorite: false,
    algorithmPolicy: "modern",
    keepAliveEnabled: true,
    keepAliveIntervalSeconds: 30,
    keepAliveMaxMisses: 3,
    reconnectEnabled: true,
    reconnectMaxAttempts: 5,
    saveCredentials: isDesktopRuntime(),
    fileTransferEnabled: true,
    fileTransferProtocol: "sftp",
    ftpHost: "",
    ftpPort: 21,
    ftpUsername: "",
    ftpPassword: "",
    ftpViaCurrentSsh: false,
    ftpTlsServerName: "",
    useJumpHost: false,
    jumpHost: "",
    jumpPort: 22,
    jumpUsername: "",
    jumpAuthType: "key",
    jumpPassword: "",
    jumpKeyFile: "",
    jumpPassphrase: "",
    useSocksProxy: false,
    socksHost: "",
    socksPort: 1080,
    socksUsername: "",
    socksPassword: "",
    portForwards: [],
  };
}

async function choosePrivateKeyPath(target: "primary" | "jump") {
  if (!isDesktopRuntime()) return;
  const selected = await chooseNativeFileOrDirectory(t("ssh.choosePrivateKeyPath"));
  if (!selected) return;
  if (target === "jump") form.jumpKeyFile = selected;
  else form.keyFile = selected;
}

function resetSshAgentStatus() {
  agentStatusRequestId += 1;
  agentStatusLoading.value = false;
  agentStatus.value = null;
}

async function refreshSshAgentStatus() {
  if (!isDesktopRuntime()) {
    resetSshAgentStatus();
    return;
  }
  const requestId = ++agentStatusRequestId;
  agentStatusLoading.value = true;
  try {
    const status = await getSshAgentStatus();
    if (requestId === agentStatusRequestId) agentStatus.value = status;
  } catch {
    if (requestId === agentStatusRequestId) {
      agentStatus.value = { state: "unavailable", identityCount: 0, identities: [] };
    }
  } finally {
    if (requestId === agentStatusRequestId) agentStatusLoading.value = false;
  }
}

function applyDraft(draft?: SshConnectionDraft) {
  const next = emptyForm();
  if (draft) {
    const { profile } = draft;
    const credentials = { ...connections.credentialsFor(profile.id), ...draft.credentials };
    Object.assign(next, {
      profileId: profile.id,
      name: profile.name,
      host: profile.host,
      port: profile.port,
      username: profile.username,
      authType: profile.authType,
      password: credentials?.password ?? "",
      keyFile: profile.keyFile ?? "",
      passphrase: credentials?.passphrase ?? "",
      totpUri: credentials?.totpUri ?? "",
      description: profile.description ?? "",
      defaultPath: profile.defaultPath ?? "",
      postConnectCommands: (profile.postConnectCommands ?? []).join("\n"),
      group: profile.group ?? "",
      tags: [...profile.tags],
      favorite: profile.favorite,
      algorithmPolicy: profile.algorithmPolicy ?? "modern",
      keepAliveEnabled: (profile.keepAlive?.intervalSeconds ?? 30) > 0,
      keepAliveIntervalSeconds: profile.keepAlive?.intervalSeconds || 30,
      keepAliveMaxMisses: profile.keepAlive?.maxMisses ?? 3,
      reconnectEnabled: profile.reconnect?.enabled !== false,
      reconnectMaxAttempts: profile.reconnect?.maxAttempts ?? 5,
      saveCredentials: isDesktopRuntime() && profile.saveCredentials !== false,
      fileTransferEnabled: profile.fileTransfer?.enabled !== false,
      fileTransferProtocol: profile.fileTransfer?.protocol ?? "sftp",
      ftpHost: profile.fileTransfer?.host ?? profile.host,
      ftpPort:
        profile.fileTransfer?.protocol === "ftp" ||
        profile.fileTransfer?.protocol === "ftps-explicit"
          ? profile.fileTransfer.port
          : 21,
      ftpUsername: profile.fileTransfer?.username ?? profile.username,
      ftpPassword: credentials?.ftpPassword ?? "",
      ftpViaCurrentSsh: profile.fileTransfer?.viaCurrentSsh ?? false,
      ftpTlsServerName: profile.fileTransfer?.tlsServerName ?? "",
      useJumpHost: Boolean(profile.jumpHost),
      jumpHost: profile.jumpHost?.host ?? "",
      jumpPort: profile.jumpHost?.port ?? 22,
      jumpUsername: profile.jumpHost?.username ?? "",
      jumpAuthType: profile.jumpHost?.authType ?? "key",
      jumpPassword: credentials?.jumpPassword ?? "",
      jumpKeyFile: profile.jumpHost?.keyFile ?? "",
      jumpPassphrase: credentials?.jumpPassphrase ?? "",
      useSocksProxy: Boolean(profile.socksProxy),
      socksHost: profile.socksProxy?.host ?? "",
      socksPort: profile.socksProxy?.port ?? 1080,
      socksUsername: profile.socksProxy?.username ?? "",
      socksPassword: credentials?.socksPassword ?? "",
      portForwards: profile.portForwards.map((rule) => ({
        id: rule.id,
        type: rule.type,
        bindPort: rule.bindPort,
        targetHost: rule.targetHost,
        targetPort: rule.targetPort,
        label: rule.label ?? "",
      })),
    });
  }
  Object.assign(form, next);
  quickConnect.value = draft ? `${next.username}@${next.host}:${next.port}` : "";
}

watch(
  [() => props.modelValue, () => props.initial, () => props.initialAddress],
  ([visible]) => {
    if (!visible) return;
    applyDraft(props.initial);
    if (!props.initial && props.initialAddress) applyQuickConnect(props.initialAddress);
  },
  { immediate: true }
);

watch(
  () => [props.modelValue, form.authType] as const,
  ([visible, authType]) => {
    if (visible && authType === "agent") {
      void refreshSshAgentStatus();
      return;
    }
    resetSshAgentStatus();
  },
  { flush: "post" }
);

onBeforeUnmount(() => {
  agentStatusRequestId += 1;
});

function applyQuickConnect(value: string) {
  quickConnect.value = value;
  const match = value.trim().match(/^(?:([^@\s]+)@)?(\[[^\]]+\]|[^:\s]+)(?::(\d{1,5}))?$/);
  if (!match) return;
  if (match[1]) form.username = match[1];
  form.host = match[2]?.replace(/^\[|\]$/g, "") ?? "";
  if (match[3]) {
    const port = Number(match[3]);
    if (validPort(port)) form.port = port;
  }
  if (!form.name.trim() && form.username && form.host) form.name = `${form.username}@${form.host}`;
}

function addPortForward() {
  form.portForwards.push({
    id: createSshProfileId("ssh-forward"),
    type: "L",
    bindPort: undefined,
    targetHost: "localhost",
    targetPort: undefined,
    label: "",
  });
}

function removePortForward(index: number) {
  form.portForwards.splice(index, 1);
}

function validPort(port: number | undefined) {
  return Number.isInteger(port) && Number(port) >= 1 && Number(port) <= 65535;
}

function buildDraft(options: { validateFileTransfer?: boolean } = {}): SshConnectionDraft | null {
  if (!form.host.trim() || !form.username.trim() || !validPort(form.port)) {
    activeTab.value = "general";
    ElMessage.warning(t("ssh.validation.connection"));
    return null;
  }
  if (form.authType === "password" && !form.password) {
    activeTab.value = "general";
    ElMessage.warning(t("ssh.validation.password"));
    return null;
  }
  if (form.useJumpHost) {
    if (!form.jumpHost.trim() || !form.jumpUsername.trim() || !validPort(form.jumpPort)) {
      activeTab.value = "network";
      ElMessage.warning(t("ssh.validation.jumpHost"));
      return null;
    }
    if (form.jumpAuthType === "password" && !form.jumpPassword) {
      activeTab.value = "network";
      ElMessage.warning(t("ssh.validation.jumpPassword"));
      return null;
    }
  }
  if (form.useSocksProxy) {
    if (!form.socksHost.trim() || !validPort(form.socksPort)) {
      activeTab.value = "network";
      ElMessage.warning(t("ssh.validation.socksProxy"));
      return null;
    }
    if (form.socksUsername.trim() && !form.socksPassword) {
      activeTab.value = "network";
      ElMessage.warning(t("ssh.validation.socksPassword"));
      return null;
    }
  }
  if (
    options.validateFileTransfer !== false &&
    form.fileTransferEnabled &&
    form.fileTransferProtocol !== "sftp"
  ) {
    if (!form.ftpHost.trim() || !form.ftpUsername.trim() || !validPort(form.ftpPort)) {
      activeTab.value = "file-transfer";
      ElMessage.warning(t("ssh.validation.fileTransferConnection"));
      return null;
    }
    if (!form.ftpPassword) {
      activeTab.value = "file-transfer";
      ElMessage.warning(t("ssh.validation.ftpPassword"));
      return null;
    }
  }

  const portForwards: SshPortForwardRule[] = [];
  for (const rule of form.portForwards) {
    const dynamic = rule.type === "D";
    if (
      !validPort(rule.bindPort) ||
      (!dynamic && (!validPort(rule.targetPort) || !rule.targetHost.trim()))
    ) {
      activeTab.value = "network";
      ElMessage.warning(t("ssh.validation.portForward"));
      return null;
    }
    portForwards.push({
      id: rule.id,
      type: rule.type,
      bindPort: rule.bindPort!,
      targetHost: dynamic ? "" : rule.targetHost.trim(),
      targetPort: dynamic ? 0 : rule.targetPort!,
      label: rule.label.trim() || undefined,
    });
  }

  const postConnectCommands = form.postConnectCommands
    .split(/\r?\n/)
    .map((command) => command.trim())
    .filter(Boolean);
  if (postConnectCommands.length > 32 || postConnectCommands.some((command) => command.length > 8192)) {
    activeTab.value = "general";
    ElMessage.warning(t("ssh.validation.postConnectCommands"));
    return null;
  }

  const now = Date.now();
  const profile: SshConnectionProfile = sanitizeSshProfile({
    id: form.profileId,
    name: form.name.trim() || `${form.username.trim()}@${form.host.trim()}`,
    host: form.host,
    port: form.port,
    username: form.username,
    authType: form.authType,
    keyFile: form.keyFile,
    description: form.description,
    defaultPath: form.defaultPath,
    postConnectCommands,
    group: form.group,
    tags: [...form.tags],
    favorite: form.favorite,
    algorithmPolicy: form.algorithmPolicy,
    keepAlive: {
      intervalSeconds: form.keepAliveEnabled ? form.keepAliveIntervalSeconds : 0,
      maxMisses: form.keepAliveMaxMisses,
    },
    reconnect: {
      enabled: form.reconnectEnabled,
      maxAttempts: form.reconnectMaxAttempts,
      baseDelayMs: 1000,
      maxDelayMs: 15_000,
    },
    sshConfigSource: props.initial?.profile.sshConfigSource,
    saveCredentials: form.saveCredentials,
    fileTransfer: {
      enabled: form.fileTransferEnabled,
      protocol: form.fileTransferProtocol,
      host: form.fileTransferProtocol === "sftp" ? form.host : form.ftpHost,
      port: form.fileTransferProtocol === "sftp" ? form.port : form.ftpPort,
      username: form.fileTransferProtocol === "sftp" ? form.username : form.ftpUsername,
      viaCurrentSsh:
        form.fileTransferProtocol !== "sftp" && form.ftpViaCurrentSsh,
      tlsServerName:
        form.fileTransferProtocol === "ftps-explicit"
          ? form.ftpTlsServerName.trim() || undefined
          : undefined,
    },
    portForwards,
    jumpHost: form.useJumpHost
      ? {
          host: form.jumpHost,
          port: form.jumpPort,
          username: form.jumpUsername,
          authType: form.jumpAuthType,
          keyFile: form.jumpKeyFile,
        }
      : undefined,
    socksProxy: form.useSocksProxy
      ? {
          host: form.socksHost,
          port: form.socksPort,
          username: form.socksUsername.trim() || undefined,
        }
      : undefined,
    createdAt: props.initial?.profile.createdAt ?? now,
    updatedAt: now,
  });

  const credentials: SshRuntimeCredentials = {
    password: form.password || undefined,
    passphrase: form.passphrase || undefined,
    jumpPassword: form.jumpPassword || undefined,
    jumpPassphrase: form.jumpPassphrase || undefined,
    socksPassword: form.useSocksProxy && form.socksPassword ? form.socksPassword : undefined,
    ftpPassword:
      form.fileTransferEnabled && form.fileTransferProtocol !== "sftp"
        ? form.ftpPassword || undefined
        : undefined,
    totpUri: form.totpUri.trim() || undefined,
  };
  return { profile, credentials };
}

function toBackendPayload(draft: SshConnectionDraft) {
  const { profile, credentials } = draft;
  return {
    host: profile.host,
    port: profile.port,
    username: profile.username,
    authType: profile.authType,
    password: credentials?.password,
    keyFile: profile.keyFile,
    passphrase: credentials?.passphrase,
    totpUri: credentials?.totpUri,
    algorithmPolicy: profile.algorithmPolicy,
    jumpHost: profile.jumpHost
      ? {
          ...profile.jumpHost,
          algorithmPolicy: profile.algorithmPolicy,
          password: credentials?.jumpPassword,
          passphrase: credentials?.jumpPassphrase,
        }
      : undefined,
    socksProxy: profile.socksProxy
      ? {
          host: profile.socksProxy.host,
          port: profile.socksProxy.port,
          username: profile.socksProxy.username,
          password: credentials?.socksPassword,
        }
      : undefined,
  };
}

async function testConnection() {
  const draft = buildDraft({ validateFileTransfer: false });
  if (!draft) return;
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("terminal.onlyDesktop"));
    return;
  }
  // The backend gives the temporary verification session this stable ID. Registering
  // it here lets a connection test use the same local TOTP auto-fill as a terminal.
  const testSessionId = "__mfa_test";
  sshMfa.ensureListening();
  sshMfa.registerCredentials(testSessionId, draft.credentials);
  testing.value = true;
  try {
    await invokeBackend<string>("test_ssh_connection", { payload: toBackendPayload(draft) });
    ElMessage.success(t("ssh.test.success"));
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.test.failed"));
  } finally {
    sshMfa.unregisterSession(testSessionId);
    testing.value = false;
  }
}

async function saveConnection() {
  const draft = buildDraft();
  if (!draft) return;
  if (!connections.hydrated) await connections.hydrate();
  draft.profile = connections.saveProfile(draft.profile);
  try {
    await connections.persistProfiles();
    if (form.saveCredentials) {
      await connections.saveCredentials(draft.profile.id, draft.credentials ?? {});
    } else {
      await connections.removeCredentials(draft.profile.id);
    }
    emit("saved", draft.profile);
    if (props.connectAfterSave) emit("connect", draft);
    emit("update:modelValue", false);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.credentialsSaveFailed"));
  }
}
</script>

<template>
  <section v-if="modelValue" class="ssh-connection-form" aria-live="polite">
    <el-scrollbar
      class="ssh-connection-form__scroll"
      tag="form"
      view-class="ssh-connection-form__body"
      @submit.prevent="saveConnection"
    >
      <section class="ssh-form-section ssh-form-section--quick">
        <div class="ssh-quick-connect-row">
          <label class="ssh-form-label" for="ssh-quick-connect">{{
            t("ssh.form.quickConnect")
          }}</label>
          <el-input
            id="ssh-quick-connect"
            class="ssh-quick-connect"
            :model-value="quickConnect"
            :placeholder="t('ssh.form.quickConnectPlaceholder')"
            autocomplete="off"
            @update:model-value="applyQuickConnect"
          >
            <template #prepend>ssh</template>
          </el-input>
          <el-button
            text
            :icon="Close"
            class="ssh-connection-form__close"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="emit('update:modelValue', false)"
          />
        </div>
      </section>

      <el-tabs v-model="activeTab" class="ssh-connection-tabs">
        <el-tab-pane :label="t('ssh.tab.general')" name="general">
          <section class="ssh-form-section ssh-form-section--connection-fields">
        <div class="ssh-form-grid ssh-form-grid--two">
          <label class="ssh-form-field is-required">
            <span>{{ t("ssh.profileName") }}</span>
            <el-input
              v-model="form.name"
              :placeholder="t('ssh.form.namePlaceholder')"
              autocomplete="off"
            />
          </label>
          <label class="ssh-form-field is-required">
            <span>{{ t("ssh.host") }}</span>
            <el-input v-model="form.host" placeholder="192.168.1.100" autocomplete="off" />
          </label>
          <label class="ssh-form-field is-required">
            <span>{{ t("ssh.username") }}</span>
            <el-input v-model="form.username" placeholder="root" autocomplete="username" />
          </label>
          <label class="ssh-form-field ssh-form-field--port is-required">
            <span>{{ t("ssh.port") }}</span>
            <el-input-number v-model="form.port" :min="1" :max="65535" :controls="false" />
          </label>
        </div>
      </section>

      <section class="ssh-form-section">
        <div class="ssh-form-section__heading">
          <strong>{{ t("ssh.section.authentication") }}</strong>
        </div>
        <el-segmented
          v-model="form.authType"
          class="ssh-auth-switcher"
          :options="[
            { value: 'password', label: t('ssh.auth.password') },
            { value: 'key', label: t('ssh.auth.key') },
            { value: 'agent', label: t('ssh.auth.agent') },
            { value: 'keyboard-interactive', label: t('ssh.auth.keyboard') },
          ]"
          :aria-label="t('ssh.authType')"
        />
        <div class="ssh-form-grid ssh-form-grid--two ssh-auth-fields">
          <label
            v-if="form.authType === 'password'"
            class="ssh-form-field ssh-form-field--wide is-required"
          >
            <span>{{ t("ssh.password") }}</span>
            <el-input
              v-model="form.password"
              type="password"
              show-password
              autocomplete="new-password"
            />
          </label>
          <template v-if="form.authType === 'key' || form.authType === 'keyboard-interactive'">
            <label class="ssh-form-field ssh-form-field--wide">
              <span>{{ t("ssh.keyFile") }}</span>
              <el-input
                v-model="form.keyFile"
                :placeholder="t('ssh.keyFilePlaceholder')"
                autocomplete="off"
              >
                <template #append>
                  <el-button
                    :disabled="!isDesktopRuntime()"
                    :aria-label="t('ssh.choosePrivateKeyPath')"
                    @click="choosePrivateKeyPath('primary')"
                  >
                    {{ t("ssh.choosePath") }}
                  </el-button>
                </template>
              </el-input>
            </label>
            <label class="ssh-form-field ssh-form-field--wide">
              <span>{{ t("ssh.passphrase") }}</span>
              <el-input
                v-model="form.passphrase"
                type="password"
                show-password
                autocomplete="new-password"
              />
            </label>
          </template>
          <label
            v-if="form.authType === 'keyboard-interactive'"
            class="ssh-form-field ssh-form-field--wide"
          >
            <span>{{ t("ssh.mfa.totpUri") }}</span>
            <el-input
              v-model="form.totpUri"
              type="password"
              show-password
              :placeholder="t('ssh.mfa.totpUriPlaceholder')"
              autocomplete="off"
            />
            <p class="ssh-form-inline-hint">{{ t("ssh.mfa.totpUriHint") }}</p>
          </label>
          <div v-if="form.authType === 'agent'" class="ssh-agent-status">
            <p class="ssh-form-inline-hint">{{ t("ssh.form.agentHint") }}</p>
            <p v-if="!isDesktopRuntime()" class="ssh-form-inline-hint">
              {{ t("ssh.agent.browserPreview") }}
            </p>
            <template v-else>
              <div
                class="ssh-agent-status__card"
                :class="`is-${agentStatus?.state ?? 'checking'}`"
                role="status"
                aria-live="polite"
              >
                <div class="ssh-agent-status__heading">
                  <strong>{{ t("ssh.agent.statusTitle") }}</strong>
                  <el-button
                    text
                    size="small"
                    :loading="agentStatusLoading"
                    :disabled="agentStatusLoading"
                    :aria-label="t('ssh.agent.refresh')"
                    @click="refreshSshAgentStatus"
                  >
                    <el-icon v-if="!agentStatusLoading"><Refresh /></el-icon>
                    {{ t("ssh.agent.refresh") }}
                  </el-button>
                </div>
                <p v-if="agentStatusLoading" class="ssh-agent-status__message">
                  {{ t("ssh.agent.status.checking") }}
                </p>
                <template v-else-if="agentStatus?.state === 'ready'">
                  <p class="ssh-agent-status__message">
                    {{
                      t("ssh.agent.status.ready").replace(
                        "{count}",
                        String(agentStatus.identityCount)
                      )
                    }}
                  </p>
                  <ul
                    v-if="agentStatus.identities.length"
                    class="ssh-agent-status__identities"
                    :aria-label="t('ssh.agent.loadedIdentities')"
                  >
                    <li v-for="identity in agentStatus.identities" :key="identity.fingerprint">
                      <code>{{ identity.algorithm }}</code>
                      <code>{{ identity.fingerprint }}</code>
                    </li>
                  </ul>
                </template>
                <template v-else-if="agentStatus?.state === 'empty'">
                  <p class="ssh-agent-status__message">{{ t("ssh.agent.status.empty") }}</p>
                  <p class="ssh-agent-status__guidance">{{ t("ssh.agent.loadKeyHint") }}</p>
                  <code class="ssh-agent-status__command">ssh-add ~/.ssh/id_ed25519</code>
                </template>
                <template v-else-if="agentStatus?.state === 'unavailable'">
                  <p class="ssh-agent-status__message">{{ t("ssh.agent.status.unavailable") }}</p>
                  <p class="ssh-agent-status__guidance">{{ t("ssh.agent.unavailableHint") }}</p>
                </template>
              </div>
            </template>
          </div>
          <div class="ssh-form-field ssh-form-field--wide">
            <div class="ssh-connection-options">
              <el-checkbox v-model="form.saveCredentials" :disabled="!isDesktopRuntime()">
                {{ t("ssh.saveCredentials") }}
              </el-checkbox>
              <el-checkbox v-model="form.favorite" class="ssh-favorite-toggle">
                <el-icon :class="{ 'is-favorite': form.favorite }"><Star /></el-icon>
                <span>{{ t("ssh.favorite") }}</span>
              </el-checkbox>
            </div>
            <p class="ssh-form-inline-hint">
              {{
                t(isDesktopRuntime() ? "ssh.credentialsSecurityNote" : "ssh.credentialsDesktopOnly")
              }}
            </p>
          </div>
        </div>
      </section>

      <section class="ssh-form-section">
        <div class="ssh-form-section__heading">
          <strong>{{ t("ssh.section.organization") }}</strong>
        </div>
        <div class="ssh-form-grid ssh-form-grid--two">
          <label class="ssh-form-field ssh-form-field--wide">
            <span>{{ t("ssh.description") }}</span>
            <el-input
              v-model="form.description"
              type="textarea"
              :rows="2"
              :placeholder="t('ssh.descriptionPlaceholder')"
            />
          </label>
          <label class="ssh-form-field">
            <span>{{ t("ssh.defaultPath") }}</span>
            <el-input
              v-model="form.defaultPath"
              :placeholder="t('ssh.defaultPathPlaceholder')"
              autocomplete="off"
            />
          </label>
          <label class="ssh-form-field ssh-form-field--wide">
            <span>{{ t("ssh.postConnectCommands") }}</span>
            <el-input
              v-model="form.postConnectCommands"
              type="textarea"
              :rows="3"
              :maxlength="65536"
              :placeholder="t('ssh.postConnectCommandsPlaceholder')"
            />
            <small class="ssh-form-inline-hint">{{ t("ssh.postConnectCommandsHint") }}</small>
          </label>
          <label class="ssh-form-field">
            <span>{{ t("ssh.group") }}</span>
            <el-select
              v-model="form.group"
              clearable
              filterable
              allow-create
              default-first-option
              :placeholder="t('ssh.groupPlaceholder')"
            >
              <el-option v-for="group in groupOptions" :key="group" :label="group" :value="group" />
            </el-select>
          </label>
          <label class="ssh-form-field">
            <span>{{ t("ssh.tags") }}</span>
            <el-select
              v-model="form.tags"
              multiple
              clearable
              filterable
              allow-create
              default-first-option
              :placeholder="t('ssh.tagsPlaceholder')"
            >
              <el-option v-for="tag in tagOptions" :key="tag" :label="tag" :value="tag" />
            </el-select>
          </label>
        </div>
      </section>
        </el-tab-pane>

        <el-tab-pane :label="t('ssh.section.fileTransfer')" name="file-transfer">
          <section class="ssh-form-section">
        <div class="ssh-form-section__heading ssh-form-section__heading--toggle">
          <strong>{{ t("ssh.section.fileTransfer") }}</strong>
          <label class="ssh-jump-host-toggle">
            <span>{{ t("ssh.fileTransfer.enable") }}</span>
            <el-switch v-model="form.fileTransferEnabled" size="small" />
          </label>
        </div>
        <div v-if="form.fileTransferEnabled" class="ssh-form-inset">
          <el-segmented
            v-model="form.fileTransferProtocol"
            class="ssh-auth-switcher ssh-auth-switcher--compact"
            :options="[
              { value: 'sftp', label: t('ssh.fileTransfer.protocol.sftp') },
              { value: 'ftp', label: t('ssh.fileTransfer.protocol.ftp') },
              { value: 'ftps-explicit', label: t('ssh.fileTransfer.protocol.ftpsExplicit') },
            ]"
            :aria-label="t('ssh.fileTransfer.protocol.label')"
          />
          <p v-if="form.fileTransferProtocol === 'sftp'" class="ssh-form-inline-hint">
            {{ t("ssh.fileTransfer.sftpCurrentSsh") }}
          </p>
          <template v-else>
            <div class="ssh-form-grid ssh-form-grid--three">
              <label class="ssh-form-field ssh-form-field--host is-required">
                <span>{{ t("ssh.fileTransfer.host") }}</span>
                <el-input v-model="form.ftpHost" autocomplete="off" />
              </label>
              <label class="ssh-form-field is-required">
                <span>{{ t("ssh.fileTransfer.username") }}</span>
                <el-input v-model="form.ftpUsername" autocomplete="username" />
              </label>
              <label class="ssh-form-field is-required">
                <span>{{ t("ssh.fileTransfer.port") }}</span>
                <el-input-number
                  v-model="form.ftpPort"
                  :min="1"
                  :max="65535"
                  :controls="false"
                />
              </label>
            </div>
            <div class="ssh-form-grid ssh-form-grid--two ssh-auth-fields">
              <label class="ssh-form-field is-required">
                <span>{{ t("ssh.fileTransfer.password") }}</span>
                <el-input
                  v-model="form.ftpPassword"
                  type="password"
                  show-password
                  autocomplete="new-password"
                />
              </label>
              <label
                v-if="form.fileTransferProtocol === 'ftps-explicit'"
                class="ssh-form-field"
              >
                <span>{{ t("ssh.fileTransfer.tlsServerName") }}</span>
                <el-input
                  v-model="form.ftpTlsServerName"
                  :placeholder="t('ssh.fileTransfer.tlsServerNamePlaceholder')"
                  autocomplete="off"
                />
              </label>
            </div>
            <el-checkbox v-model="form.ftpViaCurrentSsh">
              {{ t("ssh.fileTransfer.viaCurrentSsh") }}
            </el-checkbox>
            <el-alert
              v-if="form.fileTransferProtocol === 'ftp'"
              type="warning"
              :closable="false"
              :title="t('ssh.fileTransfer.ftpWarning')"
            />
            <el-alert
              v-else
              type="info"
              :closable="false"
              :title="t('ssh.fileTransfer.ftpsCertificateHint')"
            />
            <p class="ssh-form-inline-hint">{{ t("ssh.fileTransfer.passiveOnly") }}</p>
          </template>
        </div>
      </section>
        </el-tab-pane>

        <el-tab-pane :label="t('ssh.tab.network')" name="network">
          <section class="ssh-form-section">
        <div class="ssh-form-section__heading ssh-form-section__heading--toggle">
          <strong>{{ t("ssh.section.jumpHost") }}</strong>
          <label class="ssh-jump-host-toggle">
            <span>{{ t("ssh.jumpHost.enable") }}</span>
            <el-switch v-model="form.useJumpHost" />
          </label>
        </div>
        <div v-if="form.useJumpHost" class="ssh-form-inset">
          <div class="ssh-form-grid ssh-form-grid--three">
            <label class="ssh-form-field ssh-form-field--host is-required"
              ><span>{{ t("ssh.host") }}</span
              ><el-input v-model="form.jumpHost" autocomplete="off"
            /></label>
            <label class="ssh-form-field is-required"
              ><span>{{ t("ssh.username") }}</span
              ><el-input v-model="form.jumpUsername" autocomplete="off"
            /></label>
            <label class="ssh-form-field is-required"
              ><span>{{ t("ssh.port") }}</span
              ><el-input-number v-model="form.jumpPort" :min="1" :max="65535" :controls="false"
            /></label>
          </div>
          <el-segmented
            v-model="form.jumpAuthType"
            class="ssh-auth-switcher ssh-auth-switcher--compact"
            :options="[
              { value: 'password', label: t('ssh.auth.password') },
              { value: 'key', label: t('ssh.auth.key') },
              { value: 'agent', label: t('ssh.auth.agent') },
            ]"
          />
          <label v-if="form.jumpAuthType === 'password'" class="ssh-form-field"
            ><span>{{ t("ssh.password") }}</span
            ><el-input v-model="form.jumpPassword" type="password" show-password
          /></label>
          <div v-if="form.jumpAuthType === 'key'" class="ssh-form-grid ssh-form-grid--two">
            <label class="ssh-form-field"
              ><span>{{ t("ssh.keyFile") }}</span
              ><el-input v-model="form.jumpKeyFile">
                <template #append>
                  <el-button
                    :disabled="!isDesktopRuntime()"
                    :aria-label="t('ssh.choosePrivateKeyPath')"
                    @click="choosePrivateKeyPath('jump')"
                  >
                    {{ t("ssh.choosePath") }}
                  </el-button>
                </template>
              </el-input></label
            >
            <label class="ssh-form-field"
              ><span>{{ t("ssh.passphrase") }}</span
              ><el-input v-model="form.jumpPassphrase" type="password" show-password
            /></label>
          </div>
        </div>
      </section>

      <section class="ssh-form-section">
        <div class="ssh-form-section__heading ssh-form-section__heading--toggle">
          <strong>{{ t("ssh.section.socksProxy") }}</strong>
          <label class="ssh-jump-host-toggle">
            <span>{{ t("ssh.socksProxy.enable") }}</span>
            <el-switch v-model="form.useSocksProxy" />
          </label>
        </div>
        <div v-if="form.useSocksProxy" class="ssh-form-inset">
          <div class="ssh-form-grid ssh-form-grid--three">
            <label class="ssh-form-field ssh-form-field--host is-required"
              ><span>{{ t("ssh.socksProxy.host") }}</span
              ><el-input v-model="form.socksHost" autocomplete="off"
            /></label>
            <label class="ssh-form-field is-required"
              ><span>{{ t("ssh.port") }}</span
              ><el-input-number v-model="form.socksPort" :min="1" :max="65535" :controls="false"
            /></label>
            <label class="ssh-form-field"
              ><span>{{ t("ssh.socksProxy.username") }}</span
              ><el-input v-model="form.socksUsername" autocomplete="off"
            /></label>
          </div>
          <label v-if="form.socksUsername.trim()" class="ssh-form-field is-required"
            ><span>{{ t("ssh.socksProxy.password") }}</span
            ><el-input v-model="form.socksPassword" type="password" show-password
          /></label>
          <p class="ssh-form-inline-hint">{{ t("ssh.socksProxy.hint") }}</p>
        </div>
      </section>

      <section class="ssh-form-section">
        <div class="ssh-form-section__heading ssh-form-section__heading--toggle">
          <strong>{{ t("ssh.section.portForward") }}</strong>
          <el-button text :icon="Plus" class="ssh-form-add-rule" @click="addPortForward">
            {{ t("ssh.portForward.add") }}
          </el-button>
        </div>
        <div v-if="form.portForwards.length" class="ssh-forward-list">
          <div v-for="(rule, index) in form.portForwards" :key="rule.id" class="ssh-forward-row">
            <label class="ssh-forward-control ssh-forward-control--type">
              <span>{{ t("ssh.portForward.short") }}</span>
              <el-select v-model="rule.type">
                <el-option :label="t('ssh.portForward.local')" value="L" />
                <el-option :label="t('ssh.portForward.remote')" value="R" />
                <el-option :label="t('ssh.portForward.dynamic')" value="D" />
              </el-select>
            </label>
            <label class="ssh-forward-control ssh-forward-control--bind">
              <span>{{ t("ssh.portForward.bindPort") }}</span>
              <el-input-number v-model="rule.bindPort" :min="1" :max="65535" :controls="false" />
            </label>
            <div v-if="rule.type !== 'D'" class="ssh-forward-arrow" aria-hidden="true">
              <AppChevron direction="right" />
            </div>
            <span v-else class="ssh-forward-dynamic-hint">{{ t("ssh.portForward.dynamicHint") }}</span>
            <label v-if="rule.type !== 'D'" class="ssh-forward-control ssh-forward-control--host">
              <span>{{ t("ssh.portForward.targetHost") }}</span>
              <el-input v-model="rule.targetHost" />
            </label>
            <label v-if="rule.type !== 'D'" class="ssh-forward-control ssh-forward-control--target-port">
              <span>{{ t("ssh.portForward.targetPort") }}</span>
              <el-input-number v-model="rule.targetPort" :min="1" :max="65535" :controls="false" />
            </label>
            <label class="ssh-forward-control ssh-forward-control--label">
              <span>{{ t("ssh.portForward.label") }}</span>
              <el-input v-model="rule.label" />
            </label>
            <el-button
              text
              type="danger"
              :icon="Delete"
              :aria-label="t('ssh.portForward.remove')"
              @click="removePortForward(index)"
            />
          </div>
        </div>
        <div v-else class="ssh-forward-empty">{{ t("ssh.portForward.empty") }}</div>
      </section>
        </el-tab-pane>

        <el-tab-pane :label="t('ssh.tab.advanced')" name="advanced">
          <section class="ssh-form-section">
        <div class="ssh-form-section__heading">
          <strong>{{ t("ssh.section.stabilitySecurity") }}</strong>
        </div>
        <div class="ssh-form-grid ssh-form-grid--two">
          <label class="ssh-form-field">
            <span>{{ t("ssh.algorithmPolicy.label") }}</span>
            <el-select v-model="form.algorithmPolicy">
              <el-option value="modern" :label="t('ssh.algorithmPolicy.modern')" />
              <el-option value="legacy" :label="t('ssh.algorithmPolicy.legacy')" />
            </el-select>
          </label>
          <div class="ssh-form-field">
            <span>{{ t("ssh.keepAlive.label") }}</span>
            <el-switch v-model="form.keepAliveEnabled" />
          </div>
          <label v-if="form.keepAliveEnabled" class="ssh-form-field">
            <span>{{ t("ssh.keepAlive.interval") }}</span>
            <el-input-number v-model="form.keepAliveIntervalSeconds" :min="5" :max="300" />
          </label>
          <label v-if="form.keepAliveEnabled" class="ssh-form-field">
            <span>{{ t("ssh.keepAlive.maxMisses") }}</span>
            <el-input-number v-model="form.keepAliveMaxMisses" :min="1" :max="10" />
          </label>
          <div class="ssh-form-field">
            <span>{{ t("ssh.reconnect.label") }}</span>
            <el-switch v-model="form.reconnectEnabled" />
          </div>
          <label v-if="form.reconnectEnabled" class="ssh-form-field">
            <span>{{ t("ssh.reconnect.maxAttempts") }}</span>
            <el-input-number v-model="form.reconnectMaxAttempts" :min="1" :max="20" />
          </label>
        </div>
        <el-alert
          v-if="form.algorithmPolicy === 'legacy'"
          type="warning"
          :closable="false"
          :title="t('ssh.algorithmPolicy.legacyWarning')"
        />
      </section>
        </el-tab-pane>
      </el-tabs>

      <footer class="ssh-connection-form__footer">
        <el-button @click="emit('update:modelValue', false)">{{ t("common.cancel") }}</el-button>
        <div>
          <el-button :loading="testing" @click="testConnection">{{
            t("ssh.test.button")
          }}</el-button>
          <el-button type="primary" native-type="submit">{{
            t(connectAfterSave ? "ssh.saveAndConnect" : "ssh.addOnly")
          }}</el-button>
        </div>
      </footer>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.ssh-connection-options {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 18px;
}

.ssh-favorite-toggle :deep(.el-checkbox__label) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--app-text);
  font-size: 12px;
}

.ssh-favorite-toggle .el-icon {
  color: var(--app-text-muted);
}

.ssh-favorite-toggle .el-icon.is-favorite {
  color: var(--app-warning);
}
</style>
