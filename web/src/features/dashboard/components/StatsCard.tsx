import type { LucideIcon } from "lucide-react";
import { cn } from "@/shared/lib/utils/cn";

interface StatsCardProps {
  name: string;
  value: string | number;
  icon: LucideIcon;
  change?: string;
  changeType?: "positive" | "negative" | "neutral";
}

export function StatsCard({
  name,
  value,
  icon: Icon,
  change,
  changeType = "neutral",
}: StatsCardProps) {
  return (
    <div className="rounded-2xl border border-border bg-card p-8 shadow-sm transition-all hover:shadow-lg hover:scale-[1.02] active:scale-95 group hover:border-primary/30">
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-4">
          <div className="rounded-xl bg-primary/10 p-3 text-primary transition-colors group-hover:bg-primary group-hover:text-primary-foreground">
            <Icon className="h-6 w-6" />
          </div>
          <p className="text-base font-bold text-muted-foreground group-hover:text-foreground">
            {name}
          </p>
        </div>

        {change && (
          <span
            className={cn(
              "text-base font-bold",
              changeType === "positive" && "text-green-600 dark:text-green-400",
              changeType === "negative" && "text-red-600 dark:text-red-400",
              changeType === "neutral" && "text-muted-foreground"
            )}
          >
            {change}
          </span>
        )}
      </div>
      <div className="mt-2">
        <span className="text-4xl font-extrabold tracking-tight">{value}</span>
      </div>
    </div>
  );
}
