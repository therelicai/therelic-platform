# Domain Architecture

## Subdomains

| Domain | Purpose | Hosting | Notes |
|---|---|---|---|
| `therelic.dev` | Marketing website | Cloudflare Pages / Vercel | Static site (Astro) |
| `app.therelic.dev` | Platform dashboard | Cloudflare Pages | React SPA (Vite build) |
| `api.therelic.dev` | Control plane API | Fly.io | Go service |
| `docs.therelic.dev` | Documentation | Part of website or Mintlify | Optional subdomain |

## DNS Records

Configure these in Cloudflare (or your DNS provider):

```
# API (Fly.io)
api.therelic.dev    CNAME   therelic-api.fly.dev

# Website (Cloudflare Pages)
therelic.dev        CNAME   therelic-website.pages.dev
www.therelic.dev    CNAME   therelic-website.pages.dev

# App (Cloudflare Pages)
app.therelic.dev    CNAME   therelic-app.pages.dev
```

## Cloudflare Pages Setup

### Marketing Website (`therelic.dev`)

```bash
cd therelic-website
npx wrangler pages project create therelic-website
npx wrangler pages deploy dist
```

Custom domain: Add `therelic.dev` in Cloudflare Pages > Custom Domains.

### Platform App (`app.therelic.dev`)

```bash
cd therelic-app
npm run build
npx wrangler pages project create therelic-app
npx wrangler pages deploy dist
```

Custom domain: Add `app.therelic.dev` in Cloudflare Pages > Custom Domains.

## Fly.io Setup for API

```bash
cd therelic-platform
fly launch --name therelic-api --region iad
fly secrets set DATABASE_URL="postgresql://..." SUPABASE_JWT_SECRET="..."
fly deploy
```

Custom domain:

```bash
fly certs add api.therelic.dev
```

Then add the CNAME record as shown above.

## CORS Configuration

The Go API is configured to allow requests from:
- `https://app.therelic.dev`
- `http://localhost:5173` (local dev)
- `http://localhost:5174` (local dev alt port)

See `internal/api/server.go` for the CORS middleware configuration.

## Supabase Auth Configuration

In the Supabase dashboard (Authentication > URL Configuration):
- Site URL: `https://app.therelic.dev`
- Redirect URLs: `https://app.therelic.dev/**`, `http://localhost:5173/**`, `http://localhost:5174/**`
