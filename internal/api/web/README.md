# 管理控制台

Itemory Agent 的网页管理控制台，基于 Next.js 静态导出（`output: 'export'`）。构建产物 `out/` 由 `../web.go` 通过 `//go:embed all:web/out` 嵌入 Go 二进制，不进版本库。

```bash
npm ci
npm run dev     # 本地开发，默认 http://localhost:3000
npm run build   # 产出 out/，Go 构建前必须先执行（仓库根目录的 make web 会做这一步）
```

文案位于 `src/lib/i18n.json`，与后端 API 的交互集中在 `src/lib/api.ts`。
