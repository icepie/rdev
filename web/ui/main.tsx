import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Route, Routes, Navigate } from "react-router-dom";
import "./styles/app.css";
import { ThemeProvider } from "./lib/theme";
import { I18nProvider, useI18n } from "./lib/i18n";
import { TooltipProvider } from "./components/tooltip";
import { AppShell } from "./shell/AppShell";
import { DevicesProvider } from "./lib/devices";
import { UnauthorizedHandler } from "./shell/UnauthorizedHandler";
import { DashboardPage } from "./pages/Dashboard";
import { AgentPage } from "./pages/Agent";
import { TerminalPage } from "./pages/Terminal";
import { SessionsPage } from "./pages/Sessions";
import { FilesPage } from "./pages/Files";
import { BatchPage } from "./pages/Batch";
import { DesktopPage } from "./pages/Desktop";

function TitleSync() {
  const { t } = useI18n();
  document.title = t("title.index");
  return null;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <I18nProvider>
        <TooltipProvider>
          <UnauthorizedHandler>
            <BrowserRouter>
              <DevicesProvider>
                <TitleSync />
                <Routes>
                  <Route element={<AppShell />}>
                    <Route path="/" element={<DashboardPage />} />
                    <Route path="/agent" element={<AgentPage />} />
                    <Route path="/terminal" element={<TerminalPage />} />
                    <Route path="/sessions" element={<SessionsPage />} />
                    <Route path="/files" element={<FilesPage />} />
                    <Route path="/batch" element={<BatchPage />} />
                    <Route path="/remote-desktop" element={<DesktopPage />} />
                    <Route path="*" element={<Navigate to="/" replace />} />
                  </Route>
                </Routes>
              </DevicesProvider>
            </BrowserRouter>
          </UnauthorizedHandler>
        </TooltipProvider>
      </I18nProvider>
    </ThemeProvider>
  </StrictMode>,
);
