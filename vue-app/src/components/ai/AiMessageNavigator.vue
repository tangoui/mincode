<script setup lang="ts">
import { nextTick, ref } from "vue";

interface AiMessageNavigatorItem {
  id: string;
  title: string;
  preview: string;
  ariaLabel: string;
}

const props = defineProps<{
  items: AiMessageNavigatorItem[];
  activeId?: string;
  label: string;
  pendingLabel: string;
}>();

const emit = defineEmits<{
  select: [id: string];
}>();

const popover = ref<{ hide: () => void }>();
const list = ref<HTMLElement>();

function revealActive() {
  void nextTick(() => {
    const active = list.value?.querySelector<HTMLElement>(".is-active");
    active?.scrollIntoView({ block: "center" });
  });
}

function selectFromList(id: string) {
  emit("select", id);
  popover.value?.hide();
}

function itemTooltip(item: AiMessageNavigatorItem) {
  return `${item.title}\n\n${item.preview || props.pendingLabel}`;
}
</script>

<template>
  <nav v-if="items.length" class="ai-message-navigator" :aria-label="label">
    <el-popover
      ref="popover"
      placement="right"
      trigger="hover"
      :show-after="120"
      :hide-after="160"
      :offset="8"
      :show-arrow="false"
      :width="300"
      popper-class="ai-message-navigator-popover"
      @show="revealActive"
    >
      <template #reference>
        <div class="ai-message-navigator__rail">
          <el-scrollbar class="ai-message-navigator__scroll">
            <el-timeline class="ai-message-navigator__timeline">
              <el-timeline-item
                v-for="item in items"
                :key="item.id"
                center
                hide-timestamp
                :class="{ 'is-active': item.id === activeId }"
              >
                <template #dot>
                  <el-button
                    text
                    class="ai-message-navigator__item"
                    :aria-label="item.ariaLabel"
                    :aria-current="item.id === activeId ? 'location' : undefined"
                    @click="emit('select', item.id)"
                  >
                    <span class="ai-message-navigator__tick" aria-hidden="true" />
                  </el-button>
                </template>
              </el-timeline-item>
            </el-timeline>
          </el-scrollbar>
        </div>
      </template>
      <el-scrollbar max-height="min(70vh, 560px)">
        <ul ref="list" class="ai-message-navigator-list" :aria-label="label">
          <li v-for="(item, index) in items" :key="item.id">
            <button
              type="button"
              class="ai-message-navigator-list__item"
              :class="{ 'is-active': item.id === activeId }"
              :title="itemTooltip(item)"
              :aria-label="item.ariaLabel"
              :aria-current="item.id === activeId ? 'location' : undefined"
              @click="selectFromList(item.id)"
            >
              <span class="ai-message-navigator-list__index">{{ index + 1 }}</span>
              <span class="ai-message-navigator-list__title">{{ item.title }}</span>
            </button>
          </li>
        </ul>
      </el-scrollbar>
    </el-popover>
  </nav>
</template>
