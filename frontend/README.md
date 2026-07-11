# ServerStatus Frontend

当前前端是原生 TypeScript + Vite 实现，不依赖 Vue/React 等运行时框架。构建产物默认输出到 `frontend/dist`，后端通过 `frontend.dist` 配置项决定托管哪个前端 dist 目录。

## 命令

```bash
npm ci
npm run dev
npm run build
npm run preview
```

说明：

- `npm run dev` 仅用于本地开发。
- 生产部署使用 `npm run build`，默认生成 `frontend/dist`。
- 后端运行目录中缺少 `frontend.dist` 指向目录下的 `index.html` 时，会显示“新版前端尚未构建”的占位页面。
- 前后端完全分离部署时，可以把 `frontend.dist` 明确配置为空值，此时后端不挂载本地前端资源。

## 路由

前端由 `frontend/src/main.ts` 中的轻量路由器处理：

- `/`：首页状态卡片。
- `/network`：网络监控页。
- `/dashboard` 及其子路径：后台管理。
- `/login`：登录页。
- 其他路径回落到首页。

后端 `NoRoute` 会对非 API、非静态资源的 GET 请求返回 `frontend.dist` 指向目录下的 `index.html`，用于支持 SPA 刷新。

## API Base

默认 API Base 为同源 `/api/v1`。

独立部署前端时，构建前设置：

```bash
VITE_SERVERSTATUS_API_BASE=https://status.example.com/api/v1 npm run build
```

也可以在页面运行时通过 `window.SERVERSTATUS_API_BASE` 覆盖，但常规部署建议使用构建环境变量。

WebSocket 地址由 API Base 自动推导：

- `http` -> `ws`
- `https` -> `wss`
- 默认路径：`/api/v1/ws`

## 状态卡片

首页状态卡片使用固定的进度条视觉体系，保持现有颜色方案和圆角形态。

资源类进度：

- CPU、内存、硬盘均按 `0-100%` 显示。
- 条宽也限制在 `0-100%`。
- 服务器离线时进度条使用离线态。

流量进度：

- 百分比文字显示真实用量比例，允许超过 `100%`。
- 条宽只渲染到 `100%`，避免溢出。
- 文本位置按整条进度槽计算，确保 `121.23%` 等长文本不会超出卡片右边界。
- 当前前端优先使用字节数计算真实比例，避免后端 `percent` 已被截断。

支持的流量字段：

- 数组 payload：`server_id`、`used_bytes`、`max_bytes`、`total_bytes`、`used_formatted`、`max_formatted`、`used`、`max`、`total`、`used_percent`、`percent`、`cycle_name`
- 以服务器 ID 为 key 的对象 payload：`used`、`max`、`total`、`used_bytes`、`max_bytes`、`total_bytes`、`used_percent`、`percent`、`serverName`、`cycleName`

## 后台页面

后台管理入口为 `/dashboard`，主要模块：

- 仪表盘：概览统计。
- 服务器：服务器管理、密钥、安装命令、分组、批量操作。
- 监控：ICMP/TCP 等监控配置。
- 计划任务：定时任务与手动触发。
- 报警规则：资源、离线、流量等规则。
- 通知：通知方式和分组。
- NAT：内网映射。
- DDNS：解析配置。
- API：Token 管理。
- 设置：站点、Logo、登录、跨域、端口等配置。

## 独立部署

### Vercel

项目设置：

```text
Root Directory: frontend
Framework Preset: Vite
Install Command: npm ci
Build Command: npm run build
Output Directory: dist
```

环境变量：

```text
VITE_SERVERSTATUS_API_BASE=https://status.example.com/api/v1
```

`frontend/vercel.json` 只将 `/dashboard`、`/network`、`/login` 和资源恢复入口回落到 `index.html`。不要改成全路径回落：缺失的旧哈希 JS/CSS 必须返回 404，返回 `index.html` 会让浏览器因模块 MIME 不匹配而白屏。

缓存策略由响应头控制，不依赖 `index.html` 中的 meta 标签：

- 页面 HTML 使用 `no-store`，每次打开都获取当前构建入口。
- `/assets/` 下带内容哈希的 JS/CSS 使用一年 `immutable` 缓存。
- `/static/` 和 `favicon.svg` 每次重新验证，避免固定 URL 的 Logo 长期保留旧内容。
- 入口资源加载失败时会访问一次唯一的 `/_asset-recovery/<timestamp>` 路径，成功后恢复原 URL；CDN 不得归一化或缓存该路径。

### Cloudflare Pages

项目设置：

```text
Root directory: frontend
Framework preset: Vite
Build command: npm run build
Build output directory: dist
```

环境变量：

```text
VITE_SERVERSTATUS_API_BASE=https://status.example.com/api/v1
```

`public-lite/_redirects`、`_headers` 和 `404.html` 会随构建复制到 `dist`：页面路由显式回落到 `index.html`，缺失资源保持 404，并应用与 Vercel 相同的缓存边界。不要删除 `404.html` 或增加 `/* /index.html 200` 全局回落规则。

## 后端 CORS / WebSocket Origin

前后端分离时，后端必须允许前端 Origin：

```yaml
security:
  allowedorigins: "https://ops.example.com,*.vercel.app,https://example.pages.dev"
```

规则：

- 完整 Origin 匹配协议和域名。
- Hostname 简写只按 hostname 匹配。
- 支持 `*.example.com` 通配子域。
- 同一配置用于 HTTP API CORS 和 `/api/v1/ws` WebSocket Origin 检查。

## 开发注意事项

- 不要在生产构建中依赖 Vite dev server。
- 修改 `frontend/src/style.css` 时保持现有颜色方案，特别是首页进度条状态色。
- 固定格式控件要设置稳定尺寸，避免卡片内容跳动。
- 状态卡片中的百分比文本必须始终限制在进度槽内。
- 前端构建完成后，后端按 `frontend.dist` 配置托管构建产物；Docker 镜像默认仍复制 `frontend/dist`。
