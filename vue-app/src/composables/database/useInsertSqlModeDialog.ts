import { ref } from "vue";
import type { InsertSqlMode } from "@/domain/database/insert-sql";

/**
 * INSERT SQL 生成方式选择弹窗：多行复制为 INSERT 与导出 SQL 格式共用。
 * chooseInsertSqlMode 以 Promise 返回用户选择，取消时 resolve undefined。
 */
export function useInsertSqlModeDialog() {
  const visible = ref(false);
  const mode = ref<InsertSqlMode>("rows");
  /** 已选行数；导出场景行数未知时为 0，界面不展示。 */
  const rowCount = ref(0);
  let resolver: ((value: InsertSqlMode | undefined) => void) | undefined;

  function chooseInsertSqlMode(count = 0): Promise<InsertSqlMode | undefined> {
    // 防御重入：上一次未决的选择按取消处理。
    resolver?.(undefined);
    rowCount.value = count;
    mode.value = "rows";
    visible.value = true;
    return new Promise((resolve) => {
      resolver = resolve;
    });
  }

  function settle(value: InsertSqlMode | undefined) {
    visible.value = false;
    const resolve = resolver;
    resolver = undefined;
    resolve?.(value);
  }

  function confirmInsertSqlMode() {
    settle(mode.value);
  }

  function cancelInsertSqlMode() {
    settle(undefined);
  }

  return {
    visible,
    mode,
    rowCount,
    chooseInsertSqlMode,
    confirmInsertSqlMode,
    cancelInsertSqlMode,
  };
}
