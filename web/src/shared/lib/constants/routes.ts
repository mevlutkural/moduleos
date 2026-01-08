export const ROUTES = {
  DASHBOARD: "/",
} as const;

export type RouteKey = keyof typeof ROUTES;
