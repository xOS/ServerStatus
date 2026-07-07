# ServerStatus Frontend

Lightweight native TypeScript frontend for ServerStatus.

The runtime intentionally avoids framework dependencies. Vite is used for development, building, and proxying the backend during local testing.

```sh
npm run dev
npm run build
```

## Deploy

Set `VITE_SERVERSTATUS_API_BASE` to the backend API endpoint when the frontend is deployed separately. The backend port is the `httpport` value in `config/config.yaml`.

```sh
VITE_SERVERSTATUS_API_BASE=https://SERVER_IP:HTTP_PORT/api/v1
```

For the default `config/config.yaml` value `httpport: 1001`, the API base is:

```sh
VITE_SERVERSTATUS_API_BASE=https://SERVER_IP:1001/api/v1
```

Use `http://SERVER_IP:1001/api/v1` only when the frontend is also served over HTTP. Vercel and Cloudflare Pages serve the frontend over HTTPS, so the backend endpoint must also be HTTPS, otherwise browsers will block the request as mixed content.

Add the deployed frontend origin or hostname to the backend config, then restart the backend:

```yaml
security:
  allowedorigins: "https://ops.aapls.com"
```

Both full Origin and hostname shorthand are supported. Full Origin matches the scheme and host exactly; hostname shorthand matches by hostname and supports wildcard subdomains:

```yaml
security:
  allowedorigins: "ops.aapls.com,*.vercel.app,https://example.pages.dev"
```

For multiple frontend origins, separate them with commas. `https://ops.aapls.com` only allows that exact Origin; `ops.aapls.com` allows the same hostname regardless of `http`/`https`; `*.vercel.app` allows Vercel preview subdomains.

The same whitelist is used for HTTP API CORS and `/api/v1/ws` WebSocket Origin checks.

### Vercel

Use these project settings:

```text
Root Directory: frontend
Framework Preset: Vite
Install Command: npm ci
Build Command: npm run build
Output Directory: dist
```

`vercel.json` handles history fallback for frontend routes such as `/dashboard` and `/network`.

### Cloudflare Pages

Use these project settings:

```text
Root directory: frontend
Framework preset: Vite
Build command: npm run build
Build output directory: dist
```

Cloudflare Pages serves Vite SPA routes by default when no top-level `404.html` is present.
