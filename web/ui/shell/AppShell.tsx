import { themePresets, type ThemePreset } from "../lib/themes";
import { Suspense, createContext, useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useMatch } from "react-router-dom";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useI18n } from "../lib/i18n";
import { useTheme, type ThemeChoice } from "../lib/theme";
import { Icon } from "../components/icon";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import { Button } from "../components/button";
import { cn } from "../lib/utils";
import { useMobileViewport } from "../hooks/use-mobile-viewport";
import { useIsCompactViewport } from "../components/hooks/use-compact-viewport";
import { useRailControl } from "../lib/rail-control";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "../components/coarse-pointer-sizing";

/* Vertical icon rail instead of a horizontal top bar: icon-only nav items
 * down the left edge, settings controls pinned to the bottom. */
const NAV_ITEMS = [
  { to: "/", key: "nav.dashboard", icon: "Home", end: true },
  { to: "/terminal", key: "nav.terminal", icon: "Terminal" },
  { to: "/remote-desktop", key: "nav.desktop", icon: "Monitor" },
  { to: "/batch", key: "nav.batch", icon: "Workflow" },
  { to: "/sessions", key: "nav.sessions", icon: "Radio" },
  { to: "/files", key: "nav.files", icon: "Files" },
  { to: "/agent", key: "nav.agent", icon: "Bot" },
] as const;

const THEME_CHOICES: { value: ThemeChoice; key: string }[] = [
  { value: "auto", key: "theme.auto" },
  { value: "light", key: "theme.light" },
  { value: "dark", key: "theme.dark" },
];

const RAIL_BUTTON_CLASS =
  "flex size-8 max-md:pointer-coarse:size-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground";

function RailLink({
  item,
  label,
}: {
  item: (typeof NAV_ITEMS)[number];
  label: string;
}) {
  const active = useMatch({
    path: item.to,
    end: "end" in item && item.end,
  });
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <NavLink
          to={item.to}
          end={"end" in item && item.end}
          aria-label={label}
          className={cn(
            RAIL_BUTTON_CLASS,
            active && "bg-state-active text-foreground",
          )}
        >
          <Icon name={item.icon} className="size-4.5" />
        </NavLink>
      </TooltipTrigger>
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  );
}

/* Pages embed their own mobile nav trigger in their header (bb: one
 * button opens one drawer). Pages with a contextual panel (device rail)
 * inject it as a drawer section via open(section). */
export interface MobileNavCtx {
  open: (section?: ReactNode) => void;
  close: () => void;
}
export const MobileNavContext = createContext<MobileNavCtx>({
  open: () => {},
  close: () => {},
});

/* The global nav rows, used when the drawer has no page section. */
export function MobileNavList() {
  const { t } = useI18n();
  return (
    <>
      {NAV_ITEMS.map((item) => (
        <MobileNavLink key={item.to} item={item} label={t(item.key)} />
      ))}
    </>
  );
}

/* Drawer row: icon + label (bb sidebar row), active = bg-state-active. */
function MobileNavLink({
  item,
  label,
}: {
  item: (typeof NAV_ITEMS)[number];
  label: string;
}) {
  const active = useMatch({
    path: item.to,
    end: "end" in item && item.end,
  });
  return (
    <NavLink
      to={item.to}
      end={"end" in item && item.end}
      className={cn(
        "flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm transition-colors",
        active
          ? "bg-state-active text-foreground"
          : "text-muted-foreground hover:bg-state-hover hover:text-foreground",
      )}
    >
      <Icon name={item.icon} className="size-4" />
      {label}
    </NavLink>
  );
}

/* Rail bottom buttons (GitHub / theme / language), shared by the desktop
 * rail and the mobile drawer rail — identical controls in both (bb: the
 * drawer keeps the whole sidebar, rail included). */
