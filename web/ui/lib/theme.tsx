import {
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { parseThemePreset, themePresets, type ThemePreset } from "./themes";

export type ThemeChoice = "auto" | "light" | "dark";
const STORAGE_KEY = "rdevTheme";
const PRESET_KEY = "rdevThemePreset";
const DARK_QUERY = "(prefers-color-scheme: dark)";
const parseChoice = (value: string | null): ThemeChoice =>
  value === "light" || value === "dark" ? value : "auto";
function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function store(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* Memory-only in private/restricted contexts. */
  }
}
interface ThemeCtx {
  choice: ThemeChoice;
  resolved: "light" | "dark";
  preset: ThemePreset;
  setChoice: (choice: ThemeChoice) => void;
  setPreset: (preset: ThemePreset) => void;
}
const Ctx = createContext<ThemeCtx>({
  choice: "auto",
  resolved: "light",
  preset: "default",
  setChoice: () => {},
  setPreset: () => {},
});

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoiceState] = useState(() =>
    parseChoice(readStored(STORAGE_KEY)),
  );
  const [preset, setPresetState] = useState(() =>
    parseThemePreset(readStored(PRESET_KEY)),
  );
  const [systemDark, setSystemDark] = useState(
    () => window.matchMedia(DARK_QUERY).matches,
  );
  const resolved = choice === "auto" ? (systemDark ? "dark" : "light") : choice;
  const setChoice = useCallback((value: ThemeChoice) => {
    store(STORAGE_KEY, value);
    setChoiceState(value);
  }, []);
  const setPreset = useCallback((value: ThemePreset) => {
    store(PRESET_KEY, value);
    setPresetState(value);
  }, []);
  useEffect(() => {
    const mq = window.matchMedia(DARK_QUERY);
    const update = () => setSystemDark(mq.matches);
    update();
    mq.addEventListener("change", update);
    const sync = (event: StorageEvent) => {
      if (event.key === STORAGE_KEY || event.key === null)
        setChoiceState(parseChoice(readStored(STORAGE_KEY)));
      if (event.key === PRESET_KEY || event.key === null)
        setPresetState(parseThemePreset(readStored(PRESET_KEY)));
    };
    window.addEventListener("storage", sync);
    return () => {
      mq.removeEventListener("change", update);
      window.removeEventListener("storage", sync);
    };
  }, []);
  useLayoutEffect(() => {
    const root = document.documentElement;
    root.classList.toggle("dark", resolved === "dark");
    root.classList.toggle("light", resolved === "light");
    let style = document.getElementById("rdev-app-theme");
    if (!style) {
      style = document.createElement("style");
      style.id = "rdev-app-theme";
      document.head.appendChild(style);
    }
    style.textContent = themePresets[preset].css;
    root.dataset.themePreset = preset;
    let meta = document.querySelector<HTMLMetaElement>(
      'meta[name="theme-color"]',
    );
    if (!meta) {
      meta = document.createElement("meta");
      meta.name = "theme-color";
      document.head.appendChild(meta);
    }
    meta.content = getComputedStyle(document.body).backgroundColor;
  }, [resolved, preset]);
  return (
    <Ctx.Provider value={{ choice, resolved, preset, setChoice, setPreset }}>
      {children}
    </Ctx.Provider>
  );
}
export function useTheme(): ThemeCtx {
  return useContext(Ctx);
}
