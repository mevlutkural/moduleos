import { LayoutDashboard, Box, Settings, ChevronUp, User2 } from "lucide-react";
import { Link, useLocation } from "react-router-dom";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/shared/lib/utils/cn";
import { useTranslation } from "react-i18next";

export function AppSidebar() {
  const { t } = useTranslation();
  const location = useLocation();

  const navigation = [
    {
      name: t("navigation.dashboard"),
      href: "/",
      icon: LayoutDashboard,
    },
  ];

  return (
    <Sidebar
      collapsible="icon"
      className="border-r border-border/50 bg-card/50 backdrop-blur-xl"
    >
      <SidebarHeader className="border-b border-border/50 px-4 py-6">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              size="lg"
              asChild
              className="h-16 px-3 hover:bg-transparent"
            >
              <Link to="/" className="flex items-center gap-4">
                <div className="flex aspect-square size-12 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-lg shadow-primary/20">
                  <Box className="size-6" />
                </div>
                <div className="grid flex-1 text-left leading-tight">
                  <span className="truncate text-xl font-black tracking-tight italic">
                    ModuleOS
                  </span>
                  <span className="truncate text-xs font-bold text-muted-foreground uppercase tracking-widest">
                    v0.1.0 Beta
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent className="px-3 py-6">
        <SidebarGroup>
          <SidebarGroupLabel className="mb-4 px-4 text-[11px] font-black uppercase tracking-[0.2em] text-muted-foreground/70">
            Platform Management
          </SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu className="gap-2">
              {navigation.map((item) => {
                const isActive = location.pathname === item.href;
                return (
                  <SidebarMenuItem key={item.name}>
                    <SidebarMenuButton
                      asChild
                      className={cn(
                        "h-14 px-4 text-base font-bold transition-all duration-200 rounded-xl",
                        isActive
                          ? "bg-primary text-primary-foreground shadow-md shadow-primary/10"
                          : "text-muted-foreground hover:bg-accent/50 hover:text-foreground hover:translate-x-1"
                      )}
                    >
                      <Link to={item.href}>
                        <item.icon
                          className={cn(
                            "size-5",
                            isActive ? "animate-pulse" : ""
                          )}
                        />
                        <span>{item.name}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter className="border-t border-border/50 p-4 mt-auto">
        <SidebarMenu>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <SidebarMenuButton
                  size="lg"
                  className="h-16 px-3 rounded-2xl transition-all hover:bg-accent/50 data-[state=open]:bg-accent/50"
                >
                  <div className="flex aspect-square size-12 items-center justify-center rounded-xl bg-primary/10 text-primary shadow-inner">
                    <User2 className="size-6" />
                  </div>
                  <div className="grid flex-1 text-left leading-tight">
                    <span className="truncate text-base font-black tracking-tight text-foreground">
                      Admin Mevlüt
                    </span>
                    <span className="truncate text-xs font-bold text-muted-foreground uppercase opacity-70">
                      Super Admin
                    </span>
                  </div>
                  <ChevronUp className="ml-auto size-5 opacity-50" />
                </SidebarMenuButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                className="w-[--radix-dropdown-menu-trigger-width] min-w-64 rounded-2xl p-2 shadow-2xl border-border/50 bg-popover/90 backdrop-blur-xl"
                side="top"
                align="center"
                sideOffset={12}
              >
                <DropdownMenuItem className="py-3 px-4 rounded-xl font-bold cursor-pointer transition-colors focus:bg-primary focus:text-primary-foreground">
                  <Settings className="mr-3 size-5" />
                  Account Settings
                </DropdownMenuItem>
                <div className="h-px bg-border/50 my-1 mx-2" />
                <DropdownMenuItem className="py-3 px-4 rounded-xl font-bold cursor-pointer text-destructive transition-colors focus:bg-destructive focus:text-destructive-foreground">
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>

      <SidebarRail />
    </Sidebar>
  );
}
