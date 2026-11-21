export interface SftpFileListEntry {
  name: string;
  path: string;
  isDir: boolean;
}

export function formatSftpTransferBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const amount = value / 1024 ** index;
  return `${amount >= 100 || index === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[index]}`;
}

/** Text shown in the two trailing columns of an SFTP file row. */
export interface SftpFileListCells {
  detail: string;
  detailTitle?: string;
  size: string;
}
