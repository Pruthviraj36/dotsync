# DotSync

Client-encrypted `.env` sync for dev teams. Secrets are encrypted on your machine before they reach the server. The server stores ciphertext plus an encrypted project password used for team-assisted key distribution; this is not a zero-knowledge design.

---

## Install

```bash
curl -fsSL https://<your-server>/install.sh | sh
export DOTSYNC_SERVER=https://<your-server>
dotsync login
```

Windows:
```powershell
irm https://<your-server>/install.ps1 | iex
$env:DOTSYNC_SERVER = "https://<your-server>"
dotsync login
```

The Windows installer puts `dotsync.exe` in `%LOCALAPPDATA%\dotsync` and adds that
folder to your **user** PATH, so `dotsync` works in the terminal that ran the
installer and in new terminals afterwards. If an already-open terminal still says
`'dotsync' is not recognized`, open a new one (or set the path yourself):

```powershell
$dir = "$env:LOCALAPPDATA\dotsync"
$userPath = [Environment]::GetEnvironmentVariable('Path','User')
if (($userPath -split ';') -notcontains $dir) {
  [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $dir), 'User')
}
```

Use a different location by setting `$env:DOTSYNC_INSTALL_DIR` before installing
(it is added to PATH too). The installer verifies the release SHA-256 before
installing anything.

---

## Quick start

```bash
dotsync login          # authenticate with GitHub
dotsync init           # link this folder to a project
dotsync push           # encrypt and upload your .env
dotsync pull           # download and decrypt latest .env
dotsync run -- npm dev # run with secrets injected (nothing hits disk)
```

That's it for most use cases.

---

## How encryption works

1. Your `.env` is encrypted with **AES-256-GCM** on your machine
2. The key is derived from your project password using **Argon2id** (64MB, 3 iterations)
3. Ciphertext is bound to its project and environment with GCM additional authenticated data (AAD), so a server cannot replay a `dev` blob into `production`
4. Only the ciphertext travels to the server
5. Every push is signed with an **Ed25519** machine key
6. Each signed version stores the Ed25519 public-key snapshot used to create it
7. JWTs are short-lived with **refresh token rotation** and replay detection

The server does not receive plaintext `.env` contents, but an operator with the server master key can decrypt stored project passwords. Protect `SERVER_MASTER_KEY` as a production secret.

Rotating the project password with `dotsync password rotate` does not re-encrypt existing versions automatically — re-push the environments you want re-encrypted (or keep using `DOTSYNC_PASSWORD` with the old password for old versions).

---

## Commands

| Command | Description |
|---------|-------------|
| `dotsync login` | Authenticate with GitHub |
| `dotsync logout` | Log out and revoke sessions |
| `dotsync init` | Link this folder to a project |
| `dotsync push` | Encrypt and upload your .env |
| `dotsync pull` | Download and decrypt latest .env |
| `dotsync run -- <cmd>` | Run any command with secrets injected (zero-disk) |
| `dotsync diff` | Compare local .env with remote |
| `dotsync history` | Version history for an environment |
| `dotsync rollback <n>` | Roll back to a previous version |
| `dotsync audit` | Full audit log — who did what and when |
| `dotsync status` | Show login, project, and sync state |
| `dotsync version` | Version, build info, and current context |
| `dotsync team add <user>` | Invite a GitHub user |
| `dotsync team remove <user>` | Remove access |
| `dotsync team role <user> <role>` | Change role |
| `dotsync team list` | List members and roles |
| `dotsync envs` | List environments |
| `dotsync integrate <platform>` | CI/CD integration (see below) |
| `dotsync tokens create` | Create a CI/CD service token |
| `dotsync tokens list` | List service tokens |
| `dotsync tokens revoke <id>` | Revoke a token |
| `dotsync password rotate` | Change the project encryption password |
| `dotsync config set-server <url>` | Point CLI at a different server |
| `dotsync ui` | Open the web dashboard at localhost:4040 |

---


## Web dashboard

```bash
dotsync ui
dotsync ui --port 4041   # custom port
dotsync ui --no-open     # don't auto-open browser
```

Opens a local web dashboard at `localhost:4040`. Secrets are decrypted on your machine, same as the CLI. The dashboard is protected by a per-launch local session token and should still only be exposed on a trusted workstation.

Features available in the UI:
- View and edit secrets with a syntax-highlighted editor
- Push and pull with one click
- Browse version history and restore any previous version
- Manage team members and roles
- Create and revoke service tokens
- View the full audit log

## Zero-disk secret injection

`dotsync run` injects secrets directly into process memory. No `.env` is written to disk.

```bash
dotsync run -- node server.js
dotsync run -- python manage.py runserver
dotsync run --env production -- ./deploy.sh
dotsync run -- docker run my-image
dotsync run -- podman run my-image
```

Running under `sudo`? Use `sudo -E` to preserve your environment:

```bash
sudo -E dotsync run -- docker run my-image
```

---

## CI/CD integrations

```bash
dotsync integrate github-actions
dotsync integrate vercel
dotsync integrate railway
dotsync integrate netlify
dotsync integrate docker      # works with podman too
dotsync integrate shell       # bash, zsh, fish
```

Each command creates a scoped service token and shows exactly two steps to complete the integration. No README-length output.

---

## Teams

Invite someone:
```bash
dotsync team add alice
dotsync team add bob --role viewer
```

They run `dotsync init` with your project slug — password is fetched automatically, no manual sharing needed.

