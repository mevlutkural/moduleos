import { Loader2 } from "lucide-react";
import { cn } from "@/shared/lib/utils/cn";

interface LoadingSpinnerProps {
  size?: "sm" | "md" | "lg" | "xl";
  className?: string;
  label?: string;
}

const sizeMap = {
  sm: "size-4 border-2",
  md: "size-8 border-3",
  lg: "size-14 border-4",
  xl: "size-20 border-5",
};

const iconSizeMap = {
  sm: "size-4",
  md: "size-8",
  lg: "size-14",
  xl: "size-20",
};

export function LoadingSpinner({
  size = "md",
  className,
  label,
}: Readonly<LoadingSpinnerProps>) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-6 p-12",
        className
      )}
    >
      <div className="relative">
        <div
          className={cn(
            "rounded-full border-primary/10 animate-[spin_2s_linear_infinite]",
            sizeMap[size]
          )}
        />
        <Loader2
          className={cn(
            "absolute inset-0 animate-spin text-primary",
            iconSizeMap[size]
          )}
        />
      </div>
      {label && (
        <p className="text-lg font-black tracking-widest text-muted-foreground uppercase animate-pulse">
          {label}
        </p>
      )}
    </div>
  );
}

export function LoadingPage() {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-xl">
      <div className="space-y-8 text-center">
        <LoadingSpinner size="xl" />
        <div className="space-y-2">
          <h2 className="text-3xl font-black tracking-tighter italic">
            Loading
          </h2>
          <p className="text-muted-foreground font-medium">
            Preparing your workspace...
          </p>
        </div>
      </div>
    </div>
  );
}
