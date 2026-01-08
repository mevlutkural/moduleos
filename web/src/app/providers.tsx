import type { ReactNode } from "react";

import { BrowserRouter } from "react-router-dom";
import { Toaster } from "sonner";
import { ThemeProvider } from "@/shared/providers/ThemeProvider";
import { QueryProvider } from "@/shared/providers/QueryProvider";
import { ErrorBoundary } from "@/shared/components/ErrorBoundary";

interface AppProvidersProps {
  children: ReactNode;
}

export function AppProviders({ children }: Readonly<AppProvidersProps>) {
  return (
    <ErrorBoundary>
      <QueryProvider>
        <ThemeProvider defaultTheme="dark" storageKey="moduleos-theme">
          <BrowserRouter>
            {children}
            <Toaster position="top-right" richColors closeButton />
          </BrowserRouter>
        </ThemeProvider>
      </QueryProvider>
    </ErrorBoundary>
  );
}
