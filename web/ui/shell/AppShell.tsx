import { createContext, useEffect, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useMatch } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { useTheme, type ThemeChoice } from "../lib/theme";
import { Icon } from "../components/icon";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import { Button } from "../components/button";
import { cn } from "../lib/utils";
import { useRailControl } from "../lib/rail-control";

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
  "flex size-9 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground";

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

/* Drawer rail icon (bb: the drawer keeps the desktop rail column). */
function DrawerRailLink({ item }: { item: (typeof NAV_ITEMS)[number] }) {
  const active = useMatch({
    path: item.to,
    end: "end" in item && item.end,
  });
  return (
    <NavLink
      to={item.to}
      end={"end" in item && item.end}
      className={cn(
        "flex size-8 items-center justify-center rounded-md transition-colors",
        active
          ? "bg-state-active text-foreground"
          : "text-muted-foreground hover:bg-state-hover hover:text-foreground",
      )}
    >
      <Icon name={item.icon} className="size-4" />
    </NavLink>
  );
}

export function AppShell() {
  const { t, lang, setLang } = useI18n();
  const { choice, setChoice } = useTheme();
  const location = useLocation();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [drawerSection, setDrawerSection] = useState<ReactNode>(null);
  const railControl = useRailControl();

  /* Close the mobile drawer on navigation (bb: drawer dismisses on pick);
   * the injected page section goes with it. */
  useEffect(() => {
    setMobileNavOpen(false);
    setDrawerSection(null);
  }, [location.pathname]);

  return (
    <div className="flex h-full flex-row">
      {/* Desktop: 60px icon rail (md+ only). */}
      <nav
        aria-label={t("nav.label")}
        className="flex w-[60px] shrink-0 flex-col items-center gap-1 border-r border-border-seam bg-sidebar py-3 select-none max-md:hidden"
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
        <RailFooterButtons className="mt-auto" />
      </nav>

      {/* Mobile (<md): rail hidden; pages open this left drawer from their
       * own in-flow header trigger (bb mobile sidebar: min(86vw,360px),
       * backdrop, closes on pick). */}
      <div
        className={cn(
          "fixed inset-0 z-40 bg-background/60 transition-opacity duration-200 motion-reduce:transition-none md:hidden",
          mobileNavOpen ? "opacity-100" : "pointer-events-none opacity-0",
        )}
        onClick={() => setMobileNavOpen(false)}
      />
      <aside
        aria-label={t("nav.label")}
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex w-[min(86vw,360px)] border-r border-border-seam bg-sidebar transition-transform duration-200 ease-in-out motion-reduce:transition-none md:hidden",
          mobileNavOpen ? "translate-x-0" : "-translate-x-full",
        )}
      >
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
                className="flex size-7 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
              >
                <Icon name="X" className="size-4" />
              </button>
            </div>
            <nav className="flex shrink-0 flex-col gap-0.5 p-2">
              <MobileNavList />
            </nav>
        <div className="mt-auto flex flex-col gap-0.5 border-t border-border-seam p-2">
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
      </aside>

      <main className="min-w-0 flex-1 overflow-auto">
        <MobileNavContext.Provider
          value={{
            open: (section) => {
              setDrawerSection(section ?? null);
              setMobileNavOpen(true);
            },
            close: () => setMobileNavOpen(false),
          }}
        >
          <Outlet />
        </MobileNavContext.Provider>
      </main>
    </div>
  );
}
