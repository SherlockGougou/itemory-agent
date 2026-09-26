import type { Config } from "tailwindcss";

/**
 * 控制台的设计 token 在 globals.css 里以 CSS 变量定义（浅色 / 深色两档），
 * 这里只做变量到 Tailwind 语义类名的映射，避免在组件里散落 var(--…)。
 *
 * 与 Itemory App 的关系：颜色取自 App 的同一套色板，
 * 但圆角是**网页端独立的一档**（卡片 12 / 按钮 8，而 App 是 20 / 14）。
 * 理由是输入设备与信息密度不同——鼠标目标比手指小，控制台也比相册更密。
 * 这个分叉是有意为之，不要在评审时当成漂移来"修"。
 */
const config: Config = {
  darkMode: "class",
  content: [
    "./src/pages/**/*.{js,ts,jsx,tsx,mdx}",
    "./src/components/**/*.{js,ts,jsx,tsx,mdx}",
    "./src/app/**/*.{js,ts,jsx,tsx,mdx}",
  ],
  theme: {
    extend: {
      colors: {
        canvas: "var(--canvas)",
        rail: "var(--rail)",
        surface: "var(--surface)",
        raise: "var(--surface-2)",
        line: "var(--line)",
        sep: "var(--sep)",
        t1: "var(--t1)",
        t2: "var(--t2)",
        t3: "var(--t3)",
        brand: "var(--brand)",
        tech: "var(--tech)",
        ok: "var(--ok)",
        warn: "var(--warn)",
        danger: "var(--danger)",
      },
      borderColor: {
        DEFAULT: "var(--line)",
      },
      borderRadius: {
        card: "12px",
        ctl: "8px",
      },
      fontFamily: {
        sans: [
          "-apple-system",
          "BlinkMacSystemFont",
          "SF Pro Text",
          "PingFang SC",
          "Hiragino Sans GB",
          "Microsoft YaHei",
          "sans-serif",
        ],
        mono: [
          "ui-monospace",
          "SF Mono",
          "IBM Plex Mono",
          "Menlo",
          "Consolas",
          "monospace",
        ],
      },
    },
  },
  plugins: [],
};
export default config;
