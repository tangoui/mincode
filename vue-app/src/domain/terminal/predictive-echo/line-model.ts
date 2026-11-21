/**
 * 预测行模型：光标所在行在"队列里的预测都被确认后"的样子。
 * 引擎在按键时对它执行插入/删除/移动，并据此生成覆盖层格子。
 */

import { charWidth, isPredictableChar } from "./char-width.ts";
import type { OverlayCell } from "./types.ts";

export type Op =
  | { kind: "char"; char: string; width: 1 | 2 }
  | { kind: "del" }
  | { kind: "left" }
  | { kind: "right" };

/** 预测后的一格。src = 来自视图的哪一列（-1 = 预测字或补进来的空格） */
export interface Slot { char: string; width: 0 | 1 | 2; dim: boolean; src: number; predicted: boolean }

/** 一次按键对行的影响，供确认/纠错核对 */
export interface OpEffect {
  before: number;    // 预测前光标列
  after: number;     // 预测后光标列
  col: number;       // 要核对的格子；-1 = 只看光标（方向键）
  baseline: string;  // 该格预测前的字符
  expected: string;  // 该格预测后的字符
  /** 删除时右侧是灰色建议：该格为空格或任意 dim 格子都算确认 */
  dimOk: boolean;
}

/** 一次输入最多预测这么多个字（输入法一次提交的词） */
const MAX_GROUP = 32;

export const norm = (char: string) => (char === "" ? " " : char);
export const isBlank = (slot: Slot) => slot.width !== 0 && norm(slot.char) === " ";
const blankSlot = (): Slot => ({ char: " ", width: 1, dim: false, src: -1, predicted: false });

/** 把一次 onData 拆成可预测的按键；null = 不可预测（控制键、混合输入、过长） */
export function classify(data: string, appCursorKeys: boolean): Op[] | null {
  if (data === "\x7f" || data === "\b") return [{ kind: "del" }];
  if (data === (appCursorKeys ? "\x1bOD" : "\x1b[D")) return [{ kind: "left" }];
  if (data === (appCursorKeys ? "\x1bOC" : "\x1b[C")) return [{ kind: "right" }];
  const chars = [...data];
  if (chars.length === 0 || chars.length > MAX_GROUP || !chars.every(isPredictableChar)) return null;
  return chars.map((char) => ({ kind: "char", char, width: charWidth(char) }));
}

const firstNonBlankFrom = (line: Slot[], col: number): number => {
  for (let i = col; i < line.length; i += 1) if (!isBlank(line[i])) return i;
  return -1;
};

const rightIsSuggestion = (line: Slot[], col: number): boolean => {
  const next = firstNonBlankFrom(line, col);
  return next >= 0 && line[next].dim;
};

/** 在预测行上执行一次按键；不可预测（越界、会折行）时返回 null 且不改动 line。 */
export function applyOp(line: Slot[], op: Op, cursor: number): OpEffect | null {
  const cols = line.length;
  const leftStep = () => (cursor >= 2 && line[cursor - 1].width === 0 ? 2 : 1);
  const move = (after: number): OpEffect =>
    ({ before: cursor, after, col: -1, baseline: "", expected: "", dimOk: false });
  switch (op.kind) {
    case "char": {
      const w = op.width;
      if (cursor + w >= cols) return null;
      const baseline = norm(line[cursor].char);
      const added: Slot[] = [{ char: op.char, width: w, dim: false, src: -1, predicted: true }];
      if (w === 2) added.push({ char: "", width: 0, dim: false, src: -1, predicted: true });
      if (rightIsSuggestion(line, cursor)) {
        // 右侧是灰色建议：不右移，只盖住光标格
        line.splice(cursor, w, ...added);
        if (line[cursor + w]?.width === 0) line[cursor + w] = blankSlot();
      } else {
        for (let i = cols - w; i < cols; i += 1) if (!isBlank(line[i])) return null; // 会折行
        line.splice(cursor, 0, ...added);
        line.length = cols;
      }
      return { before: cursor, after: cursor + w, col: cursor, baseline, expected: op.char, dimOk: false };
    }
    case "del": {
      if (cursor === 0) return null;
      const w = leftStep();
      const start = cursor - w;
      const baseline = norm(line[start].char);
      if (rightIsSuggestion(line, cursor)) {
        // 右侧是灰色建议：不左移它，只把删掉的格子画成空格；服务器会重画建议
        for (let i = start; i < cursor; i += 1) line[i] = blankSlot();
        return { before: cursor, after: start, col: start, baseline, expected: " ", dimOk: true };
      }
      line.splice(start, w);
      for (let i = 0; i < w; i += 1) line.push(blankSlot());
      return { before: cursor, after: start, col: start, baseline, expected: norm(line[start].char), dimOk: false };
    }
    case "left":
      return cursor === 0 ? null : move(cursor - leftStep());
    case "right": {
      // 右边已没有内容时 shell 不会移动光标
      if (firstNonBlankFrom(line, cursor) < 0) return null;
      const after = cursor + (line[cursor].width === 2 ? 2 : 1);
      return after > cols - 1 ? null : move(after);
    }
  }
}

/** 预测行与建立时的视图行之差：预测字，以及被移位、需要重画的原有格子 */
export function overlayCells(line: Slot[], base: Slot[]): OverlayCell[] {
  const cells: OverlayCell[] = [];
  for (let col = 0; col < line.length; col += 1) {
    const slot = line[col];
    if (slot.width === 0) continue;
    if (slot.predicted) {
      cells.push({ col, char: slot.char, width: slot.width, predicted: true });
    } else if (slot.src !== col && !(isBlank(slot) && isBlank(base[col]))) {
      const cell: OverlayCell = { col, char: norm(slot.char), width: slot.width, predicted: false };
      if (slot.src >= 0) cell.sourceCol = slot.src;
      cells.push(cell);
    }
  }
  return cells;
}
