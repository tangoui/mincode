<script setup lang="ts">
import { ChatDotSquare, CircleCheck, Delete, Download, EditPen, FolderOpened, Message, Notebook, Share, Top, WarningFilled } from "@element-plus/icons-vue";
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from "vue";
import {
  moveWorkspaceGroupKey,
  resolveWorkspaceGroupDropTarget,
  type WorkspaceGroupDropPosition,
} from "@/domain/ai/workspace-group-order";
import AppChevron from "@/components/ui/AppChevron.vue";
import LinkedNotesBadge from "@/components/notes/LinkedNotesBadge.vue";
import { useVueI18n } from "@/i18n/messages";
import { AI_CONVERSATION_TITLE_MAX_LENGTH, normalizeAiConversationTitle } from "@/domain/ai/conversation-title";
import type { AiConversation, AiConversationStatus } from "@/stores/ai-assistant";
import type { AiMessageSearchMatch } from "@/services/ai-conversation-persistence";
import { createConversationTree } from "@/domain/ai/conversation-tree";

type Scope = "active" | "archived";
type Sort = "updated" | "created";
interface StatusOption { value: "all" | AiConversationStatus; label: string }
interface Labels {
  title: string; create: string; active: string; archived: string; status: string; sort: string;
  updated: string; created: string; updatedAt: string; empty: string; actions: string; archive: string; unarchive: string;
    pin: string; unpin: string; pinned: string; rename: string; renamePlaceholder: string;
  remove: string; clearMessages: string; untitled: string; exportConversation: string; messageMatches: string;
  confirm: string; cancel: string; backgroundRunning: string;
        unreadCompleted: string; unread: string; markRead: string; markUnread: string; workspace: string; search: string; workspaceUnavailable: string; switchWorkspace: string; relinkWorkspace: string; reorderWorkspace: string;
  statusLabels: Partial<Record<AiConversationStatus, string>>;
}

const props = defineProps<{
  groups: Array<{ key: string; label: string; detail?: string; title?: string; tone?: number; conversations: AiConversation[] }>;
  activeConversationId: string;
  scope: Scope;
  status: "all" | AiConversationStatus;
  sort: Sort;
  statusOptions: StatusOption[];
  labels: Labels;
  /** Saved messages matching the search box, shown below the conversation list. */
  messageMatches?: AiMessageSearchMatch[];
  globalMode: boolean;
  workspaceFilter: string;
  search: string;
  workspaceOptions: Array<{ value: string; label: string }>;
  workspaceGroupOrderable: boolean;
  topWorkspaceId: string;
  workspaceName: (conversation: AiConversation) => string;
  workspaceAvailable: (conversation: AiConversation) => boolean;
}>();
const { intlLocale, t } = useVueI18n();
const collapsedParents = reactive(new Set<string>());
const groupRows = computed(() => new Map(props.groups.map((group) => [
  group.key,
  createConversationTree(group.conversations).rows(collapsedParents),
])));
function toggleChildren(id: string) {
  if (!collapsedParents.delete(id)) collapsedParents.add(id);
}
watch(() => props.activeConversationId, (id) => {
  const conversations = new Map(props.groups.flatMap((group) => group.conversations.map((item) => [item.id, item] as const)));
  const visited = new Set([id]);
  let parentId = conversations.get(id)?.delegation?.parentId;
  while (parentId && !visited.has(parentId)) {
    visited.add(parentId);
    collapsedParents.delete(parentId);
    parentId = conversations.get(parentId)?.delegation?.parentId;
  }
});

const emit = defineEmits<{
  "update:scope": [value: Scope]; "update:status": [value: "all" | AiConversationStatus];
  "update:sort": [value: Sort]; "update:workspaceFilter": [value: string]; "update:search": [value: string]; select: [id: string]; archive: [conversation: AiConversation];
            pin: [conversation: AiConversation]; rename: [conversation: AiConversation, title: string];
  readState: [conversation: AiConversation, unread: boolean];
  remove: [conversation: AiConversation]; clearMessages: [conversation: AiConversation];
  export: [conversation: AiConversation]; "open-message": [match: AiMessageSearchMatch];
  switchWorkspace: [conversation: AiConversation]; relinkWorkspace: [conversation: AiConversation];
  reorderWorkspaceGroups: [keys: string[]];
  notes: [conversation: AiConversation];
}>();

