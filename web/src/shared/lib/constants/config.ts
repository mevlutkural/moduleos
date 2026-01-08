export const CONFIG = {
  API_BASE_URL: import.meta.env.VITE_API_BASE_URL || "http://localhost:3000",
  API_TIMEOUT: 10000,
  WS_URL: import.meta.env.VITE_WS_URL || "ws://localhost:3000",
} as const;

export const QUERY_CONFIG = {
  STALE_TIME: 1000 * 60 * 5,
  CACHE_TIME: 1000 * 60 * 10,
  RETRY: 3,
  RETRY_DELAY: 1000,
} as const;

export const APP_INFO = {
  NAME: "ModuleOS",
  VERSION: "0.1.0",
  DESCRIPTION: "Self-hosted PaaS Platform",
} as const;
