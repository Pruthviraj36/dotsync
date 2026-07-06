# DotSync 🔐

**Encrypted `.env` sync for dev teams. Your secrets never leave your machine in plaintext — ever.**

\---

I built DotSync after one too many *"can someone send me the latest `.env`?"* messages.
Between onboarding, new laptops, CI fixes, and accidental commits, secret sharing was always messy.
DotSync keeps that workflow simple: one command to push, one to pull, and encryption happens on your machine before anything is uploaded.

```
$ dotsync push
🔒 Encrypting 10 secrets for team access (my-app/dev)...
📤 Uploading... ✅
  Project : my-app
  Env     : dev
  Version : v7
  Secrets : 10 keys encrypted
  Teammates can now run: dotsync pull
```

\---

## How it works (plain English)

DotSync does **client-side encryption**. That means:

1. Your `.env` is encrypted on your laptop using a key derived from your project password
2. Only the ciphertext travels to the server
3. The server stores an encrypted blob it can't read
4. Your teammates decrypt it locally using the same password

No trust required on our end. Even if the database leaked tomorrow, your secrets would be unreadable.

**The crypto stack, if you care:**

* **Argon2id** key derivation (time=3, memory=64MB) — slow enough to make brute-force miserable
* **AES-256-GCM** encryption — authenticated, so tampered data fails loudly
* **HMAC-SHA256** request signing — every API call is signed, replay attacks don't work
* **JWT with refresh rotation** — short-lived access tokens, automatic refresh

\---

## Why DotSync is written in Go (and not another language)

Short version: DotSync has two core jobs — a **CLI** people run everywhere and an **API server** that should be fast, simple, and cheap to operate. Go gave the best balance for both in one codebase.

These are the main criteria I optimized for:

1. **Single static binaries**: easy install for users, easy deploy for server, fewer runtime surprises.
2. **Cross-platform CLI**: first-class support for Linux/macOS/Windows from one build pipeline.
3. **Fast startup + low overhead**: important for short-lived CLI commands and autoscaled server instances.
4. **Operational simplicity**: no VM/runtime management in production, fewer moving parts in containers.
5. **Strong stdlib for networking/crypto**: HTTP, TLS, JSON, and crypto primitives are mature and built in.
6. **Concurrency model that stays readable**: goroutines/channels fit IO-heavy API workloads well.
7. **Type safety without heavy ceremony**: catches a lot at compile time while staying productive.
8. **Small team velocity**: code is easier to onboard into than many low-level alternatives.
9. **Predictable performance**: good enough throughput/latency without hand-tuning everything.
10. **Release and CI ergonomics**: GoReleaser + cross-compile makes shipping straightforward.
11. **Security posture**: fewer transitive runtime dependencies for the core path than typical JS stacks.
12. **One language across CLI + server**: shared mental model, easier maintenance.

That doesn’t mean other languages are bad. They’re excellent — just optimized for different tradeoffs:

| Language | Great strengths | Why I didn’t choose it for DotSync core |
| --- | --- | --- |
| **TypeScript/Node.js** | Fast product iteration, huge package ecosystem | Runtime dependency + heavier supply-chain surface for security-sensitive core tooling |
| **Python** | Rapid prototyping, superb scripting/data ecosystem | Packaging/distribution friction for polished cross-platform CLI binaries |
| **Rust** | Maximum performance/control, strong safety | Higher complexity and slower iteration for a small team shipping quickly |
| **Java/Kotlin** | Mature backend ecosystem, great tooling | Heavier runtime footprint/startup for this CLI-first use case |
| **C#/.NET** | Excellent developer experience, strong cloud tooling | Larger runtime/distribution footprint than needed for this project shape |

So this wasn’t ideology. It was a practical choice for **distribution, deployability, security, and maintenance cost**.
If DotSync’s constraints change, the stack can change too.

\---

## Install

**With Go:**

```bash
go install github.com/Pruthviraj36/dotsync@latest
```

**Download binary directly:**