function RailFooterButtons({ className }: { className?: string }) {
  const { t, lang, setLang } = useI18n();
  const { choice, setChoice } = useTheme();
  return (
    <div className={cn("flex flex-col items-center gap-1", className)}>
      <Tooltip>
        <TooltipTrigger asChild>
          <a
            href="https://github.com/icepie/rdev"
            target="_blank"
            rel="noreferrer"
            aria-label="GitHub"
            className={RAIL_BUTTON_CLASS}
          >
            <Icon name="Code" className="size-4.5" />
          </a>
        </TooltipTrigger>
        <TooltipContent side="right">GitHub</TooltipContent>
      </Tooltip>
      <DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("theme.toggle")}
                className={RAIL_BUTTON_CLASS}
              >
                <Icon
                  name={choice === "light" ? "Sun" : choice === "dark" ? "Moon" : "Monitor"}
                  className="size-4.5"
                />
              </Button>
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent side="right">{t("theme.toggle")}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent side="right" align="end">
          {THEME_CHOICES.map((c) => (
            <DropdownMenuItem key={c.value} onSelect={() => setChoice(c.value)}>
              <span className="flex-1">{t(c.key)}</span>
              {choice === c.value && <Icon name="Check" className="size-4" />}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("lang.label")}
                className={RAIL_BUTTON_CLASS}
              >
                <Icon name="Globe" className="size-4.5" />
              </Button>
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent side="right">{t("lang.label")}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent side="right" align="end">
          <DropdownMenuItem onSelect={() => setLang("zh")}>
            <span className="flex-1">中文</span>
            {lang === "zh" && <Icon name="Check" className="size-4" />}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setLang("en")}>
            <span className="flex-1">English</span>
            {lang === "en" && <Icon name="Check" className="size-4" />}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

function RailSettings() {
  const { t, lang, setLang } = useI18n();
  const { choice, setChoice, preset, setPreset } = useTheme();
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <Button variant="ghost" size="icon" className={cn(RAIL_BUTTON_CLASS, "mt-auto")} aria-label={t("nav.settings")}><Icon name="Settings" className="size-4" /></Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent side="right" align="end">
      <DropdownMenuLabel>{t("theme.toggle")}</DropdownMenuLabel>
      {THEME_CHOICES.map((theme) => <DropdownMenuItem key={theme.value} onSelect={() => setChoice(theme.value)}><span className="flex-1">{t(theme.key)}</span>{choice === theme.value && <Icon name="Check" className="size-4" />}</DropdownMenuItem>)}
      <DropdownMenuSeparator />
      <DropdownMenuLabel>{t("theme.palette")}</DropdownMenuLabel>
      {(Object.keys(themePresets) as ThemePreset[]).map((id) => <DropdownMenuItem key={id} onSelect={() => setPreset(id)}><span className="flex-1">{themePresets[id].label}</span>{preset === id && <Icon name="Check" className="size-4" />}</DropdownMenuItem>)}
      <DropdownMenuSeparator />
      <DropdownMenuLabel>{t("lang.label")}</DropdownMenuLabel>
      {(["zh", "en"] as const).map((language) => <DropdownMenuItem key={language} onSelect={() => setLang(language)}><span className="flex-1">{language === "zh" ? "中文" : "English"}</span>{lang === language && <Icon name="Check" className="size-4" />}</DropdownMenuItem>)}
      <DropdownMenuSeparator />
      <DropdownMenuItem asChild><a href="https://github.com/icepie/rdev" target="_blank" rel="noreferrer">GitHub</a></DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>;
}

/* Drawer rail icon (bb: the drawer keeps the desktop rail column). */
function DrawerRailLink({ item }: { item: (typeof NAV_ITEMS)[number] }) {
  const { t } = useI18n();
  const active = useMatch({
    path: item.to,
    end: "end" in item && item.end,
  });
  return (
    <NavLink
      to={item.to}
      end={"end" in item && item.end}
      aria-label={t(item.key)}
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-md transition-colors max-md:pointer-coarse:size-9",
        active
          ? "bg-state-active text-foreground"
          : "text-muted-foreground hover:bg-state-hover hover:text-foreground",
      )}
    >
      <Icon name={item.icon} className="size-4" />
    </NavLink>
  );
}

