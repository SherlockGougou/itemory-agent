"use client";

import React, { createContext, useContext, useEffect, useState } from "react";
import catalog from "@/lib/i18n.json";

const SUPPORTED = ["zh-Hans", "zh-Hant", "en", "ja", "ko"] as const;
type Locale = (typeof SUPPORTED)[number];
const FALLBACK: Locale = "zh-Hans";

export const LANGUAGE_NAMES: Record<Locale, string> = {
  "zh-Hans": "简体中文",
  "zh-Hant": "繁體中文",
  en: "English",
  ja: "日本語",
  ko: "한국어",
};

interface I18nContextType {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}

const I18nContext = createContext<I18nContextType>({
  locale: FALLBACK,
  setLocale: () => {},
  t: (key) => key,
});

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(FALLBACK);
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    const stored = localStorage.getItem("itemory.lang") as Locale;
    if (stored && SUPPORTED.includes(stored)) {
      setLocaleState(stored);
    } else {
      let detected: Locale = FALLBACK;
      for (const tag of navigator.languages || [navigator.language || "en"]) {
        const lower = tag.replace("_", "-").toLowerCase();
        if (lower.startsWith("zh")) {
          detected = /hant|tw|hk|mo/.test(lower) ? "zh-Hant" : "zh-Hans";
          break;
        }
        if (lower.startsWith("ja")) {
          detected = "ja";
          break;
        }
        if (lower.startsWith("ko")) {
          detected = "ko";
          break;
        }
        if (lower.startsWith("en")) {
          detected = "en";
          break;
        }
      }
      setLocaleState(detected);
    }
    setMounted(true);
  }, []);

  const setLocale = (l: Locale) => {
    setLocaleState(l);
    localStorage.setItem("itemory.lang", l);
  };

  const t = (key: string, values?: Record<string, string | number>) => {
    const strings: any = (catalog as any)[locale] || (catalog as any)[FALLBACK] || {};
    let text = strings[key] || key;
    if (values) {
      for (const [name, value] of Object.entries(values)) {
        text = text.split(`{${name}}`).join(String(value));
      }
    }
    return text;
  };

  if (!mounted) {
    // 首帧返回一个空骨架而不是 null：静态导出下整页白屏一拍很扎眼。
    // 骨架不含任何文案，因此与静态 HTML 完全一致，不会造成 hydration 不匹配。
    return <div style={{ minHeight: "100vh" }} aria-busy="true" />;
  }

  return (
    <I18nContext.Provider value={{ locale, setLocale, t }}>
      <div lang={locale}>{children}</div>
    </I18nContext.Provider>
  );
}

export function useI18n() {
  return useContext(I18nContext);
}
