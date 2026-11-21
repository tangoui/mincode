/**
 * 覆盖层格子的颜色：把 xterm 缓冲区格子的前景/背景（默认、256 色调色板、RGB、反显）
 * 解析成 CSS 颜色，重画被移位的格子和真实光标下那格时保持原有配色（vim 配色、powerline、
 * 语法高亮）。纯函数，不依赖 xterm：只用到 IBufferCell 的这几个公开方法。
 */

export interface CellColorSource {
  isInverse(): number;
  isFgDefault(): boolean;
  isFgRGB(): boolean;
  isFgPalette(): boolean;
  getFgColor(): number;
  isBgDefault(): boolean;
  isBgRGB(): boolean;
  isBgPalette(): boolean;
  getBgColor(): number;
}

/** xterm ITheme 里与格子颜色有关的字段 */
export interface CellColorTheme {
  foreground?: string; background?: string;
  black?: string; red?: string; green?: string; yellow?: string;
  blue?: string; magenta?: string; cyan?: string; white?: string;
  brightBlack?: string; brightRed?: string; brightGreen?: string; brightYellow?: string;
  brightBlue?: string; brightMagenta?: string; brightCyan?: string; brightWhite?: string;
  /** 16–255 号颜色（xterm 的 extendedAnsi），缺的按标准 xterm 调色板补 */
  extendedAnsi?: string[];
}

export interface CellPalette {
  foreground: string;
  background: string;
  /** 0–255 */
  ansi: readonly string[];
}

const ANSI_NAMES = [
  "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite",
] as const;

/** xterm.js 的默认 16 色 */
const DEFAULT_ANSI = [
  "#2e3436", "#cc0000", "#4e9a06", "#c4a000", "#3465a4", "#75507b", "#06989a", "#d3d7cf",
  "#555753", "#ef2929", "#8ae234", "#fce94f", "#729fcf", "#ad7fa8", "#34e2e2", "#eeeeec",
];

const CUBE = [0x00, 0x5f, 0x87, 0xaf, 0xd7, 0xff];

export const rgbHex = (rgb: number) => `#${(rgb & 0xffffff).toString(16).padStart(6, "0")}`;

const hex = (r: number, g: number, b: number) => rgbHex((r << 16) | (g << 8) | b);

/** 标准 xterm 256 色中的 16–255：6×6×6 色立方 + 24 级灰 */
export function extendedAnsiColor(index: number): string {
  if (index < 232) {
    const n = index - 16;
    return hex(CUBE[Math.floor(n / 36) % 6], CUBE[Math.floor(n / 6) % 6], CUBE[n % 6]);
  }
  const grey = 8 + (index - 232) * 10;
  return hex(grey, grey, grey);
}

export function buildCellPalette(theme: CellColorTheme, foreground: string, background: string): CellPalette {
  const ansi: string[] = [];
  for (let i = 0; i < 16; i += 1) ansi.push(theme[ANSI_NAMES[i]] ?? DEFAULT_ANSI[i]);
  for (let i = 16; i < 256; i += 1) ansi.push(theme.extendedAnsi?.[i - 16] ?? extendedAnsiColor(i));
  return { foreground: theme.foreground ?? foreground, background: theme.background ?? background, ansi };
}

/** 格子实际显示的前景/背景（反显已交换） */
export function resolveCellColors(cell: CellColorSource, palette: CellPalette): { fg: string; bg: string } {
  const pick = (isDefault: boolean, isRgb: boolean, isPalette: boolean, color: number, fallback: string) => {
    if (isDefault) return fallback;
    if (isRgb) return rgbHex(color);
    if (isPalette) return palette.ansi[color] ?? fallback;
    return fallback;
  };
  const fg = pick(cell.isFgDefault(), cell.isFgRGB(), cell.isFgPalette(), cell.getFgColor(), palette.foreground);
  const bg = pick(cell.isBgDefault(), cell.isBgRGB(), cell.isBgPalette(), cell.getBgColor(), palette.background);
  return cell.isInverse() ? { fg: bg, bg: fg } : { fg, bg };
}
