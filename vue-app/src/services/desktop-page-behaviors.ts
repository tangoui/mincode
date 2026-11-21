// 桌面窗口里的页面行为（浏览器预览不需要，main.ts 只在桌面运行时安装）：
//
// - 文件拖入高亮：拖着文件经过 [data-file-drop-target] 区域时给它加
//   file-drop-target-active 类；落下的文件由 Go 侧 OnFilesDropped 处理
//   （main.go handleFileDrop），页面只负责视觉反馈并阻止 WebView 自己打开文件。
// - 默认右键菜单：WebView 的默认菜单（重新载入、检查元素等）只在可编辑内容、
//   有选中文字或可输入的输入框上保留；组件自己的右键菜单会先 preventDefault，不受影响。
//   开发构建始终保留默认菜单，方便调试。

const DROP_TARGET_SELECTOR = "[data-file-drop-target]";
const DROP_TARGET_ACTIVE_CLASS = "file-drop-target-active";
// 光标离开窗口后多久清掉高亮；仍在窗口上方时 dragover 会持续触发并取消这次清理。
const DRAG_EXIT_GRACE_MS = 150;

function isFileDrag(event: DragEvent) {
  return Array.from(event.dataTransfer?.types ?? []).includes("Files");
}

function installFileDropHighlight(doc: Document) {
  let active: Element | null = null;
  let exitTimer: ReturnType<typeof setTimeout> | undefined;

  const setActive = (target: Element | null) => {
    if (active === target) return;
    active?.classList.remove(DROP_TARGET_ACTIVE_CLASS);
    target?.classList.add(DROP_TARGET_ACTIVE_CLASS);
    active = target;
  };
  const clear = () => {
    clearTimeout(exitTimer);
    exitTimer = undefined;
    setActive(null);
  };
  const track = (event: DragEvent) => {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    clearTimeout(exitTimer);
    const under = doc.elementFromPoint(event.clientX, event.clientY);
    const target = under?.closest(DROP_TARGET_SELECTOR) ?? null;
    setActive(target);
    if (event.dataTransfer) event.dataTransfer.dropEffect = target ? "copy" : "none";
  };

  doc.addEventListener("dragenter", track);
  doc.addEventListener("dragover", track);
  doc.addEventListener("dragleave", (event) => {
    if (!isFileDrag(event)) return;
    // relatedTarget 为 null：光标可能离开了窗口（macOS 在原生拖动中也会立即报一次），
    // 稍后清理；仍在窗口内时下一个 dragover 会取消它。
    if (event.relatedTarget === null) {
      clearTimeout(exitTimer);
      exitTimer = setTimeout(clear, DRAG_EXIT_GRACE_MS);
    }
  });
  doc.addEventListener("drop", (event) => {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    clear();
  });
}

function allowsDefaultContextMenu(target: EventTarget | null, view: Window): boolean {
  if (!(target instanceof Element)) return false;
  if (target instanceof HTMLElement && target.isContentEditable) return true;
  if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) {
    return !target.readOnly && !target.disabled;
  }
  const selection = view.getSelection();
  if (!selection || selection.isCollapsed || !selection.toString()) return false;
  for (let i = 0; i < selection.rangeCount; i++) {
    const range = selection.getRangeAt(i);
    if (range.intersectsNode(target)) return true;
  }
  return false;
}

function installContextMenuPolicy(view: Window) {
  view.addEventListener("contextmenu", (event) => {
    if (event.defaultPrevented) return;
    if (!allowsDefaultContextMenu(event.target, view)) event.preventDefault();
  });
}

let installed = false;

export function installDesktopPageBehaviors(view: Window = window, options: { devContextMenu?: boolean } = {}) {
  if (installed) return;
  installed = true;
  installFileDropHighlight(view.document);
  if (!options.devContextMenu) installContextMenuPolicy(view);
}
