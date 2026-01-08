import { SidebarProvider } from "@/components/ui/sidebar";
import { AppSidebar } from "@/shared/components/layout/AppSidebar";
import { Header } from "@/shared/components/layout/Header";
import { Outlet } from "react-router-dom";

export function DashboardLayout() {
  return (
    <SidebarProvider>
      <AppSidebar />
      <main className="flex-1 min-h-screen flex flex-col">
        <Header />
        <div className="flex-1 p-6 md:p-8 lg:p-10 max-w-[1600px] mx-auto w-full">
          <Outlet />
        </div>
      </main>
    </SidebarProvider>
  );
}
