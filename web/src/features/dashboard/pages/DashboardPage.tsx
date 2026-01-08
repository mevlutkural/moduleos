import { Activity, Box, Rocket, TrendingUp } from "lucide-react";
import { StatsCard } from "../components/StatsCard";
import { ActivityFeed } from "../components/ActivityFeed";
import { DashboardSkeleton } from "../components/DashboardSkeleton";
import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/shared/lib/utils/cn";

export function DashboardPage() {
  const { t } = useTranslation();
  const [isLoading, setIsLoading] = useState(true);

  const stats = [
    {
      name: t("dashboard.stats.totalApps"),
      value: "12",
      icon: Box,
      change: "+2",
      changeType: "positive" as const,
    },
    {
      name: t("dashboard.stats.running"),
      value: "8",
      icon: Activity,
      change: "+1",
      changeType: "positive" as const,
    },
    {
      name: t("dashboard.stats.deployments"),
      value: "47",
      icon: Rocket,
      change: "+12",
      changeType: "positive" as const,
    },
    {
      name: t("dashboard.stats.successRate"),
      value: "94%",
      icon: TrendingUp,
      change: "+2%",
      changeType: "positive" as const,
    },
  ];

  useEffect(() => {
    const timer = setTimeout(() => setIsLoading(false), 500);
    return () => clearTimeout(timer);
  }, []);

  if (isLoading) {
    return <DashboardSkeleton />;
  }

  return (
    <div className="space-y-12">
      <div className="space-y-4">
        <h1 className="text-5xl font-extrabold tracking-tight">
          {t("dashboard.title")}
        </h1>
        <p className="text-xl text-muted-foreground font-medium max-w-2xl">
          {t("dashboard.description")}
        </p>
      </div>

      {}
      <div className="grid gap-8 md:grid-cols-2 lg:grid-cols-4">
        {stats.map((stat) => (
          <StatsCard
            key={stat.name}
            name={stat.name}
            value={stat.value}
            icon={stat.icon}
            change={stat.change}
            changeType={stat.changeType}
          />
        ))}
      </div>

      {}
      <div className="grid gap-10 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ActivityFeed />
        </div>

        {}
        <div className="space-y-10">
          <div className="rounded-2xl border border-border bg-card p-8 shadow-sm">
            <h3 className="text-xl font-bold mb-6 tracking-tight">
              {t("dashboard.platformStatus")}
            </h3>
            <div className="space-y-5">
              <StatusRow label="API Server" status="optimal" />
              <StatusRow label="Worker Nodes" status="optimal" />
              <StatusRow label="Database" status="optimal" />
              <StatusRow label="Image Registry" status="degraded" />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function StatusRow({
  label,
  status,
}: {
  label: string;
  status: "optimal" | "degraded" | "down";
}) {
  const colors = {
    optimal: "bg-green-500 shadow-green-500/50",
    degraded: "bg-yellow-500 shadow-yellow-500/50",
    down: "bg-red-500 shadow-red-500/50",
  };

  return (
    <div className="flex items-center justify-between group">
      <span className="text-base font-semibold text-muted-foreground group-hover:text-foreground transition-colors">
        {label}
      </span>
      <div className="flex items-center gap-3">
        <span className="text-sm font-bold capitalize">{status}</span>
        <div
          className={cn("size-2.5 rounded-full shadow-sm", colors[status])}
        />
      </div>
    </div>
  );
}
