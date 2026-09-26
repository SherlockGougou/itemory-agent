import type { Metadata } from "next";
import "./globals.css";
import { ThemeProvider } from "@/components/ThemeProvider";
import { I18nProvider } from "@/components/I18nProvider";
import { Shell } from "@/components/Shell";

export const metadata: Metadata = {
  title: "Itemory 增强服务",
  description: "Itemory NAS Agent 控制台",
  robots: "noindex",
  icons: {
    icon: [
      { url: "/favicon.ico", sizes: "any" },
      { url: "/icon.png", type: "image/png" },
      { url: "/icon-dark.png", type: "image/png", media: "(prefers-color-scheme: dark)" },
    ],
    apple: [
      { url: "/apple-icon.png" },
      { url: "/apple-icon-dark.png", media: "(prefers-color-scheme: dark)" },
    ],
  },
};

/**
 * 控制台外壳。Providers 提到根 layout 之后，8 条路由共用同一套 i18n / 主题 /
 * 管理员会话状态，不会再出现「某个页面忘了包 Provider」这类漂移。
 *
 * 字体改用系统栈（与 App 的规则一致：正文、按钮、导航一律用系统动态字体，
 * 随包字族只留给展示字声），因此不再引 Geist。
 */
export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="min-h-screen bg-canvas font-sans text-t1 antialiased">
        {/* 默认深色：控制台是要盯着看很久的界面，深底比白底省眼睛；
            enableSystem 保留「跟随系统」这一档。 */}
        <ThemeProvider attribute="class" defaultTheme="dark" enableSystem>
          <I18nProvider>
            <Shell>{children}</Shell>
          </I18nProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
