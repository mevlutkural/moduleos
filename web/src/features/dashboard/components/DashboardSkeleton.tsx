import { Skeleton } from "@/components/ui/skeleton";

export function DashboardSkeleton() {
  return (
    <div className="space-y-12">
      <div className="space-y-4">
        <Skeleton className="h-14 w-64" />
        <Skeleton className="h-6 w-96" />
      </div>

      <div className="grid gap-8 md:grid-cols-2 lg:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <div
            key={i}
            className="rounded-2xl border border-border bg-card p-8 shadow-sm space-y-4"
          >
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-4">
                <Skeleton className="size-12 rounded-xl" />
                <Skeleton className="h-6 w-24" />
              </div>
              <Skeleton className="h-6 w-12" />
            </div>
            <Skeleton className="h-10 w-20" />
          </div>
        ))}
      </div>

      <div className="grid gap-10 lg:grid-cols-3">
        <div className="lg:col-span-2 rounded-2xl border border-border bg-card shadow-sm p-8 space-y-6">
          <div className="flex items-center justify-between border-b border-border pb-6">
            <Skeleton className="h-8 w-48" />
            <Skeleton className="h-6 w-16" />
          </div>
          <div className="space-y-4">
            {Array.from({ length: 3 }).map((_, i) => (
              <div
                key={i}
                className="flex items-center gap-5 rounded-xl border border-border p-6"
              >
                <Skeleton className="size-14 rounded-2xl" />
                <div className="flex-1 space-y-2">
                  <div className="flex justify-between">
                    <Skeleton className="h-6 w-48" />
                    <Skeleton className="h-4 w-24" />
                  </div>
                  <Skeleton className="h-4 w-64" />
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="rounded-2xl border border-border bg-card p-8 shadow-sm space-y-8">
          <Skeleton className="h-8 w-40 mb-6" />
          <div className="space-y-6">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="flex items-center justify-between">
                <Skeleton className="h-6 w-32" />
                <div className="flex items-center gap-3">
                  <Skeleton className="h-4 w-16" />
                  <Skeleton className="size-2.5 rounded-full" />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
