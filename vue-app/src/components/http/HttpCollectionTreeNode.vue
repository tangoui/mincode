<script setup lang="ts">
import { Delete, Document, EditPen, FolderOpened, MoreFilled, Plus, Upload, VideoPlay } from "@element-plus/icons-vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import LinkedNotesBadge from "@/components/notes/LinkedNotesBadge.vue";
import { claimContextMenuKey } from "@/components/ui/app-context-menu";
import type { HttpRequestCollection, SavedHttpRequest } from "@/domain/http/request-collection";
import { flattenCollectionRequests } from "@/domain/http/request-collection";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  collection: HttpRequestCollection;
  depth?: number;
  expandedIds: Set<string>;
  renamingCollectionId: string;
  renamingRequestId: string;
  renameValue: string;
}>();
const emit = defineEmits<{
  toggle: [collectionId: string];
  "update:renameValue": [value: string];
  "rename-collection": [collection: HttpRequestCollection];
  "rename-request": [request: SavedHttpRequest];
  "save-rename": [];
  "cancel-rename": [];
  "add-request": [collection: HttpRequestCollection];
  "add-child": [collection: HttpRequestCollection];
  run: [collection: HttpRequestCollection];
  command: [collection: HttpRequestCollection, command: string];
  open: [request: SavedHttpRequest, collection: HttpRequestCollection];
  "delete-request": [collection: HttpRequestCollection, requestId: string];
  /** Right-click, ContextMenu key or Shift+F10 on a saved request row (the view owns the menu). */
  "request-contextmenu": [event: MouseEvent | KeyboardEvent, request: SavedHttpRequest, collection: HttpRequestCollection];
  drop: [event: DragEvent, targetCollection: HttpRequestCollection];
}>();

const { t } = useVueI18n();
const depth = props.depth ?? 0;

function requestContextMenu(event: MouseEvent | KeyboardEvent, request: SavedHttpRequest) {
  // The inline rename input keeps its own text menu and keys.
  if (props.renamingRequestId === request.id) return;
  if (event instanceof KeyboardEvent) {
    if (!claimContextMenuKey(event)) return;
  } else {
    event.preventDefault();
  }
  emit("request-contextmenu", event, request, props.collection);
}

function forwardRequestContextMenu(event: MouseEvent | KeyboardEvent, request: SavedHttpRequest, node: HttpRequestCollection) {
  emit("request-contextmenu", event, request, node);
}

function dragCollection(event: DragEvent) {
  event.dataTransfer?.setData("application/x-http-collection", props.collection.id);
  if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
}

function dragRequest(event: DragEvent, request: SavedHttpRequest) {
  event.dataTransfer?.setData("application/x-http-request", request.id);
  if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
}

function forwardCommand(node: HttpRequestCollection, command: string) {
  emit("command", node, command);
}

function forwardOpen(request: SavedHttpRequest, node: HttpRequestCollection) {
  emit("open", request, node);
}

function forwardDeleteRequest(node: HttpRequestCollection, requestId: string) {
  emit("delete-request", node, requestId);
}

function forwardDrop(event: DragEvent, node: HttpRequestCollection) {
  emit("drop", event, node);
}
</script>

