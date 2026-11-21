<script setup lang="ts">
import { computed } from "vue";
import { assignGitGraphLanes } from "@/domain/git/graph";
import type { GitLogEntry } from "@/services/git-service";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  entries: GitLogEntry[];
  selectedHash?: string;
}>();
const emit = defineEmits<{ select: [entry: GitLogEntry] }>();
const { t, intlLocale } = useVueI18n();
const ROW_HEIGHT = 30;
const LANE_WIDTH = 17;
const DOT_RADIUS = 4;
const laneColors = [
  "var(--app-accent)",
  "var(--app-success)",
  "var(--app-warning)",
  "var(--app-danger)",
  "color-mix(in srgb, var(--app-info) 72%, var(--app-success))",
  "color-mix(in srgb, var(--app-warning) 72%, var(--app-text-regular))",
];
const lanes = computed(() => assignGitGraphLanes(props.entries));
const laneMap = computed(() => new Map(lanes.value.map((item) => [item.hash, item])));
// 历史里并行分支可能很多，泳道列若无限变宽会把提交信息挤出抽屉。
// 只渲染前若干条泳道，更远的泳道折叠到最后一列，保证文字区域始终可见。
const MAX_VISIBLE_LANES = 8;
// 列宽按实际用到的泳道数计算：只有一条主线时不再空出 8 条泳道的宽度。
const usedLanes = computed(() =>
  Math.min(MAX_VISIBLE_LANES, lanes.value.reduce((max, item) => Math.max(max, item.lane + 1), 1))
);
const graphWidth = computed(() => usedLanes.value * LANE_WIDTH + 8);
function laneX(lane: number) {
  return Math.min(lane, MAX_VISIBLE_LANES - 1) * LANE_WIDTH + LANE_WIDTH / 2;
}
const graphHeight = computed(() => props.entries.length * ROW_HEIGHT);

function colorForLane(lane: number) {
  return laneColors[lane % laneColors.length];
}

// 一个月内显示"3 小时前"这类相对时间，更早的显示日期；完整时间放在悬停提示里。
function relativeTime(timestamp: number) {
  const seconds = Math.max(0, Math.floor(Date.now() / 1000) - timestamp);
  const format = new Intl.RelativeTimeFormat(intlLocale.value, { numeric: "auto" });
  if (seconds < 60) return format.format(0, "second");
  if (seconds < 3600) return format.format(-Math.floor(seconds / 60), "minute");
  if (seconds < 86400) return format.format(-Math.floor(seconds / 3600), "hour");
  if (seconds < 2592000) return format.format(-Math.floor(seconds / 86400), "day");
  return new Date(timestamp * 1000).toLocaleDateString(intlLocale.value);
}

function fullTime(timestamp: number) {
  return new Date(timestamp * 1000).toLocaleString(intlLocale.value);
}
</script>

<template>
  <el-scrollbar v-if="entries.length" class="git-graph">
    <div
      class="git-graph__canvas"
      :style="{ minHeight: `${graphHeight}px`, '--git-graph-width': `${graphWidth}px` }"
    >
      <svg :width="graphWidth" :height="graphHeight" aria-hidden="true">
        <g v-for="node in lanes" :key="node.hash">
          <template v-for="parentHash in node.parents" :key="parentHash">
            <line
              v-if="laneMap.get(parentHash)?.lane === node.lane"
              :x1="laneX(node.lane)"
              :y1="node.row * ROW_HEIGHT + ROW_HEIGHT / 2 + DOT_RADIUS"
              :x2="laneX(node.lane)"
              :y2="
                (laneMap.get(parentHash)?.row ?? node.row) * ROW_HEIGHT +
                ROW_HEIGHT / 2 -
                DOT_RADIUS
              "
              :stroke="colorForLane(node.lane)"
              stroke-width="1.5"
            />
            <path
              v-else-if="laneMap.get(parentHash)"
              :d="`M ${laneX(node.lane)} ${node.row * ROW_HEIGHT + ROW_HEIGHT / 2 + DOT_RADIUS} C ${laneX(node.lane)} ${((node.row + (laneMap.get(parentHash)?.row ?? node.row)) * ROW_HEIGHT) / 2 + ROW_HEIGHT / 2} ${laneX(laneMap.get(parentHash)?.lane ?? node.lane)} ${((node.row + (laneMap.get(parentHash)?.row ?? node.row)) * ROW_HEIGHT) / 2 + ROW_HEIGHT / 2} ${laneX(laneMap.get(parentHash)?.lane ?? node.lane)} ${(laneMap.get(parentHash)?.row ?? node.row) * ROW_HEIGHT + ROW_HEIGHT / 2 - DOT_RADIUS}`"
              fill="none"
              :stroke="colorForLane(node.lane)"
              stroke-width="1.5"
            />
          </template>
          <circle
            :cx="laneX(node.lane)"
            :cy="node.row * ROW_HEIGHT + ROW_HEIGHT / 2"
            :r="DOT_RADIUS"
            :fill="colorForLane(node.lane)"
          />
        </g>
      </svg>
      <div class="git-graph__commits">
        <button
          v-for="entry in entries"
          :key="entry.hash"
          type="button"
          :class="{ 'is-active': selectedHash === entry.hash }"
          @click="emit('select', entry)"
        >
          <code>{{ entry.short_hash }}</code>
          <!-- 分支/标签标记放进摘要格里：作为独立网格项会把这一行后面的列整体挤错位。 -->
          <span class="git-graph__subject">
            <span v-for="refName in entry.refs ?? []" :key="refName" class="git-graph__ref">{{
              refName
            }}</span>
            <strong :title="entry.message">{{ entry.message }}</strong>
          </span>
          <small :title="entry.email || entry.author">{{ entry.author }}</small>
          <time :datetime="new Date(entry.time * 1000).toISOString()" :title="fullTime(entry.time)">{{
            relativeTime(entry.time)
          }}</time>
        </button>
      </div>
    </div>
  </el-scrollbar>
  <div v-else class="git-inspector-empty">{{ t("git.graph.empty") }}</div>
</template>
