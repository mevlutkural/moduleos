import {
  QueryClient,
  QueryClientProvider,
  QueryCache,
  MutationCache,
} from "@tanstack/react-query";
import { ReactQueryDevtools } from "@tanstack/react-query-devtools";
import { useState } from "react";
import type { ReactNode } from "react";
import { QUERY_CONFIG } from "../lib/constants/config";
import { NotificationManager } from "../services/notification-manager";

interface QueryProviderProps {
  children: ReactNode;
}

export function QueryProvider({ children }: Readonly<QueryProviderProps>) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        queryCache: new QueryCache({
          onError: (error) => NotificationManager.handle(error),
        }),
        mutationCache: new MutationCache({
          onError: (error) => NotificationManager.handle(error),
        }),
        defaultOptions: {
          queries: {
            staleTime: QUERY_CONFIG.STALE_TIME,
            gcTime: QUERY_CONFIG.CACHE_TIME,
            retry: QUERY_CONFIG.RETRY,
            refetchOnWindowFocus: false,
          },
        },
      })
  );

  return (
    <QueryClientProvider client={queryClient}>
      {children}
      <ReactQueryDevtools initialIsOpen={false} />
    </QueryClientProvider>
  );
}