Head to [Releases](https://github.com/Pruthviraj36/dotsync/releases) and grab the binary for your platform. Cross-compiled for Linux, macOS, and Windows via GoReleaser.

\---

## Quick start

### 1\. Log in

```bash
dotsync login
```

Opens a GitHub device flow — you'll see a short code to enter at `github.com/login/device`. No passwords to set up, no OAuth app to configure yourself.

### 2\. Link your project

Run this inside your project folder:

```bash
dotsync init
```

It'll ask for a project slug and a shared password. The password is what encrypts your secrets — share it with teammates the same way you'd share a WiFi password, once, securely. After that, DotSync handles everything.

### 3\. Push and pull

```bash
# Encrypt and upload
dotsync push
# Download and decrypt to .env
dotsync pull
```

That's it for most use cases.

**.env format rule:** DotSync accepts only blank lines, comment lines (`# ...`), and `KEY=VALUE` entries. Any other syntax is rejected on push/pull/diff/run/rollback to prevent malformed secret files.

\---

## Command reference

### Secrets

```bash
dotsync push                          # Push current .env
dotsync push --env production         # Push to a specific environment
dotsync push --file .env.staging      # Push a different file
dotsync push --local                  # Encrypt for yourself only (no team access)
dotsync pull                          # Pull latest .env
dotsync pull --env staging            # Pull a different environment
dotsync pull --output .env.local      # Write to a specific file
dotsync pull --force                  # Skip overwrite confirmation
```

### History and diff

```bash
dotsync history                       # See all versions and who pushed them
dotsync diff                          # Compare your local .env with remote
dotsync rollback --version 3          # Restore a previous version
```

The diff only shows which keys changed — never the values:

```
🔍 Diff: local .env ↔ remote my-app/dev (v7)
──────────────────────────────────────────────────
  + DATABASE\_URL                  (new key, only in local)
  \~ REDIS\_URL                     (value changed)
──────────────────────────────────────────────────
  +1 added  -0 removed  \~1 changed
  Run 'dotsync push' to upload your local changes.
```

### Run with secrets injected

```bash
dotsync run -- node server.js
dotsync run -- python manage.py runserver
dotsync run --env staging -- ./scripts/migrate.sh
```

Secrets are injected into the subprocess environment and never written to disk. When the process exits, they're gone.

### Team management

```bash
dotsync team list                     # See who's on the project
dotsync team add @username            # Invite someone
dotsync team remove @username         # Remove access
dotsync team role @username admin     # Change role
```

Roles: `owner` → `admin` → `member` → `viewer`. Viewers can pull but not push.

### Other

```bash
dotsync envs                          # List environments for this project
dotsync status                        # Show login, project, and sync state
dotsync scan                          # Scan codebase for accidentally committed secrets
dotsync audit                         # View audit log (Business plan)
dotsync billing plans                 # Compare plans
dotsync billing upgrade               # Upgrade your plan
dotsync billing giftcard create       # Create a gift card (server admin)
dotsync billing giftcard redeem CODE  # Redeem a gift card to unlock features
dotsync update                        # Update the CLI to latest
dotsync version                       # Show current version
```

Server admins are configured with `DOTSYNC_ADMIN_USERS` (comma-separated GitHub usernames, with or without `@`). Admin users get full feature access and can create gift cards.

\---

## CI/CD

Set `DOTSYNC\_PASSWORD` in your pipeline and DotSync skips the interactive password prompt:

```yaml
# GitHub Actions
- name: Pull production secrets
  env:
    DOTSYNC\_PASSWORD: ${{ secrets.DOTSYNC\_PROJECT\_PASSWORD }}
  run: |
    dotsync pull --env production
    # your .env is now written and ready
```

Works with GitHub Actions, GitLab CI, Vercel, Railway, Render — anything that supports environment variables.

\---

## Environments

Every project comes with three environments out of the box: `dev`, `staging`, `production`. Use `--env` to target any of them:

```bash
dotsync push --env production
dotsync pull --env staging --output .env.staging
dotsync diff --env production
```

\---

## Personal mode

Sometimes you want to push secrets only you can read — API keys for personal accounts, local overrides, stuff that shouldn't be shared even with your team:

```bash
dotsync push --local   # encrypted with your personal token
dotsync pull --local   # only you can decrypt this
```

\---

## Contributing

The project is still early. If something breaks or you find a security issue:

* **Bug?** Open an issue with your OS, CLI version (`dotsync version`), and what happened
* **Security issue?** Email directly — don't open a public issue for vulns
* **Want to contribute?** PRs are welcome. The codebase is straightforward Go — Chi router on the server, Cobra CLI on the client

If you're one of the first 100 people to open a meaningful PR or file a real bug, you get free lifetime Premium. That's not marketing copy — it's just a thank you.

\---



*Built out of frustration with copy-pasting `.env` files into Slack. If you've felt the same pain, give it a try.*
