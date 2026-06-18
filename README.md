# 🔐 DotSync

> *End-to-end encrypted secret sync for dev teams — production-grade, self-hostable.*

**DotSync** syncs `.env` secrets across your team with client-side AES-256-GCM encryption. The server stores only encrypted blobs — it **never** sees your raw secret values.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Version](https://img.shields.io/badge/version-v0.1.0--beta-yellow.svg)
![Stack](https://img.shields.io/badge/stack-Go%20%7C%20PostgreSQL%20%7C%20Chi-00ADD8.svg)
![Go Version](https://img.shields.io/badge/go-%3E%3D1.24-blue.svg)

---

## Table of Contents

- [Quick Start (User)](#-quick-start-user)
- [Architecture](#-architecture)
- [Security Model](#-security-model)
- [Self-Hosting (Render)](#-self-hosting-render)
  - [Step 1 — GitHub OAuth App](#step-1--create-github-oauth-app)
  - [Step 2 — Deploy on Render](#step-2--deploy-on-render)
  - [Step 3 — Set Environment Variables](#step-3--set-environment-variables)
  - [Step 4 — Stripe Setup](#step-4--stripe-setup-optional)
- [CLI Reference](#-cli-reference)
- [API Reference](#-api-reference)
- [Development Setup](#-local-development-setup)
- [Project Structure](#-project-structure)

---

## ⚡ Quick Start (User)

```bash
# Install the CLI
go install github.com/Pruthviraj36/dotsync/cli/dotsync@latest

# Or build from source
git clone https://github.com/Pruthviraj36/dotsync
cd dotsync && go build -o dotsync ./cli/dotsync && mv dotsync /usr/local/bin/

# Login with GitHub
dotsync login

# In your project directory
dotsync init          # Link folder to a DotSync project
dotsync push          # Encrypt and upload your .env
dotsync pull          # Download and decrypt to .env
dotsync diff          # See what changed vs remote
dotsync history       # Version history
```

---

## 🏗 Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI (Go + Cobra)                         │
│                                                                 │
│  .env file → AES-256-GCM encrypt → HMAC sign → HTTPS → Server  │
│  Server → HTTPS → AES-256-GCM decrypt → .env file              │
│                                                                 │
│  ⚠️  Encryption/Decryption happens ONLY on your machine         │
└─────────────────────────────────────────────────────────────────┘
                              │ HTTPS
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Server (Go + Chi)                            │
│                                                                 │
│  Auth: GitHub OAuth → JWT (15min) + Refresh Tokens (30d)       │
│  Rate limiting: per IP + per user                               │
│  HMAC verification on all CLI requests                          │
│  Refresh token rotation (replay attack detection)               │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                       PostgreSQL                                │
│                                                                 │
│  users, projects, environments, team_members                    │
│  secrets (encrypted_data BYTEA, data_nonce BYTEA)              │
│  refresh_tokens (token_hash only — never raw)                   │
│  audit_logs (immutable, append-only)                            │
└─────────────────────────────────────────────────────────────────┘
```

---

## 🔒 Security Model

### Zero-Knowledge Architecture

| What the server stores | What the server NEVER sees |
|---|---|
| Encrypted bytes (BYTEA) | Raw secret values |
| GCM nonce | Encryption key |
| Username, email (from GitHub) | Your actual `.env` content |
| Audit logs (who pushed, when) | What the secrets are |

### Encryption Flow

```
Your .env content
       ↓
PBKDF2-SHA256 key derivation
  input:  access_token + project_slug
  output: 256-bit AES key (100,000 iterations)
       ↓
AES-256-GCM encryption
  - Unique 12-byte random nonce per push (never reused)
  - Authenticated encryption (detects tampering)
       ↓
Encrypted blob + nonce → sent to server over TLS
```

### Authentication Security

- **JWT access tokens** — 15-minute expiry, HS256 signed
- **Refresh tokens** — stored as SHA-256 hashes only (never raw)
- **Refresh token rotation** — every refresh issues a new pair; old token revoked immediately
- **Replay attack detection** — if a revoked refresh token is used, ALL sessions for that user are invalidated
- **HMAC request signing** — every CLI request is signed with HMAC-SHA256; server verifies before processing

### Rate Limiting

| Scope | Limit |
|---|---|
| Global (per IP) | 200 req/min |
| Auth endpoints (per IP) | 20 req/min |
| Authenticated users | 300 req/min |
| Push/Pull endpoints | 100 req/min per user |

---

## 🚀 Self-Hosting (Render)

### Step 1 — Create GitHub OAuth App

1. Go to [github.com/settings/developers](https://github.com/settings/developers)
2. Click **OAuth Apps** → **New OAuth App**
3. Fill in:
   - **Application name**: `DotSync`
   - **Homepage URL**: `https://your-app.onrender.com` (update after deploy)
   - **Authorization callback URL**: `https://your-app.onrender.com/api/auth/github/callback`
4. Click **Register application**
5. Click **Generate a new client secret**
6. Save **Client ID** and **Client secret** — you'll need them in Step 3

> 💡 For local dev, create a second OAuth app with callback `http://localhost:8080/api/auth/github/callback`

---

### Step 2 — Deploy on Render

1. **Fork or clone this repo** to your GitHub account
2. Go to [render.com](https://render.com) → **New Project**
3. Click **Deploy from GitHub repo** → Select your forked repo
4. Render will auto-detect the `render.yaml` in your repo root
5. Add a **PostgreSQL** plugin:
   - In your Render project, the `render.yaml` provisions a PostgreSQL database automatically
   - Render injects `DATABASE_URL` from the linked database — done

---

### Step 3 — Set Environment Variables

In Render → your service → **Environment**, add:

```
PORT                   = 10000        (Render sets this automatically)
DATABASE_URL           = (auto-injected by Render from linked database)
FRONTEND_URL           = https://your-frontend.vercel.app
JWT_SECRET             = (generate: openssl rand -hex 64)
GITHUB_CLIENT_ID       = (from Step 1)
GITHUB_CLIENT_SECRET   = (from Step 1)
STRIPE_SECRET_KEY      = sk_live_... (or sk_test_... for testing)
STRIPE_WEBHOOK_SECRET  = whsec_...   (from Step 4)
MIGRATIONS_PATH        = ./migrations
```

**Generate JWT_SECRET:**
```bash
openssl rand -hex 64
```

> ⚠️ Never commit real values. Use Render's Environment UI or the `render.yaml` `sync: false` fields.

---

### Step 4 — Stripe Setup (Optional — for paid plans)

1. Go to [dashboard.stripe.com](https://dashboard.stripe.com)
2. Create **3 Products** with monthly prices:
   - **DotSync Pro** → $5/mo → note the Price ID (`price_...`)
   - **DotSync Team** → $15/mo → note the Price ID
   - **DotSync Business** → $49/mo → note the Price ID
3. In `internal/stripe/stripe.go`, update `PriceIDToPlan`:
   ```go
   var PriceIDToPlan = map[string]string{
       "price_ABC123": "pro",
       "price_DEF456": "team",
       "price_GHI789": "business",
   }
   ```
4. Add a **Webhook** at [dashboard.stripe.com/webhooks](https://dashboard.stripe.com/webhooks):
   - Endpoint URL: `https://your-app.onrender.com/api/stripe/webhook`
   - Events to listen for:
     - `customer.subscription.created`
     - `customer.subscription.updated`
     - `customer.subscription.deleted`
     - `invoice.payment_failed`
5. Copy the **Signing secret** → set as `STRIPE_WEBHOOK_SECRET` in Railway

---

### Step 5 — Point CLI to Your Server

Users set the server URL via environment variable:

```bash
export DOTSYNC_SERVER=https://your-app.onrender.com
dotsync login
```

Or update the default in `cli/config/config.go`:
```go
return "https://your-app.onrender.com"
```

---

## 💻 CLI Reference

```bash
dotsync login                        # Authenticate via GitHub OAuth
dotsync logout                       # Revoke all sessions + clear credentials

dotsync init                         # Link current folder to a project
dotsync init --new                   # Create a new project and link

dotsync push                         # Encrypt + upload .env (default env)
dotsync push --env production        # Push to specific environment
dotsync push --file .env.prod --env production  # Push a different file

dotsync pull                         # Download + decrypt latest .env
dotsync pull --env staging           # Pull specific environment
dotsync pull --output .env.staging   # Write to specific file
dotsync pull --force                 # Skip overwrite confirmation

dotsync diff                         # Show key differences (not values)
dotsync diff --env production        # Diff against production

dotsync history                      # Version history (within plan limit)
dotsync history --env staging

dotsync envs                         # List environments
dotsync status                       # Show login + project status
```

---

## 📡 API Reference

### Auth

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/auth/github` | Exchange GitHub OAuth code for tokens |
| `POST` | `/api/auth/refresh` | Rotate refresh token |
| `POST` | `/api/auth/logout` | Revoke all sessions |
| `GET`  | `/api/auth/me` | Current user info |

### Projects

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/projects` | Create project |
| `GET`  | `/api/projects` | List your projects |

### Secrets

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/projects/:slug/envs/:env/push` | Upload encrypted secrets |
| `GET`  | `/api/projects/:slug/envs/:env/pull` | Download latest encrypted blob |
| `GET`  | `/api/projects/:slug/envs/:env/history` | Version history |

### Stripe

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/stripe/webhook` | Stripe event webhook |

### Health

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Health check |

**Request headers (CLI):**
```
Authorization: Bearer <access_token>
Content-Type: application/json
X-DotSync-Signature: <hmac-sha256 of body>
```

---

## 🛠 Local Development Setup

### Prerequisites

- Go 1.24+
- PostgreSQL 14+ (or Docker)
- A GitHub OAuth App (callback: `http://localhost:8080/api/auth/github/callback`)

### 1. Clone and configure

```bash
git clone https://github.com/Pruthviraj36/dotsync
cd dotsync

cp .env.example .env
# Edit .env with your values
```

### 2. Start PostgreSQL

```bash
# Using Docker:
docker run -d \
  --name dotsync-db \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=dotsync \
  -p 5432:5432 \
  postgres:16-alpine

# Or if you have psql locally:
createdb dotsync
```

### 3. Run the server

```bash
go run ./cmd/dotsync
# → Server on :8080, migrations run automatically
```

### 4. Build the CLI

```bash
go build -o dotsync ./cli/dotsync
./dotsync --help
```

### 5. Run tests

```bash
go test ./...
```

### 6. Build all release binaries

```bash
bash scripts/build.sh
# Outputs to dist/
```

---

## 📁 Project Structure

```
dotsync/
├── cmd/dotsync/          # Server entrypoint (main.go)
├── cli/
│   ├── main.go           # CLI entrypoint
│   ├── cmd/              # All CLI commands (login, push, pull, etc.)
│   ├── api/              # HTTP client with HMAC signing + auto-refresh
│   ├── config/           # ~/.dotsync/config.json + .dotsync.json
│   └── crypto/           # Client-side encrypt/decrypt + .env parsing
├── internal/
│   ├── auth/             # JWT, refresh tokens, GitHub OAuth
│   ├── crypto/           # AES-256-GCM, PBKDF2, HMAC, key derivation
│   ├── db/               # Database connection + migration runner
│   ├── handler/          # HTTP handlers (auth, projects, secrets)
│   ├── middleware/        # JWT auth, HMAC verify, rate limiting, security headers
│   ├── model/            # Data models + plan limits
│   ├── service/          # Business logic (secrets, projects, teams, audit)
│   └── stripe/           # Stripe webhook handler + subscription sync
├── migrations/           # SQL migration files (up/down)
├── scripts/build.sh      # Multi-platform build script
├── render.yaml            # Render deploy config
├── ├── .env.example          # Environment variable template
└── go.mod
```

---

## 💰 Pricing Plans

| Plan | Price | Projects | Team Members | History | Leak Detection |
|---|---|---|---|---|---|
| **Free** | $0/mo | 1 | 3 | 7 days | ❌ |
| **Pro** | $5/mo | Unlimited | 5 | 30 days | ✅ |
| **Team** | $15/mo | Unlimited | 10 | 90 days | ✅ |
| **Business** | $49/mo | Unlimited | Unlimited | 1 year | ✅ + Audit Logs |

---

## 🔐 Security Checklist (Production)

Before going live:

- [ ] `JWT_SECRET` is 64+ random bytes (`openssl rand -hex 64`)
- [ ] `GITHUB_CLIENT_SECRET` is set and not committed
- [ ] `STRIPE_WEBHOOK_SECRET` is set
- [ ] `DATABASE_URL` uses SSL (`?sslmode=require`)
- [ ] `FRONTEND_URL` is set to your exact frontend domain (CORS)
- [ ] Render service is not publicly exposing the DB
- [ ] Stripe webhook only listens to needed events
- [ ] `.env` is in `.gitignore` ✅ (dotsync init does this automatically)

---

## 📄 License

MIT — free to use, modify, and distribute.

---

> Built with Go, secured with AES-256-GCM, deployed on Render.
