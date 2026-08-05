# DotSync

**Encrypted `.env` sync for dev teams. Your secrets never leave your machine in plaintext — ever.**

---

We've all been there. Someone joins the team and the first onboarding message is *"hey, check your DMs, I'm sending you the `.env` file."* Or someone commits a `.env` by accident. Or you're juggling five machines and can never remember which one has the up-to-date `DATABASE_URL`.

DotSync fixes that. One command to push. One command to pull. Everything encrypted on your machine before it touches the network.

```
$ dotsync push
success  Encrypting 12 secrets for team access (my-app/production)...
success  Pushed. Rev a3f8c2d. Server stores ciphertext only.

  Project : my-app
  Env     : production
  Version : v7
  Secrets : 12 keys encrypted

  Teammates can now run: dotsync pull
```

---

## How it works

DotSync does **client-side encryption**. That means:

1. Your `.env` is encrypted on your laptop using a key derived from your project password
2. Only the ciphertext travels to the server
3. The server stores an encrypted blob it can't read
4. Your teammates decrypt it locally using the same password

No trust required on our end. Even if the database leaked tomorrow, your secrets would be unreadable.

**The crypto stack:**

- **Argon2id** key derivation (time=3, memory=64MB) — brute-force resistant
- **AES-256-GCM** encryption — authenticated, tampered data fails loudly
- **Ed25519** push signatures — every push is signed by the pusher's machine key
- **HMAC-SHA256** request signing — every API call is signed, replay attacks don't work
- **JWT with refresh rotation** — short-lived access tokens, automatic refresh

---

## Install

```bash
curl -fsSL https://dotsync.onrender.com/install.sh | sh
```

Or download a binary directly from the [releases page](https://github.com/Pruthviraj36/dotsync/releases).

---

## Quick start

```bash
dotsync login         # Authenticate with GitHub (OAuth device flow)
dotsync init          # Link this folder to a DotSync project
dotsync push          # Encrypt and upload your .env
dotsync pull          # Download and decrypt the latest .env
```

---

## Commands

| Command | Description |
|---------|-------------|
| `dotsync login` | Authenticate with GitHub |
| `dotsync logout` | Log out and revoke sessions |
| `dotsync init` | Link this folder to a project |
| `dotsync push` | Encrypt and upload your .env |
| `dotsync pull` | Download and decrypt latest .env |
| `dotsync run` | Run a command with secrets injected (nothing hits disk) |
| `dotsync diff` | Show what changed between local and remote |
| `dotsync history` | Version history for an environment |
| `dotsync rollback` | Roll back to a previous version |
| `dotsync audit` | Full audit log (who did what and when) |
| `dotsync scan` | Scan for secrets accidentally left in source files |
| `dotsync team` | Manage project team members |
| `dotsync envs` | List environments for this project |
| `dotsync status` | Show login, project, and sync state |
| `dotsync integrate` | Generate CI/CD integration snippets |

---

## Injecting secrets without writing a file

`dotsync run` injects secrets directly into process memory. No `.env` file is ever written to disk. This is especially important in 2026, where AI coding tools (Claude Code, Cursor, Copilot) read your project directory automatically.

```bash
dotsync run -- npm run dev
dotsync run -- python manage.py runserver
dotsync run --env production -- ./deploy.sh
```

---

## CI/CD integrations

DotSync generates ready-to-use snippets for all major platforms. Run:

```bash
dotsync integrate --help
```

Supported integrations:

```bash
dotsync integrate github-actions   # GitHub Actions workflow step
dotsync integrate vercel           # Vercel environment variable sync
dotsync integrate railway          # Railway deployment
dotsync integrate netlify          # Netlify build environment
dotsync integrate docker           # Docker / Docker Compose
dotsync integrate shell            # Bash, Zsh, or Fish export
```

### GitHub Actions example

```bash
dotsync integrate github-actions --env production
```

This generates a workflow YAML step that pulls secrets at CI runtime using a scoped service token. No secrets are stored in your repo.

---

## Audit logs

Every push, pull, password rotation, and team change is logged with the user, timestamp, and IP address.

```bash
dotsync audit
dotsync audit --env production
```

Audit logs are available to all users at no cost. Only owners and admins can view them.

```
Audit Log — my-app
──────────────────────────────────────────────────────
  WHEN        WHO           ACTION   ENV          DETAIL
──────────────────────────────────────────────────────
  2m ago      @alice        push     production   v12
  1h ago      @bob          pull     staging      v11
  2026-07-28  @alice        invite   —            @charlie
──────────────────────────────────────────────────────
  3 event(s) shown
```

---

## Secret scanning

`dotsync scan` checks your source files for secrets accidentally left in plaintext — API keys, tokens, database URLs, private keys.

```bash
dotsync scan
dotsync scan --path ./src
```

It looks for common patterns: AWS keys, Stripe tokens, private keys, database connection strings, and generic high-entropy strings. Runs locally — nothing is sent to the server.

---

## Teams

```bash
dotsync team invite @alice          # Invite a GitHub user
dotsync team invite @alice --role viewer
dotsync team list                   # List all members and roles
dotsync team revoke @alice          # Remove access
```

Roles: `owner`, `admin`, `member`, `viewer`.

---

## Environments

Each project supports multiple environments. Common setup:

```bash
dotsync push --env dev
dotsync push --env staging
dotsync push --env production

dotsync pull --env production
```

---

## Pricing

DotSync is **completely free**. Every feature — audit logs, secret scanning, version history, team management, unlimited projects — is available to all users at no cost.

The only paid option is a **one-time $500 license** to self-host the DotSync server on your own infrastructure, if you have compliance or data-residency requirements.

| | Free | Self-Hosted ($500 once) |
|--|------|------------------------|
| Users | Unlimited | Unlimited |
| Projects | Unlimited | Unlimited |
| Environments | Unlimited | Unlimited |
| Version history | Unlimited | Unlimited |
| Audit logs | Yes | Yes |
| Secret scanning | Yes | Yes |
| Team management | Yes | Yes |
| CI/CD integrations | Yes | Yes |
| Data location | dotsync.onrender.com | Your own server |

---

## Self-hosting

```bash
git clone https://github.com/Pruthviraj36/dotsync
cd dotsync

# Set environment variables (see .env.example)
export DATABASE_URL=postgres://...
export GITHUB_CLIENT_ID=...
export JWT_SECRET=...

go run main.go
```

Deploy on Railway, Render, Fly, or any VPS. See [render.yaml](render.yaml) for the Render configuration.

A self-hosting license ($500, one-time) is required for production use outside of dotsync.onrender.com. [Purchase here](https://dotsync.onrender.com/billing).

---

## GitHub OAuth App setup

Create a GitHub OAuth App at https://github.com/settings/developers:

- **Application name:** DotSync (or anything you like)
- **Homepage URL:** your server URL
- **Authorization callback URL:** `{your-server-url}/api/auth/github/callback`

Set `GITHUB_CLIENT_ID` in your server environment. The client secret is not needed — DotSync uses the OAuth Device Flow, which doesn't require a server-side secret.

---

## Security

- The server never sees your plaintext secrets
- All secrets are AES-256-GCM encrypted before leaving your machine
- Every push is signed with an Ed25519 machine key; teammates can verify who pushed
- Audit logs record every access with IP address and timestamp
- HMAC-SHA256 signs every API request; replay attacks don't work
- Short-lived JWTs with refresh token rotation and replay detection

Found a security issue? Email the maintainer directly. Do not open a public GitHub issue.

---

## License

MIT for personal and open-source use. A commercial self-hosting license is required for production deployment outside of the hosted service. See [LICENSE.md](LICENSE.md).