/** 关联笔记记录的工作区名称快照：工作区不可用时不用「不可用」提示文案。 */
function noteScopeName(conversation: AiConversation) {
  return props.workspaceAvailable(conversation) ? props.workspaceName(conversation) : conversation.workspaceName ?? "";
}

const sidebarRoot = ref<HTMLElement>();
const editingConversationId = ref("");
const editingTitle = ref("");
const collapsedGroups = ref<Record<string, boolean>>({});
const draggingGroupKey = ref("");
const groupDropTarget = ref<{ key: string; position: WorkspaceGroupDropPosition }>();
const GROUP_DRAG_THRESHOLD_PX = 4;
let suppressToggleClick = false;
let pendingGroupDrag: {
  key: string;
  pointerId: number;
  startX: number;
  startY: number;
  handle: HTMLElement;
} | undefined;

function isGroupCollapsed(key: string) {
  return collapsedGroups.value[key] === true;
}

function toggleGroup(key: string) {
  // 分组标题同时是拖拽区：拖完松手时浏览器仍会派发 click，不能顺带折叠。
  if (suppressToggleClick) {
    suppressToggleClick = false;
    return;
  }
  collapsedGroups.value[key] = !isGroupCollapsed(key);
}

function groupHeaderCandidates() {
  return Array.from(sidebarRoot.value?.querySelectorAll<HTMLElement>("[data-workspace-group-key]") ?? [])
    .map((element) => {
      const key = element.dataset.workspaceGroupKey ?? "";
      const header = element.querySelector<HTMLElement>(".ai-conversation-group__header");
      const rect = header?.getBoundingClientRect();
      return rect ? { key, top: rect.top, bottom: rect.bottom } : undefined;
    })
    .filter((candidate): candidate is { key: string; top: number; bottom: number } => Boolean(candidate));
}

function updateGroupDropTarget(clientY: number) {
  groupDropTarget.value = resolveWorkspaceGroupDropTarget(
    groupHeaderCandidates(),
    draggingGroupKey.value,
    clientY,
  );
}

function finishGroupDrag(commit: boolean) {
  const sourceKey = draggingGroupKey.value;
  const target = groupDropTarget.value;
  const handle = pendingGroupDrag?.handle;
  const pointerId = pendingGroupDrag?.pointerId;
  pendingGroupDrag = undefined;
  if (handle && pointerId !== undefined && handle.hasPointerCapture(pointerId)) {
    handle.releasePointerCapture(pointerId);
  }
  draggingGroupKey.value = "";
  groupDropTarget.value = undefined;
  if (sourceKey) {
    suppressToggleClick = true;
    // 指针被取消时不会再有 click，下一轮事件循环前清掉，免得吞掉之后的正常点击。
    setTimeout(() => { suppressToggleClick = false; }, 0);
  }
  if (!commit || !sourceKey || !target || sourceKey === target.key) return;
  emit("reorderWorkspaceGroups", moveWorkspaceGroupKey(
    props.groups.map((group) => group.key),
    sourceKey,
    target.key,
    target.position,
  ));
}

function startGroupPointerDrag(event: PointerEvent, key: string) {
  if (!props.workspaceGroupOrderable || event.button !== 0) return;
  const handle = event.currentTarget as HTMLElement;
  pendingGroupDrag = {
    key,
    pointerId: event.pointerId,
    startX: event.clientX,
    startY: event.clientY,
    handle,
  };
  handle.setPointerCapture(event.pointerId);
  event.preventDefault();
}

function moveGroupPointerDrag(event: PointerEvent) {
  const pending = pendingGroupDrag;
  if (!pending || pending.pointerId !== event.pointerId) return;
  if (!draggingGroupKey.value) {
    const distance = Math.hypot(event.clientX - pending.startX, event.clientY - pending.startY);
    if (distance < GROUP_DRAG_THRESHOLD_PX) return;
    draggingGroupKey.value = pending.key;
  }
  updateGroupDropTarget(event.clientY);
  event.preventDefault();
}

function endGroupPointerDrag(event: PointerEvent) {
  if (pendingGroupDrag?.pointerId !== event.pointerId) return;
  finishGroupDrag(Boolean(draggingGroupKey.value));
}

function cancelGroupPointerDrag(event: PointerEvent) {
  if (pendingGroupDrag?.pointerId !== event.pointerId) return;
  finishGroupDrag(false);
}

onBeforeUnmount(() => finishGroupDrag(false));

