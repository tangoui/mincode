/** IPv4 subnet calculations using unsigned 32-bit integer operations. */
export interface SubnetInfo {
  network: string;
  broadcast: string;
  netmask: string;
  wildcard: string;
  firstHost: string | null;
  lastHost: string | null;
  hostCount: number;
  prefix: number;
  cidr: string;
}

function parseIp(ip: string): number {
  const parts = ip.trim().split(".");
  if (parts.length !== 4) throw new Error("invalid-ipv4");

  let value = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) throw new Error("invalid-ipv4");
    const octet = Number(part);
    if (octet > 255) throw new Error("invalid-ipv4");
    value = ((value << 8) | octet) >>> 0;
  }
  return value;
}

function formatIp(value: number): string {
  return [(value >>> 24) & 0xff, (value >>> 16) & 0xff, (value >>> 8) & 0xff, value & 0xff].join(
    "."
  );
}

function maskOfPrefix(prefix: number): number {
  if (!Number.isInteger(prefix) || prefix < 0 || prefix > 32) {
    throw new Error("invalid-prefix");
  }
  return prefix === 0 ? 0 : (~0 << (32 - prefix)) >>> 0;
}

export function prefixToMask(prefix: number): string {
  return formatIp(maskOfPrefix(prefix));
}

export function maskToPrefix(mask: string): number {
  const value = parseIp(mask);
  let prefix = 0;
  while (prefix < 32 && (value & (0x80000000 >>> prefix)) !== 0) prefix += 1;
  if (maskOfPrefix(prefix) !== value) throw new Error("non-contiguous-mask");
  return prefix;
}

export function calcSubnet(cidr: string): SubnetInfo {
  const match = cidr.trim().match(/^(.+)\/(\d{1,2})$/);
  if (!match) throw new Error("invalid-cidr");

  const ip = parseIp(match[1]);
  const prefix = Number(match[2]);
  const mask = maskOfPrefix(prefix);
  const network = (ip & mask) >>> 0;
  const broadcast = (network | (~mask >>> 0)) >>> 0;
  const hasTraditionalHostRange = prefix < 31;
  const hostCount = prefix === 32 ? 1 : prefix === 31 ? 2 : broadcast - network - 1;

  return {
    network: formatIp(network),
    broadcast: formatIp(broadcast),
    netmask: formatIp(mask),
    wildcard: formatIp(~mask >>> 0),
    firstHost: hasTraditionalHostRange ? formatIp(network + 1) : null,
    lastHost: hasTraditionalHostRange ? formatIp(broadcast - 1) : null,
    hostCount,
    prefix,
    cidr: `${formatIp(network)}/${prefix}`,
  };
}

export function ipInCidr(ip: string, cidr: string): boolean {
  const info = calcSubnet(cidr);
  const value = parseIp(ip);
  const mask = maskOfPrefix(info.prefix);
  return (value & mask) >>> 0 === parseIp(info.network);
}