<template>
  <section class="http-collection" :style="{ '--http-tree-depth': depth }">
    <header
      draggable="true"
      @dragstart="dragCollection"
      @dragover.prevent
      @drop.stop="emit('drop', $event, collection)"
    >
      <el-button text native-type="button" class="http-collection__toggle" @click="emit('toggle', collection.id)">
        <AppChevron :direction="expandedIds.has(collection.id) ? 'down' : 'right'" />
      </el-button>
      <el-icon class="http-collection__folder"><FolderOpened /></el-icon>
      <el-input
        v-if="renamingCollectionId === collection.id"
        :model-value="renameValue"
        class="app-inline-rename http-collection__rename"
        :data-http-collection-rename="collection.id"
        :aria-label="t('http.renameCollection')"
        maxlength="80"
        @update:model-value="emit('update:renameValue', String($event))"
        @keydown.enter.stop.prevent="emit('save-rename')"
        @keydown.esc.stop.prevent="emit('cancel-rename')"
        @blur="emit('save-rename')"
      />
      <strong v-else>{{ collection.name }}</strong>
      <el-button text native-type="button" :aria-label="t('http.renameCollection')" :title="t('http.renameCollection')" @click="emit('rename-collection', collection)">
        <el-icon><EditPen /></el-icon>
      </el-button>
      <el-dropdown
        trigger="click"
        @command="$event === 'request' ? emit('add-request', collection) : emit('add-child', collection)"
      >
        <el-button text native-type="button" :aria-label="t('http.createInCollection')" :title="t('http.createInCollection')">
          <el-icon><Plus /></el-icon>
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="request">{{ t('http.newRequest') }}</el-dropdown-item>
            <el-dropdown-item command="collection">{{ t('http.newSubcollection') }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button
        text
        native-type="button"
        :aria-label="t('http.runCollection')"
        :title="t('http.runCollection')"
        :disabled="flattenCollectionRequests(collection).length === 0"
        @click="emit('run', collection)"
      >
        <el-icon><VideoPlay /></el-icon>
      </el-button>
      <el-dropdown trigger="click" @command="emit('command', collection, $event)">
        <el-button text native-type="button" :aria-label="t('http.collectionActions')" :title="t('http.collectionActions')"><el-icon><MoreFilled /></el-icon></el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="manage-environments">{{ t('http.manageCollectionEnvironments') }}</el-dropdown-item>
            <el-dropdown-item command="import" :icon="Upload">{{ t('http.importToCollection') }}</el-dropdown-item>
            <el-dropdown-item command="openapi-json">{{ t('http.exportOpenApiJson') }}</el-dropdown-item>
            <el-dropdown-item command="openapi-yaml">{{ t('http.exportOpenApiYaml') }}</el-dropdown-item>
            <el-dropdown-item command="postman">{{ t('http.exportPostman') }}</el-dropdown-item>
            <el-dropdown-item divided command="delete" :icon="Delete">{{ t('common.delete') }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </header>

    <template v-if="expandedIds.has(collection.id)">
      <HttpCollectionTreeNode
        v-for="child in collection.children ?? []"
        :key="child.id"
        :collection="child"
        :depth="depth + 1"
        :expanded-ids="expandedIds"
        :renaming-collection-id="renamingCollectionId"
        :renaming-request-id="renamingRequestId"
        :rename-value="renameValue"
        @toggle="emit('toggle', $event)"
        @update:rename-value="emit('update:renameValue', $event)"
        @rename-collection="emit('rename-collection', $event)"
        @rename-request="emit('rename-request', $event)"
        @save-rename="emit('save-rename')"
        @cancel-rename="emit('cancel-rename')"
        @add-request="emit('add-request', $event)"
        @add-child="emit('add-child', $event)"
        @run="emit('run', $event)"
        @command="forwardCommand"
        @open="forwardOpen"
        @delete-request="forwardDeleteRequest"
        @request-contextmenu="forwardRequestContextMenu"
        @drop="forwardDrop"
      />
      <div
        v-for="request in collection.requests"
        :key="request.id"
        class="http-saved-request"
        draggable="true"
        @dragstart="dragRequest($event, request)"
        @contextmenu="requestContextMenu($event, request)"
        @keydown="requestContextMenu($event, request)"
      >
        <div v-if="renamingRequestId === request.id" class="http-saved-request__rename">
          <span class="http-method">{{ request.method }}</span>
          <el-input
            :model-value="renameValue"
            class="app-inline-rename"
            :data-http-request-rename="request.id"
            :aria-label="t('http.renameRequest')"
            maxlength="80"
            @update:model-value="emit('update:renameValue', String($event))"
            @keydown.enter.stop.prevent="emit('save-rename')"
            @keydown.esc.stop.prevent="emit('cancel-rename')"
            @blur="emit('save-rename')"
          />
        </div>
        <el-button v-else text native-type="button" class="http-saved-request__open" aria-keyshortcuts="ContextMenu Shift+F10" @click="emit('open', request, collection)">
          <span class="http-method">{{ request.method }}</span><span>{{ request.name }}</span>
          <el-icon v-if="request.documentation"><Document /></el-icon>
        </el-button>
        <LinkedNotesBadge
          v-if="renamingRequestId !== request.id"
          type="http"
          :id="request.id"
          :name="request.name"
          :parent-id="collection.id"
        />
        <el-button text native-type="button" class="http-saved-request__action" :aria-label="t('http.renameRequest')" :title="t('http.renameRequest')" @click="emit('rename-request', request)">
          <el-icon><EditPen /></el-icon>
        </el-button>
        <el-button text native-type="button" class="http-saved-request__action" :aria-label="t('common.delete')" :title="t('common.delete')" @click="emit('delete-request', collection, request.id)">
          <el-icon><Delete /></el-icon>
        </el-button>
      </div>
    </template>
  </section>
</template>
