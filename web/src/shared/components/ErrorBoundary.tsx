import { Component, type ErrorInfo, type ReactNode } from "react";
import { ShieldAlert, Home, ArrowLeft } from "lucide-react";

interface Props {
  children?: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export class ErrorBoundary extends Component<Props, State> {
  public state: State = {
    hasError: false,
    error: null,
  };

  public static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  public componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error("Uncaught error boundary:", error, errorInfo);
  }

  private handleReset = () => {
    this.setState({ hasError: false, error: null });
    window.location.href = "/";
  };

  private handleGoBack = () => {
    this.setState({ hasError: false, error: null });
    window.history.back();
  };

  public render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }

      return (
        <div className="min-h-screen w-full flex items-center justify-center bg-background p-6">
          <div className="relative max-w-2xl w-full">
            <div className="absolute -inset-4 bg-gradient-to-br from-destructive/20 via-transparent to-primary/10 blur-3xl opacity-50 pointer-events-none" />

            <div className="relative overflow-hidden rounded-[2.5rem] border border-border/50 bg-card/50 backdrop-blur-2xl p-12 shadow-2xl space-y-10 text-center">
              <div className="flex flex-col items-center gap-6">
                <div className="inline-flex items-center justify-center rounded-[2rem] bg-destructive/10 p-8 text-destructive shadow-2xl shadow-destructive/10 ring-1 ring-destructive/20">
                  <ShieldAlert className="size-16" />
                </div>
                <div className="space-y-4">
                  <h1 className="text-5xl font-black tracking-tighter bg-gradient-to-b from-foreground to-foreground/60 bg-clip-text text-transparent italic">
                    Critical Error
                  </h1>
                  <p className="text-xl text-muted-foreground font-medium leading-relaxed max-w-md mx-auto">
                    The application encountered an unexpected state. Don't
                    worry, your data is safe.
                  </p>
                </div>
              </div>

              <div className="rounded-2xl bg-muted/30 p-6 font-mono text-left text-sm border border-border/50 overflow-auto max-h-40">
                <p className="text-destructive font-bold mb-2">Error Detail:</p>
                <p className="text-muted-foreground break-words">
                  {this.state.error?.name}: {this.state.error?.message}
                </p>
              </div>

              <div className="flex flex-col sm:flex-row items-center justify-center gap-4">
                <button
                  onClick={this.handleGoBack}
                  className="w-full sm:w-auto inline-flex items-center justify-center gap-3 rounded-2xl border border-border bg-background px-8 py-4 text-base font-bold transition-all hover:bg-accent hover:scale-105 active:scale-95"
                >
                  <ArrowLeft className="size-5" />
                  Go Back
                </button>
                <button
                  onClick={this.handleReset}
                  className="w-full sm:w-auto inline-flex items-center justify-center gap-3 rounded-2xl bg-foreground text-background px-8 py-4 text-base font-bold shadow-xl transition-all hover:opacity-90 hover:scale-105 active:scale-95"
                >
                  <Home className="size-5" />
                  Return Home
                </button>
              </div>
            </div>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
}
