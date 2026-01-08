import { Rocket, Clock, CheckCircle2, XCircle } from "lucide-react";
import { formatRelativeTime } from "@/shared/lib/utils/format";

interface ActivityItem {
  id: string;
  type: "deploy" | "error" | "success";
  title: string;
  description: string;
  timestamp: string;
}

const activities: ActivityItem[] = [
  {
    id: "1",
    type: "deploy",
    title: "New deployment triggered",
    description: "my-app • main branch",
    timestamp: new Date().toISOString(),
  },
  {
    id: "2",
    type: "success",
    title: "App started successfully",
    description: "api-gateway is now running",
    timestamp: new Date(Date.now() - 3600000).toISOString(),
  },
  {
    id: "3",
    type: "error",
    title: "Database connection failed",
    description: "redis-cache failed to start",
    timestamp: new Date(Date.now() - 7200000).toISOString(),
  },
];


export function ActivityFeed() {
  return (
    <div className="rounded-2xl border border-border bg-card shadow-sm overflow-hidden">
      <div className="border-b border-border px-8 py-6 bg-muted/30 flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Recent Activity</h2>
        <button className="text-sm font-bold text-primary hover:underline">
          View All
        </button>
      </div>
      <div className="p-4 sm:p-8">
        <div className="space-y-4">
          {activities.map((item) => (
            <div
              key={item.id}
              className="group flex items-center gap-5 rounded-xl border border-border p-6 transition-all hover:bg-accent/50 hover:scale-[1.01] hover:border-primary/20"
            >
              <ActivityIcon type={item.type} />
              <div className="flex-1">
                <div className="flex items-center justify-between mb-1">
                  <p className="font-bold text-lg leading-none">{item.title}</p>
                  <div className="flex items-center gap-1.5 text-sm text-muted-foreground font-medium">
                    <Clock className="size-3.5" />
                    {formatRelativeTime(item.timestamp)}
                  </div>
                </div>
                <p className="text-base text-muted-foreground font-medium">
                  {item.description}
                </p>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

function ActivityIcon({ type }: { type: ActivityItem["type"] }) {
  switch (type) {
    case "deploy":
      return (
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-blue-500/10 text-blue-600">
          <Rocket className="size-7" />
        </div>
      );
    case "error":
      return (
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-red-500/10 text-red-600">
          <XCircle className="size-7" />
        </div>
      );
    case "success":
      return (
        <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-green-500/10 text-green-600">
          <CheckCircle2 className="size-7" />
        </div>
      );
  }
}
