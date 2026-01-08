import { cn } from "@/shared/lib/utils/cn";
import { AlertCircle, RefreshCcw } from "lucide-react";

interface ErrorAlertProps {
  title?: string;
  message?: string;
  retry?: () => void;
  className?: string;
}

export function ErrorAlert({
  title = "Something went wrong",
  message = "Failed to load data. Please try again.",
  retry,
  className,
}: Readonly<ErrorAlertProps>) {
  return (
    <div
      className={cn(
        "relative overflow-hidden rounded-2xl border border-destructive/20 bg-destructive/5 p-8 text-center",
        className
      )}
    >
      <div className="absolute inset-0 bg-gradient-to-br from-destructive/10 to-transparent pointer-events-none" />
      <div className="relative flex flex-col items-center gap-6">
        <div className="inline-flex items-center justify-center rounded-2xl bg-destructive/10 p-4 text-destructive shadow-inner">
          <AlertCircle className="size-8" />
        </div>
        <div className="space-y-2">
          <h3 className="text-2xl font-black tracking-tight">{title}</h3>
          <p className="text-muted-foreground font-medium max-w-md mx-auto">
            {message}
          </p>
        </div>
        {retry && (
          <button
            onClick={retry}
            className="inline-flex items-center gap-2 rounded-xl bg-destructive px-6 py-3 text-sm font-bold text-destructive-foreground transition-all hover:bg-destructive/90 hover:scale-105 active:scale-95 shadow-lg shadow-destructive/20"
          >
            <RefreshCcw className="size-4" />
            Try Again
          </button>
        )}
      </div>
    </div>
  );
}
