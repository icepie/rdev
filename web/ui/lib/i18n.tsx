import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { dictionaries, type Lang, type I18nKey } from "./dictionaries";

const STORAGE_KEY = "rdevLang";

type Vars = Record<string, string | number>;

function format(template: string, vars?: Vars): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (m, name) =>
    vars[name] !== undefined ? String(vars[name]) : m,
  );
}

interface I18nCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (key: I18nKey | string, vars?: Vars) => string;
}

const Ctx = createContext<I18nCtx>({
  lang: "en",
  setLang: () => {},
  t: (k) => k,
});

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(() => {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === "zh" || saved === "en") return saved;
    return (navigator.language || "en").toLowerCase().startsWith("zh")
      ? "zh"
      : "en";
  });

  const setLang = useCallback((l: Lang) => {
    localStorage.setItem(STORAGE_KEY, l);
    setLangState(l);
  }, []);

  useEffect(() => {
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  }, [lang]);

  const t = useCallback(
    (key: string, vars?: Vars): string => {
      const dict = dictionaries[lang] as Record<string, string>;
      const en = dictionaries.en as Record<string, string>;
      return format(dict[key] || en[key] || key, vars);
    },
    [lang],
  );

  return <Ctx.Provider value={{ lang, setLang, t }}>{children}</Ctx.Provider>;
}

export function useI18n(): I18nCtx {
  return useContext(Ctx);
}

/** Standalone translator for non-React spots (document.title, xterm). */
export function createTranslator(lang: Lang) {
  return (key: string, vars?: Vars): string => {
    const dict = dictionaries[lang] as Record<string, string>;
    const en = dictionaries.en as Record<string, string>;
    return format(dict[key] || en[key] || key, vars);
  };
}