async function startRenaming(conversation: AiConversation) {
  editingConversationId.value = conversation.id;
  editingTitle.value = conversation.title;
  await nextTick();
  const input = document.querySelector<HTMLInputElement>(
    `[data-conversation-rename-input="${conversation.id}"]`,
  );
  input?.focus();
  input?.select();
}

function cancelRenaming() {
  editingConversationId.value = "";
  editingTitle.value = "";
}

function submitRename(conversation: AiConversation) {
  if (editingConversationId.value !== conversation.id) return;
  const title = normalizeAiConversationTitle(editingTitle.value);
  if (title && title !== conversation.title) emit("rename", conversation, title);
  cancelRenaming();
}

function isImeComposing(event: KeyboardEvent) {
  return event.isComposing || event.keyCode === 229;
}

function handleRenameEnter(event: KeyboardEvent, conversation: AiConversation) {
  if (isImeComposing(event)) return;
  event.preventDefault();
  submitRename(conversation);
}

function handleRenameEscape(event: KeyboardEvent) {
  if (isImeComposing(event)) return;
  event.preventDefault();
  cancelRenaming();
}


function conversationUpdatedAtTitle(timestamp: number, label: string) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return label;
  const language = intlLocale.value;
  return `${label}: ${date.toLocaleString(language)}`;
}

// The row shows only the title and status; workspace, background work and update time live in the hover text.
function conversationSummary(conversation: AiConversation) {
  const lines = [conversation.title || props.labels.untitled];
  if (conversation.delegation) lines.push(t("ai.delegation.child"));
  if (props.globalMode) {
    lines.push(props.workspaceAvailable(conversation)
      ? [props.workspaceName(conversation), conversation.projectRoot].filter(Boolean).join(" · ")
      : props.labels.workspaceUnavailable);
  }
  if (conversation.running) lines.push(props.labels.backgroundRunning);
  lines.push(conversationUpdatedAtTitle(conversation.updatedAt, props.labels.updatedAt));
  return lines.join("\n");
}

function statusTagType(status: AiConversationStatus) {
  const types = {
    not_started: "info",
    in_progress: "primary",
    needs_input: "warning",
    completed: "success",
    failed: "danger",
    interrupted: "warning",
  } as const;
  return types[status];
}

const deletePopperOptions = {
  strategy: "fixed" as const,
  modifiers: [
    { name: "flip", enabled: false },
    {
      name: "preventOverflow",
      options: {
        boundary: "viewport",
        rootBoundary: "viewport",
        mainAxis: true,
        altAxis: true,
        tether: false,
        padding: 8,
      },
    },
  ],
};
</script>

