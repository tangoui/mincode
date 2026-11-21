// Monaco 命令注册桥接。
//
// 本项目只引入 monaco 的 editor.api（不含 editor.main），所以没有 monaco.commands
// 命名空间；但代码操作（code action）的 command 执行仍走平台命令注册表，
// 这里直接接入该注册表，供 LSP 等模块注册自定义命令。
// @ts-expect-error monaco 内部模块没有类型声明
import { CommandsRegistry } from "../../node_modules/monaco-editor/esm/vs/platform/commands/common/commands.js";

export type MonacoCommandHandler = (...args: unknown[]) => unknown;

export function registerMonacoCommand(id: string, handler: MonacoCommandHandler): () => void {
  const disposable = CommandsRegistry.registerCommand(id, (_accessor: unknown, ...args: unknown[]) =>
    handler(...args)
  );
  return () => disposable.dispose();
}

/** Whether a command (Monaco's own editor commands included) is registered. */
export function hasMonacoCommand(id: string): boolean {
  return Boolean(CommandsRegistry.getCommand(id));
}