/** Hidden with the owning sidebar; mobile has no persistent navigation rail. */
export function MobileWorkspaceNavigation() {
  const { t } = useI18n();
  return <nav aria-label={t("nav.label")} className="flex shrink-0 flex-wrap items-center gap-1 border-b border-border-seam p-2 md:hidden">
    {NAV_ITEMS.map(item => <RailLink key={item.to} item={item} label={t(item.key)} />)}
    <RailSettings />
  </nav>;
}

export function AppShell() {
  const shellRef = useRef<HTMLDivElement>(null);
  const compactViewport = useIsCompactViewport();
  useMobileViewport(shellRef, compactViewport);
  const { t, lang, setLang } = useI18n();
  const { choice, setChoice, preset, setPreset } = useTheme();
  const location = useLocation();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [drawerSection, setDrawerSection] = useState<ReactNode>(null);
  const railControl = useRailControl();
  const navTrigger = useRef<HTMLElement | null>(null);

  /* Close the mobile drawer on navigation (bb: drawer dismisses on pick);
   * the injected page section goes with it. */
  useEffect(() => {
    setMobileNavOpen(false);
    setDrawerSection(null);
  }, [location.pathname]);

  useEffect(() => {
    const desktop = window.matchMedia("(min-width: 768px)");
    const dismiss = () => {
      if (desktop.matches) setMobileNavOpen(false);
    };
    desktop.addEventListener("change", dismiss);
    return () => desktop.removeEventListener("change", dismiss);
  }, []);

  return (
    <div ref={shellRef} className="relative flex h-full min-h-0 flex-row overflow-hidden max-md:pt-[env(safe-area-inset-top)] max-md:pr-[env(safe-area-inset-right)] max-md:pb-[var(--app-safe-area-bottom,env(safe-area-inset-bottom))] max-md:pl-[env(safe-area-inset-left)]">
      {/* Compact workspaces expose navigation only inside their sidebar. */}
      <nav
        aria-label={t("nav.label")}
        className="hidden w-12 shrink-0 flex-col items-center gap-0.5 overflow-y-auto border-r border-border-seam bg-sidebar py-2 select-none md:flex"
      >
        {railControl ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-label={railControl.collapsed ? t("rail.expand") : t("rail.collapse")}
                onClick={railControl.toggle}
                className={cn(RAIL_BUTTON_CLASS, "mb-2 cursor-pointer")}
              >
                <Icon name="PanelLeft" className="size-4.5" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">
              {railControl.collapsed ? t("rail.expand") : t("rail.collapse")}
            </TooltipContent>
          </Tooltip>
        ) : (
          <Tooltip>
            <TooltipTrigger asChild>
              <NavLink to="/" aria-label="RDev" className={cn(RAIL_BUTTON_CLASS, "mb-2 text-foreground")}>
                <Icon name="Terminal" className="size-4.5" />
              </NavLink>
            </TooltipTrigger>
            <TooltipContent side="right">RDev</TooltipContent>
          </Tooltip>
        )}
        {NAV_ITEMS.map((item) => (
          <RailLink key={item.to} item={item} label={t(item.key)} />
        ))}
        <RailSettings />
      </nav>

      {/* Mobile (<md): rail hidden; pages open this left drawer from their
       * own in-flow header trigger (bb mobile sidebar: min(86vw,360px),
       * backdrop, closes on pick). */}
      <DialogPrimitive.Root open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-background/60 data-[state=open]:animate-in data-[state=closed]:animate-out fade-in-0 fade-out-0 duration-200 motion-reduce:animate-none md:hidden" />
          <DialogPrimitive.Content
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              if (navTrigger.current?.isConnected) navTrigger.current.focus();
            }}
            className="fixed inset-y-0 left-0 z-50 flex w-[min(86vw,360px)] overflow-y-auto border-r border-border-seam bg-sidebar pb-[env(safe-area-inset-bottom)] pt-[env(safe-area-inset-top)] outline-none data-[state=open]:animate-in data-[state=open]:slide-in-from-left data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left duration-[220ms] ease-[cubic-bezier(0.32,0.72,0,1)] motion-reduce:animate-none md:hidden"
          >
            <DialogPrimitive.Title className="sr-only">{t("nav.label")}</DialogPrimitive.Title>
        {drawerSection ? (
          /* bb mobile: the drawer IS the whole sidebar — the nav rail
           * column on the left, the page's own sidebar filling the rest. */
          <>
            <nav
              aria-label={t("nav.label")}
              className="flex w-11 shrink-0 flex-col items-center gap-0.5 py-2"
            >
              {NAV_ITEMS.map((item) => (
                <DrawerRailLink key={item.to} item={item} />
              ))}
              <RailFooterButtons className="mt-auto" />
            </nav>
            <div className="flex min-w-0 flex-1 flex-col border-l border-border-seam">
              {drawerSection}
            </div>
          </>
        ) : (
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex h-12 shrink-0 items-center gap-2 border-b border-border-seam px-3">
              <Icon name="Terminal" className="size-4.5 text-foreground" />
              <span className="flex-1 text-sm font-medium">RDev</span>
              <button
                type="button"
                aria-label={t("common.close")}
                onClick={() => setMobileNavOpen(false)}
                className={cn("flex cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground", COARSE_POINTER_HEADER_ICON_BUTTON_CLASS)}
              >
                <Icon name="X" className="size-4" />
              </button>
            </div>
            <nav className="flex shrink-0 flex-col gap-0.5 p-2">
              <MobileNavList />
            </nav>
        <div className="mt-auto flex flex-col gap-0.5 border-t border-border-seam p-2">
          <label className="flex items-center justify-between gap-2 px-2 py-2 text-sm text-muted-foreground">
            {t("theme.palette")}
            <select aria-label={t("theme.palette")} value={preset} onChange={(event) => setPreset(event.target.value as ThemePreset)} className="h-10 rounded-md border border-input bg-background px-2 text-foreground">
              {(Object.keys(themePresets) as ThemePreset[]).map((id) => <option key={id} value={id}>{themePresets[id].label}</option>)}
            </select>
          </label>
          <a
            href="https://github.com/icepie/rdev"
            target="_blank"
            rel="noreferrer"
            className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
          >
            <Icon name="Code" className="size-4" />
            GitHub
          </a>
          <div className="flex items-center gap-2 px-1 pt-1">
            <div className="flex flex-1 items-center gap-0.5 rounded-lg border border-border-seam p-0.5">
              {THEME_CHOICES.map((c) => (
                <button
                  key={c.value}
                  type="button"
                  aria-label={t(c.key)}
                  aria-pressed={choice === c.value}
                  title={t(c.key)}
                  onClick={() => setChoice(c.value)}
                  className={cn(
                    "flex h-7 flex-1 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors",
                    choice === c.value
                      ? "bg-state-active text-foreground"
                      : "hover:text-foreground",
                  )}
                >
                  <Icon
                    name={
                      c.value === "light"
                        ? "Sun"
                        : c.value === "dark"
                          ? "Moon"
                          : "Monitor"
                    }
                    className="size-3.5"
                  />
                </button>
              ))}
            </div>
            <div className="flex items-center gap-0.5 rounded-lg border border-border-seam p-0.5">
              {(["zh", "en"] as const).map((l) => (
                <button
                  key={l}
                  type="button"
                  aria-pressed={lang === l}
                  onClick={() => setLang(l)}
                  className={cn(
                    "flex h-7 cursor-pointer items-center rounded-md px-2 text-xs text-muted-foreground transition-colors",
                    lang === l
                      ? "bg-state-active text-foreground"
                      : "hover:text-foreground",
                  )}
                >
                  {l === "zh" ? "中文" : "EN"}
                </button>
              ))}
            </div>
          </div>
        </div>
          </div>
        )}
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>

      <main className="min-w-0 flex-1 overflow-auto">
        <MobileNavContext.Provider
          value={{
            open: (section) => {
              navTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
              setDrawerSection(section ?? null);
              setMobileNavOpen(true);
            },
            close: () => setMobileNavOpen(false),
          }}
        >
          <Suspense fallback={<div role="status" className="flex h-full min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground"><Icon name="Spinner" className="size-4 animate-spin motion-reduce:animate-none" />{t("common.loading")}</div>}>
            <Outlet />
          </Suspense>
        </MobileNavContext.Provider>
      </main>
    </div>
  );
}