<template>
  <aside ref="sidebarRoot" class="ai-conversation-sidebar" :aria-label="labels.title">
    <div class="ai-conversation-sidebar__filters">
      <!-- 新建会话用顶部“AI 助手 / 本地 Agent”旁的 +：列表收起时它仍可用。 -->
      <el-tabs class="ai-conversation-sidebar__scope-tabs" :model-value="scope" stretch @update:model-value="emit('update:scope', $event as Scope)">
        <el-tab-pane :label="labels.active" name="active" />
        <el-tab-pane :label="labels.archived" name="archived" />
      </el-tabs>
      <div v-if="globalMode" class="ai-conversation-sidebar__filter-row">
        <el-input
          class="ai-conversation-sidebar__search"
          :model-value="search"
          size="small"
          clearable
          :placeholder="labels.search"
          :aria-label="labels.search"
          @update:model-value="emit('update:search', $event)"
        />
        <el-select
          class="ai-conversation-sidebar__workspace-filter"
          :model-value="workspaceFilter"
          size="small"
          :aria-label="labels.workspace"
          @update:model-value="emit('update:workspaceFilter', $event)"
        >
          <el-option
            v-for="option in workspaceOptions"
            :key="option.value"
            :label="option.label"
            :value="option.value"
          />
        </el-select>
      </div>
      <div class="ai-conversation-sidebar__filter-row">
        <el-select :model-value="status" size="small" :aria-label="labels.status" @update:model-value="emit('update:status', $event)">
          <el-option v-for="option in statusOptions" :key="option.value" :label="option.label" :value="option.value" />
        </el-select>
        <el-select :model-value="sort" size="small" :aria-label="labels.sort" @update:model-value="emit('update:sort', $event as Sort)">
          <el-option :label="labels.updated" value="updated" />
          <el-option :label="labels.created" value="created" />
        </el-select>
      </div>
    </div>
    <el-scrollbar class="ai-conversation-sidebar__list">
      <el-empty v-if="!groups.length && !messageMatches?.length" :description="labels.empty" :image-size="56" />
      <section
        v-for="group in groups"
        :key="group.key"
        class="ai-conversation-group"
        :data-workspace-group-key="group.key"
        :class="[
          {
            'is-collapsed': isGroupCollapsed(group.key),
            'is-dragging': draggingGroupKey === group.key,
            'is-drop-before': groupDropTarget?.key === group.key && groupDropTarget.position === 'before',
            'is-drop-after': groupDropTarget?.key === group.key && groupDropTarget.position === 'after',
          },
          group.tone !== undefined ? `is-tone-${group.tone}` : '',
        ]"
      >
        <div class="ai-conversation-group__header">
        <button
          type="button"
          class="ai-conversation-group__toggle"
          :class="{ 'is-orderable': workspaceGroupOrderable }"
          :aria-expanded="!isGroupCollapsed(group.key)"
          :title="workspaceGroupOrderable ? `${group.title || group.label}\n${labels.reorderWorkspace}` : group.title || group.label"
          @click="toggleGroup(group.key)"
          @pointerdown="startGroupPointerDrag($event, group.key)"
          @pointermove="moveGroupPointerDrag"
          @pointerup="endGroupPointerDrag"
          @pointercancel="cancelGroupPointerDrag"
          @lostpointercapture="cancelGroupPointerDrag"
        >
          <span class="ai-conversation-group__icon" aria-hidden="true">
            <el-icon><FolderOpened /></el-icon>
          </span>
          <span class="ai-conversation-group__label">{{ group.label }}</span>
          <span v-if="group.detail" class="ai-conversation-group__detail">{{ group.detail }}</span>
          <span class="ai-conversation-group__count">{{ group.conversations.length }}</span>
          <AppChevron :direction="isGroupCollapsed(group.key) ? 'right' : 'down'" />
        </button>
        </div>
        <template v-if="!isGroupCollapsed(group.key)">
          <el-card
            v-for="{ item: conversation, depth, children } in groupRows.get(group.key)"
            :key="conversation.id"
            class="ai-conversation-item"
            :class="{
              'is-active': conversation.id === activeConversationId,
              'is-unread': conversation.unread,
              'is-renaming': editingConversationId === conversation.id,
              'is-child': Boolean(depth),
            }"
            :style="depth ? { '--ai-conversation-depth': Math.min(depth, 3) } : undefined"
            shadow="never"
            @click="emit('select', conversation.id)"
          >
            <button
              v-if="children"
              type="button"
              class="ai-conversation-item__children-toggle"
              :aria-label="t('ai.delegation.childCount', { count: children })"
              :title="t('ai.delegation.childCount', { count: children })"
              :aria-expanded="!collapsedParents.has(conversation.id)"
              @click.stop="toggleChildren(conversation.id)"
            >
              <AppChevron :direction="collapsedParents.has(conversation.id) ? 'right' : 'down'" />
            </button>
            <button
              type="button"
              class="ai-conversation-item__content"
              :data-conversation-id="conversation.id"
              :title="conversationSummary(conversation)"
            >
              <span class="ai-conversation-item__title" @dblclick.stop="startRenaming(conversation)">
                <el-icon v-if="conversation.delegation" :title="t('ai.delegation.child')" :aria-label="t('ai.delegation.child')">
                  <Share />
                </el-icon>
                <el-icon v-if="conversation.pinned" class="ai-conversation-item__pin" :title="labels.pinned" aria-hidden="true">
                  <Top />
                </el-icon>
                <input
                  v-if="editingConversationId === conversation.id"
                  v-model="editingTitle"
                  type="text"
                  class="ai-conversation-item__rename-input"
                  :data-conversation-rename-input="conversation.id"
                  :maxlength="AI_CONVERSATION_TITLE_MAX_LENGTH"
                  :placeholder="labels.renamePlaceholder"
                  :aria-label="labels.rename"
                  @click.stop
                  @dblclick.stop
                  @keydown.enter="handleRenameEnter($event, conversation)"
                  @keydown.esc="handleRenameEscape"
                  @blur="submitRename(conversation)"
                />
                <strong v-else>{{ conversation.title || labels.untitled }}</strong>
              </span>
            </button>
            <LinkedNotesBadge
              type="ai"
              :id="conversation.id"
              :name="conversation.title"
              :scope-id="conversation.workspaceScopeId"
              :scope-name="noteScopeName(conversation)"
            />
            <!-- A conversation whose workspace is gone keeps a visible way to relink it; available ones switch on click. -->
            <el-button
              v-if="globalMode && !workspaceAvailable(conversation)"
              text
              :icon="WarningFilled"
              class="ai-conversation-item__workspace is-unavailable"
              :title="labels.relinkWorkspace"
              :aria-label="labels.relinkWorkspace"
              @click.stop="emit('relinkWorkspace', conversation)"
            />
            <span class="ai-conversation-item__meta">
              <el-tag
                :type="conversation.unread ? 'warning' : statusTagType(conversation.status)"
                effect="light"
                size="small"
                :class="{ 'ai-conversation-item__unread-status': conversation.unread }"
              >
                <span v-if="conversation.unread" class="ai-conversation-item__unread-dot" aria-hidden="true" />
                {{ conversation.unread
                  ? (conversation.status === "completed" ? labels.unreadCompleted : labels.unread)
                  : labels.statusLabels[conversation.status] || conversation.status }}
              </el-tag>
            </span>
            <div class="ai-conversation-item__actions" @click.stop>
              <el-button
                text
                :icon="conversation.unread ? CircleCheck : Message"
                class="ai-conversation-item__action-button ai-conversation-item__read-state-action"
                :class="{ 'is-unread': conversation.unread }"
                :title="conversation.unread ? labels.markRead : labels.markUnread"
                :aria-label="conversation.unread ? labels.markRead : labels.markUnread"
                @click.stop="emit('readState', conversation, !conversation.unread)"
              />
              <el-button
                text
                :icon="EditPen"
                class="ai-conversation-item__action-button"
                :title="labels.rename"
                :aria-label="labels.rename"
                @click="startRenaming(conversation)"
              />
              <el-button
                text
                :icon="Top"
                class="ai-conversation-item__action-button ai-conversation-item__pin-action"
                :class="{ 'is-active': conversation.pinned }"
                :title="conversation.pinned ? labels.unpin : labels.pin"
                :aria-label="conversation.pinned ? labels.unpin : labels.pin"
                @click="emit('pin', conversation)"
              />
              <el-button
                text
                :icon="FolderOpened"
                class="ai-conversation-item__action-button"
                :title="conversation.archived ? labels.unarchive : labels.archive"
                :aria-label="conversation.archived ? labels.unarchive : labels.archive"
                @click="emit('archive', conversation)"
              />
              <el-button
                text
                :icon="Notebook"
                class="ai-conversation-item__action-button"
                :title="t('notes.link.notesButton')"
                :aria-label="t('notes.link.notesButton')"
                @click="emit('notes', conversation)"
              />
              <el-button
                text
                :icon="Download"
                class="ai-conversation-item__action-button"
                :title="labels.exportConversation"
                :aria-label="labels.exportConversation"
                @click="emit('export', conversation)"
              />
              <el-popconfirm
                :title="labels.clearMessages"
                :confirm-button-text="labels.confirm"
                :cancel-button-text="labels.cancel"
                placement="left"
                :teleported="true"
                :popper-options="deletePopperOptions"
                @confirm="emit('clearMessages', conversation)"
              >
                <template #reference>
                  <el-button
                    text
                    :icon="ChatDotSquare"
                    class="ai-conversation-item__action-button"
                    :disabled="conversation.running || (conversation.detailsLoaded !== false && !conversation.messages.length)"
                    :title="labels.clearMessages"
                    :aria-label="labels.clearMessages"
                  />
                </template>
              </el-popconfirm>
              <el-button
                text
                :icon="Delete"
                class="ai-conversation-item__action-button"
                :title="labels.remove"
                :aria-label="labels.remove"
                @click="emit('remove', conversation)"
              />
            </div>
          </el-card>
        </template>
      </section>
      <section v-if="messageMatches?.length" class="ai-conversation-message-matches" :aria-label="labels.messageMatches">
        <h3>{{ labels.messageMatches }}</h3>
        <button
          v-for="match in messageMatches"
          :key="`${match.scopeId}:${match.messageId}`"
          type="button"
          class="ai-conversation-message-match"
          @click="emit('open-message', match)"
        >
          <strong>{{ match.title || labels.untitled }}</strong>
          <span>{{ match.snippet }}</span>
        </button>
      </section>
    </el-scrollbar>
  </aside>
</template>