Roles: `owner` > `admin` > `member` > `viewer`

---

## Environments

```bash
dotsync push --env staging
dotsync pull --env production
dotsync run --env staging -- npm test
```

---

## Service tokens (for CI/CD)

```bash
dotsync tokens create --env production --name github-actions
dotsync tokens list
dotsync tokens revoke <id>
```

Tokens are prefixed `dst_` and bound to a project and environment scope. They are read-only and limited to secret retrieval. Only the SHA-256 hash is stored; the raw token is shown once.

---

## Audit logs

Pushes, pulls, project-password access, and team/token changes are logged with user, timestamp, and IP.

```bash
dotsync audit
dotsync audit --env production
```

Available to project owners and admins, free of charge.

---

## Pricing

Completely free. No tiers, no per-seat pricing, no trial period.

Self-hosting is also free and open source — see below.

---

## Self-hosting

**Requirements:** Go 1.25+, PostgreSQL 14+, a GitHub OAuth App

**Environment variables:**

Required:

```bash
DATABASE_URL=postgres://user:pass@host:5432/dbname
JWT_SECRET=                          # openssl rand -hex 32 (>= 32 bytes, != SERVER_MASTER_KEY)
SERVER_MASTER_KEY=                   # openssl rand -hex 32 (never change after first use)
GITHUB_CLIENT_ID=                    # from github.com/settings/developers
```

Optional:

```bash
DATABASE_URL_DIRECT=postgres://...   # unpooled, for migrations (skip if not using PgBouncer/Neon)
PORT=8080                            # default 8080
TRUST_PROXY=false                    # true only behind a trusted reverse proxy (Render, Fly, nginx)
FRONTEND_URL=                        # unset = CORS disabled; set to allow one credentialed origin
MIGRATIONS_PATH=./migrations         # default ./migrations
DB_MAX_OPEN_CONNS=25
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME_MIN=30
RATE_LIMIT_PER_MIN=600               # global per-IP ceiling; auth routes are stricter
```

> **`TRUST_PROXY` matters for security.** When `false` (the default) the socket address is
> authoritative and `X-Forwarded-For` is ignored. Set it to `true` only when a reverse proxy
> you control always overwrites that header — otherwise clients can spoof their IP and bypass
> per-IP rate limits.

**GitHub OAuth App setup:**
- Go to github.com/settings/developers → New OAuth App
- Callback URL: `https://your-server/api/auth/github/callback`
- Copy the Client ID (you don't need the Client Secret — DotSync uses Device Flow)

**Run:**

```bash
cp .env.example .env   # fill in the required values
go run ./cmd/dotsync
```

Migrations run automatically on startup.

**Docker / any container:**

The repository ships a multi-stage `Dockerfile` that builds a static binary and runs it as
`nobody` (UID/GID 65534) from a `scratch` image:

```bash
docker build -t dotsync-server .
docker run --rm -p 8080:8080 --env-file .env dotsync-server
```

Using your own image? Keep the runtime non-root and copy the migrations directory:

```dockerfile
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags netgo -ldflags '-s -w' -o server ./cmd/dotsync

FROM alpine:3.21
RUN apk --no-cache add ca-certificates && adduser -D -u 10001 dotsync
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations
USER 10001
EXPOSE 8080
CMD ["./server"]
```

**Point CLI at your server:**

```bash
dotsync config set-server https://your-server.example.com
# or per-session:
export DOTSYNC_SERVER=https://your-server.example.com
```

---

## Releasing

Releases are cut from tags. `.github/workflows/release.yml` runs the test suite and publishes the artifacts with GoReleaser:

```bash
git tag v3.6.0
git push origin v3.6.0
```

The workflow refuses to publish when `go.mod`/`go.sum` are untidy, `go vet` or `go test` fails, or the GoReleaser config is invalid. A release must contain six archives (`dotsync-{linux,darwin}-{amd64,arm64}.tar.gz`, `dotsync-windows-{amd64,arm64}.zip`) plus `checksums.txt`. `dotsync update` and both installers verify the SHA-256 and refuse any asset without a matching checksum entry, so do not upload release assets by hand — `scripts/build.sh` is for local builds only.

---

## Security

- Client-side AES-256-GCM encryption; the server stores an encrypted project password for team-assisted distribution
- AES-256-GCM encryption + Argon2id key derivation
- AES-GCM AAD binds every ciphertext to its project + environment (and stored passwords to their project), so blobs cannot be relocated server-side
- Ed25519 push signatures with per-version signer-key snapshots
- Short-lived JWTs + refresh token rotation with replay detection (20s grace window for concurrent client refreshes)
- Service tokens are project-bound, read-only (GET/HEAD on retrieval routes), and environment-scoped
- Per-IP rate limits; `X-Forwarded-For` is only trusted when `TRUST_PROXY=true`
- Audit logging for secret access, team changes, and service-token changes
- Local dashboard requests require a per-launch session token and loopback host
- Container image runs as an unprivileged user with no writable filesystem

The project password is intentionally server-assisted for team convenience. A server operator with `SERVER_MASTER_KEY` can decrypt stored project passwords, so this implementation is encrypted-at-rest and client-encrypted, not zero-knowledge end-to-end encryption. Removing a team member does not revoke secrets they already downloaded; rotate affected upstream credentials separately.

Found a security issue? Email the maintainer directly. Do not open a public GitHub issue.

---

## License

MIT.
