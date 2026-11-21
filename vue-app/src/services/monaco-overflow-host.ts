// Monaco 溢出浮层的窗口级共享宿主。
//
// 悬停气泡、"找不到定义"等消息窗这类 allowEditorOverflow 浮层，默认渲染在
// 编辑器自身 DOM 里；当锚点靠近文件首行时，浮层会溢出到编辑器上方，被编辑器
// 区域的 overflow 裁掉或被顶部标签/面包屑导航盖住。把这些浮层统一挂到窗口级
// 容器上（VS Code 同款做法），即可悬浮在所有 UI 之上。
//
// 定位安全性：Monaco 用 getDomNodePagePosition 把编辑器坐标换算成视口绝对
// 坐标后再写入浮层样式，因此宿主必须固定在视口原点（fixed + inset:0）。
//
// 样式安全性：StandaloneThemeService 只给 Monaco 作用域写入 --vscode-* 变量，
// suggest/hover 等组件样式又使用 `.monaco-editor .组件` 后代选择器。宿主同时带上
// monaco-component（接收主题变量）和 monaco-editor（恢复组件选择器祖先链），否则
// 补全列表不仅背景透明，文字、布局等规则也会整套失配。
let overflowHost: HTMLDivElement | undefined;

export function getMonacoOverflowWidgetsHost(): HTMLDivElement {
  if (!overflowHost) {
    overflowHost = document.createElement("div");
    overflowHost.className = "monaco-overflow-widgets-host monaco-component monaco-editor";
    document.body.appendChild(overflowHost);
  }
  return overflowHost;
}
