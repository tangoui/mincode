/** 预测回显引擎与覆盖层之间的数据结构。 */

/**
 * width 0 = 宽字符的右半格。fg/bg = 格子实际显示的颜色（CSS，反显已交换），只在
 * cell(row, col, true) 时给出，供覆盖层按原配色重画；缺省为终端默认色。
 */
export interface ViewCell { char: string; width: 0 | 1 | 2; dim: boolean; fg?: string; bg?: string }

export interface TerminalView {
  cols: number; rows: number;
  cursorX: number; cursorY: number;            // 视口坐标
  /** 光标所在行的单调行标识（滚屏次数 + cursorY，不是缓冲区下标）：回滚缓冲写满后滚屏仍可区分新行 */
  cursorAbsY: number;
  alternate: boolean; atBottom: boolean; appCursorKeys: boolean;
  /** colors = 同时解析前景/背景（只有覆盖层要画的格子需要，保持开销与重画格数成正比） */
  cell(row: number, col: number, colors?: boolean): ViewCell;
}

/** predicted = 用户输入的预测字；否则是右移/左移后的原有格子（sourceCol 为缓冲区原列，缺省为空格） */
export interface OverlayCell { col: number; char: string; width: 1 | 2; predicted: boolean; sourceCol?: number }

export interface OverlayFrame { row: number; cells: OverlayCell[]; cursorCol: number }
