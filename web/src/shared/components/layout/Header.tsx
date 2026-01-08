import { Bell } from "lucide-react";
import { ThemeToggle } from "@/shared/components/ui/ThemeToggle";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { LanguageToggle } from "./LanguageToggle";

export function Header() {
  return (
    <header className="sticky top-0 z-40 flex h-16 items-center gap-4 border-b border-border bg-background px-6">
      <SidebarTrigger className="-ml-2" />

      <div className="flex flex-1 items-center gap-4">
        <h2 className="text-lg font-semibold">Dashboard</h2>
      </div>

      <div className="flex items-center gap-2">
        <LanguageToggle />
        <ThemeToggle />
        <button className="relative size-9 flex items-center justify-center rounded-xl bg-card border border-border/50 text-muted-foreground transition-all hover:bg-accent hover:text-foreground">
          <Bell className="size-5" />
          <span className="absolute top-2 right-2 size-2 bg-primary rounded-full ring-2 ring-background" />
        </button>
        <div className="size-9 rounded-xl bg-gradient-to-br from-primary/20 to-primary/5 border border-primary/20 flex items-center justify-center">
          <span className="text-xs font-black text-primary italic">MK</span>
        </div>
      </div>
    </header>
  );
}
