const protocolFailures = [
  [/context deadline exceeded|timed?\s*out/i, '操作超时，请确认目标状态后再决定是否重试。'],
  [/plugin (?:process|UI session).*(?:not running|stale|unavailable)|generation changed/i, '插件已停止或更新，请重新打开页面后重试。'],
  [/capability not granted|not allowed|permission denied/i, '操作未获授权，请检查插件能力和当前授权策略。'],
  [/payload.*(?:large|limit)|request.*(?:large|limit)/i, '请求数据超过限制，请缩小内容后重试。'],
  [/result.*(?:large|limit)|response.*(?:large|limit)/i, '操作结果超过限制，请缩小范围后重试。'],
];

// Return only allowlisted protocol diagnostics; business errors stay inside each plugin.
export function describePluginActionFailure(cause, fallback) {
  const message = typeof cause === 'string' ? cause : cause?.message;
  if (typeof message !== 'string') return fallback;
  for (const [pattern, description] of protocolFailures) {
    if (pattern.test(message)) return description;
  }
  return fallback;
}
