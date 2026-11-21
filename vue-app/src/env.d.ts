/// <reference types="vite/client" />

import type { DevPerformanceInspector } from "@/services/dev-performance";

declare global {
  interface Window {
    __MINCODE_PERFORMANCE__?: DevPerformanceInspector;
  }
}

export {};
