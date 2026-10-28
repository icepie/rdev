import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";

export type ThemeChoice = "auto" | "light" | "dark";

const STORAGE_KEY = "rdevTheme";

function systemIsLight(): boolean {
  return window.matchMedia("(prefers-color-scheme: light)").matches;
}

function apply(choice: ThemeChoice) {
  const light = choice === "light" || (choice === "auto" && systemIsLight());
  document.documentElement.classList.toggle("dark", !light);
  document.documentElement.classList.toggle("light", light);
}

interface ThemeCtx {
  choice: ThemeChoice;
  setChoice: (c: ThemeChoice) => void;
}

const Ctx = createContext<ThemeCtx>({ choice: "auto", setChoice: () => {} });

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoiceState] = useState<ThemeChoice>(() => {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === "light" || v === "dark" ? v : "auto";
  });

  const setChoice = useCallback((c: ThemeChoice) => {
    localStorage.setItem(STORAGE_KEY, c);
    setChoiceState(c);
  }, []);

  useEffect(() => {
    apply(choice);
    if (choice !== "auto") return;
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    const onChange = () => apply("auto");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [choice]);

  return <Ctx.Provider value={{ choice, setChoice }}>{children}</Ctx.Provider>;
}

export function useTheme(): ThemeCtx {
  return useContext(Ctx);
}
