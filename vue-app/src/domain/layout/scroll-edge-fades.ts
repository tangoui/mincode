export interface ScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

export interface ScrollEdgeFades {
  top: boolean;
  bottom: boolean;
}

// Fractional scroll positions on scaled displays never reach the exact end.
const EDGE_TOLERANCE_PX = 1;

/** Which edges of a scroll area hide content, so they can fade out instead of cutting an item in half. */
export function scrollEdgeFades({ scrollTop, clientHeight, scrollHeight }: ScrollMetrics): ScrollEdgeFades {
  const hidden = scrollHeight - clientHeight;
  if (hidden <= EDGE_TOLERANCE_PX) return { top: false, bottom: false };
  return {
    top: scrollTop > EDGE_TOLERANCE_PX,
    bottom: scrollTop < hidden - EDGE_TOLERANCE_PX,
  };
}
