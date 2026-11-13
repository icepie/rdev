import { ServerCapabilitiesProvider } from "./lib/server-capabilities";
import { StrictMode, lazy, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Route, Routes, Navigate, useLocation } from "react-router-dom";
import "./styles/app.css";
import { ThemeProvider } from "./lib/theme";
import { I18nProvider, useI18n } from "./lib/i18n";
import { TooltipProvider } from "./components/tooltip";
import { AppShell } from "./shell/AppShell";
import { DevicesProvider } from "./lib/devices";
import { UnauthorizedHandler } from "./shell/UnauthorizedHandler";
const DashboardPage = lazy(() => import("./pages/Dashboard").then((m) => ({ default: m.DashboardPage })));
const AgentPage = lazy(() => import("./pages/Agent").then((m) => ({ default: m.AgentPage })));
const TerminalPage = lazy(() => import("./pages/Terminal").then((m) => ({ default: m.TerminalPage })));
const SessionsPage = lazy(() => import("./pages/Sessions").then((m) => ({ default: m.SessionsPage })));
const FilesPage = lazy(() => import("./pages/Files").then((m) => ({ default: m.FilesPage })));
const BatchPage = lazy(() => import("./pages/Batch").then((m) => ({ default: m.BatchPage })));
const DesktopPage = lazy(() => import("./pages/Desktop").then((m) => ({ default: m.DesktopPage })));
const TasksPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.TasksPage })));
const NewTaskPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.NewTaskPage })));
const EditTaskPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.EditTaskPage })));
const TaskDetailPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.TaskDetailPage })));
const TaskRunPage = lazy(() => import("./pages/Tasks").then((m) => ({ default: m.TaskRunPage })));

const BrowserPage = lazy(() => import("./pages/Browser").then(m => ({ default: m.BrowserPage })));

function TitleSync() {
  const { t } = useI18n();
  const { pathname } = useLocation();
  useEffect(() => {
    const titles: Record<string, string> = {
      "/browser": "nav.browser", "/terminal": "title.terminal", "/remote-desktop": "title.desktop",
      "/batch": "title.batch", "/sessions": "title.sessions", "/files": "title.files", "/tasks": "tasks.title",
    };
    document.title = pathname === "/agent" ? `RDev - ${t("nav.agent")}` : t(titles[pathname] ?? (pathname.startsWith("/tasks/") ? "tasks.title" : "title.index"));

  }, [pathname, t]);
  return null;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <I18nProvider>
        <TooltipProvider>
          <UnauthorizedHandler>
            <BrowserRouter>
              <ServerCapabilitiesProvider>
              <DevicesProvider>
                <TitleSync />
                <Routes>
                  <Route element={<AppShell />}>
                    <Route path="/" element={<DashboardPage />} />
                    <Route path="/browser" element={<BrowserPage />} />
                    <Route path="/agent" element={<AgentPage />} />
                    <Route path="/terminal" element={<TerminalPage />} />
                    <Route path="/sessions" element={<SessionsPage />} />
                    <Route path="/files" element={<FilesPage />} />
                    <Route path="/tasks" element={<TasksPage />} />
                    <Route path="/tasks/new" element={<NewTaskPage />} />
                    <Route path="/tasks/runs/:id" element={<TaskRunPage />} />
                    <Route path="/tasks/:id/edit" element={<EditTaskPage />} />
                    <Route path="/tasks/:id" element={<TaskDetailPage />} />
                    <Route path="/batch" element={<BatchPage />} />
                    <Route path="/remote-desktop" element={<DesktopPage />} />
                    <Route path="*" element={<Navigate to="/" replace />} />
                  </Route>
                </Routes>
              </DevicesProvider>
              </ServerCapabilitiesProvider>
            </BrowserRouter>
          </UnauthorizedHandler>
        </TooltipProvider>
      </I18nProvider>
    </ThemeProvider>
  </StrictMode>,
);
