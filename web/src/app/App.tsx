import { Routes, Route } from "react-router-dom";
import { AppProviders } from "./providers";
import { DashboardLayout } from "@/shared/components/layout/DashboardLayout";
import { DashboardPage } from "@/features/dashboard/pages/DashboardPage";
import { ROUTES } from "@/shared/lib/constants/routes";

function App() {
  return (
    <AppProviders>
      <Routes>
        <Route path={ROUTES.DASHBOARD} element={<DashboardLayout />}>
          <Route index element={<DashboardPage />} />
        </Route>
      </Routes>
    </AppProviders>
  );
}

export default App;
