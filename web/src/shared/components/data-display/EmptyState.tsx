import { type ReactNode } from "react";
import { type LucideIcon, Plus } from "lucide-react";
interface EmptyStateProps {
  title: string;
  description: string;
  icon: LucideIcon;
  action?: {
    label: string;
    onClick: () => void;
    icon?: LucideIcon;
  };
  children?: ReactNode;
}

export function EmptyState({
  title,
  description,
  icon: Icon,
  action,
  children,
}: Readonly<EmptyStateProps>) {
  return (
    <div className="relative overflow-hidden rounded-2xl border border-dashed border-border bg-card/50 p-16 text-center">
      <div className="relative max-w-md mx-auto space-y-6">
        <div className="inline-flex items-center justify-center rounded-2xl bg-primary/10 p-6 text-primary">
          <Icon className="size-12" />
        </div>
        <div className="space-y-2">
          <h3 className="text-2xl font-bold text-foreground">{title}</h3>
          <p className="text-base text-muted-foreground font-medium">
            {description}
          </p>
        </div>

        {action ? (
          <button
            onClick={action.onClick}
            className="inline-flex items-center gap-2 rounded-xl bg-primary px-6 py-3 text-base font-bold text-primary-foreground transition-all hover:bg-primary/90 active:scale-95"
          >
            {action.icon ? (
              <action.icon className="size-5" />
            ) : (
              <Plus className="size-5" />
            )}
            {action.label}
          </button>
        ) : (
          children
        )}
      </div>
    </div>
  );
}
