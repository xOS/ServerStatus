# ServerStatus Frontend

当前前端是原生 TypeScript + Vite 实现，不依赖 Vue/React 等运行时框架。构建产物输出到 `frontend/dist`，后端会从该目录托管 SPA 页面、`/assets` 静态资源和 `favicon.svg`。

## 命令

```bash
npm ci
npm run dev
npm run build
npm run preview
```

说明：

- `npm run dev` 仅用于本地开发。
- 生产部署使用 `npm run build`，生成 `frontend/dist`。
- 后端运行目录中缺少 `frontend/dist/index.html` 时，会显示“新版前端尚未构建”的占位页面。

## 路由

前端由 `frontend/src/main.ts` 中的轻量路由器处理：

- `/`：首页状态卡片。
- `/network`：网络监控页。
- `/dashboard` 及其子路径：后台管理。
- `/login`：登录页。
- 其他路径回落到首页。

后端 `NoRoute` 会对非 API、非静态资源的 GET 请求返回 `frontend/dist/index.html`，用于支持 SPA 刷新。

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
- 设置：站点、登录、跨域、端口等配置。

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

`frontend/vercel.json` 负责将 `/dashboard`、`/network` 等 SPA 路由回落到 `index.html`。

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

Cloudflare Pages 在没有顶层 `404.html` 时可正常处理 Vite SPA 路由。

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
- 前端构建完成后，由后端统一托管 `frontend/dist`；Docker 镜像构建也依赖该目录。
