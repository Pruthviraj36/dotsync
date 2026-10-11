# DotSync: Deep Review

SWOT · Security (attacker's view) · Dead code · Redundant API calls · Correctness bugs · Terminal UI · Web UI · Remediation plan

---

## 0. Scope, method, caveats

**Reviewed:** everything outside `vendor/`:

| Area | Files | Approx. size |
|---|---|---|
| Server | `cmd/dotsync/main.go`, `internal/{auth,crypto,db,handler,middleware,model,service,assets}` | ~2.4k lines Go |
| CLI | `cli/{api,cmd,config,crypto,identity}`, `main.go` | ~4.9k lines Go (of which `cli/cmd/ui.go` is 3,990) |
| Data | `migrations/000001–000008` | 8 up/down pairs |
| Ops | `Dockerfile`, `render.yaml`, `scripts/*.sh`, `install.sh`, `install.ps1`, `_config.yml` | |
| Docs | `README.md`, `CODEBASE_ANALYSIS.md` (header only) | |

**Not reviewed:** `vendor/` (1,031 files), the middle portion of the legacy dashboard HTML (it is dead code, see D1), and most of `integrate.go` beyond its printed output.

**Method:** static reading plus scripted greps (reference counts for every top-level symbol, route/middleware tracing, claim-vs-code cross-checks of the README and in-app Guide).

**Caveats:** I could not compile or run anything (no Go toolchain in my sandbox). Items marked **[verify]** are inferred from code and should be confirmed at runtime. Severity ratings assume this is used to store real production secrets.

**Severity scale:** Critical = breaks the product's core promise or allows takeover. High = real exploit or data-loss path. Medium = hardening or integrity gap. Low = hygiene.

---

## 1. Executive verdict

DotSync has good bones and obvious care in places: sane Argon2id parameters, AES-GCM with random nonces, hashed refresh and service tokens, refresh-token rotation with row locks, parameterized SQL everywhere, and a thoughtful `run` command. But for a secrets product, the distance between what is claimed and what is implemented is the main problem.

**Three things block trusting it with production secrets:**

1. **The trust model is not zero-knowledge** (S1). The server stores every project password in a form it can decrypt, and serves it to any member or service token.
2. **Service tokens are not scoped the way the docs say** (S3). They are not bound to a project, they can write, and they impersonate their creator in the audit log.
3. **The local dashboard is an unauthenticated command-execution surface** (S4) that returns plaintext secrets.

**Secondary themes:**
- About 40% of `ui.go` (3,067 of 3,990 lines) is dead code.
- Documentation (README, in-app Guide, CLI hints) describes commands and behaviors that do not exist.
- There are zero tests.
- Redundant API calls inflate latency and the audit log.
- The terminal output is verbose, and its "progress" is not truthful.

---

## 2. SWOT

### Strengths

| Strength | Evidence |
|---|---|
| Sound crypto primitives | `crypto.DeriveKey` uses Argon2id (t=3, m=64 MiB, p=4). `Encrypt` uses AES-256-GCM with a fresh random 12-byte nonce per call. |
| Token hygiene | Refresh tokens and service tokens are stored only as SHA-256 hashes. `dst_` prefix gives fast-path detection. |
| Refresh rotation done properly | `RotateRefreshToken` uses a transaction with `FOR UPDATE OF rt`, revokes on reuse, and checks expiry. |
| No SQL injection found | All queries use placeholders. The one dynamic query (`GetHistory`) appends only fixed fragments. |
| Device-flow login | No client secret on the server. Works over SSH and in containers. |
| Credential storage | OS keyring with 0600 file fallback. `DOTSYNC_CONFIG_DIR` and sudo handling show real-world thought. |
| Product instincts | `dotsync run` (secrets injected without writing a `.env`), digest-based no-op pull, signal forwarding, signed pushes, version history with append-only rollback. |
| Operational basics | Graceful shutdown, server timeouts, rate limits, security headers, auto-migrations, static `scratch` image. |
| Breadth for one maintainer | Server, CLI, local dashboard, installers (sh/ps1), self-update, CI snippets, six integrations. |

### Weaknesses

| Weakness | Evidence |
|---|---|
| Claims exceed reality | See §4 table "Claim vs code". |
| Zero tests | `find . -name '*_test.go' -not -path './vendor/*'` returns 0. |
| Monolithic, patch-layered UI | `ui.go` is 3,990 lines. The live dashboard is 99 lines of ~92 KB minified JS wrapped by runtime patches. |
| Doc drift | README promises `dotsync scan` (does not exist), the Guide documents `--token`, `secrets import`, email invites. |
| Billing remnants | Plans, gift cards, LemonSqueezy and PayPal env vars in a "completely free" product. |
| No key or credential lifecycle | No password rotation, no master-key rotation caller, no TTL on service tokens, no revocation on member removal. |
| No concurrency control | Last write wins on push, with a `MAX+1` race on the server. |
| Bus factor of one | Single maintainer, single GitHub account anchoring the update path. |

### Opportunities

- **True E2EE via per-member key wrapping** (age / X25519): remove the server's ability to read secrets. This is the real differentiator in a crowded space.
- **Ship the scanner you already advertise** (`dotsync scan` plus a pre-commit hook). It is a strong adoption wedge.
- **Trust signals:** minisign-signed releases (the `aead.dev/minisign` module is already in your dependency tree), SBOM, `SECURITY.md`, `govulncheck` in CI.
- **Official GitHub Action** and Homebrew/Scoop/winget packages to replace `curl | sh`.
- **Audit export** (CSV/JSON) and SIEM webhook for team adoption.
- **Interop:** import and export for `.env`, SOPS and age.

### Threats

| Threat | Why it matters |
|---|---|
| One incident is existential | It is a secrets product, and false "zero-knowledge" copy turns an incident into a reputational or legal problem. |
| You are the honeypot | The hosted instance holds `SERVER_MASTER_KEY` and the DB connection. A Render, Neon or GitHub account compromise exposes every user. |
| Update-path supply chain | `dotsync update` trusts a SHA-256 from the same GitHub release and applies it with `selfupdate.Options{}`. A compromised maintainer account means code execution on every installed CLI. |
| Single points of failure | One Render starter instance plus Neon. Loss of `SERVER_MASTER_KEY` makes every stored project password unrecoverable. |
| Competition | Doppler, Infisical, 1Password CLI, SOPS, Vault, cloud-native secret managers. |
| Platform dependency | GitHub OAuth is the only identity provider. Rate limits and policy changes affect all logins. |
| Privacy exposure | GitHub email stored (`user:email` scope requested) but never used. |

---

## 3. Architecture as built (so the findings make sense)

```
 CLI / dotsync ui (Go, local)                         Server (Go + chi, Render)            Neon Postgres
 ───────────────────────────                          ───────────────────────────          ─────────────
 login ─ GitHub device flow ───────────────────────►  (CLI talks to GitHub directly)
        └─ POST /api/auth/github/device (GH token) ─► verify via GET api.github.com/user ─► users
 init  ─ POST /projects, PUT /projects/:s/password ─► stores password AES-GCM(master subkey) ─► project_passwords
 push  ─ GET /password ◄─── plaintext password ────── server decrypts with SERVER_MASTER_KEY
        ─ Argon2id(password, slug) → AES-GCM(.env)
        ─ ed25519 sign(sha256(ciphertext))
        ─ PUT /me/pubkey, POST /push ───────────────► secrets (ciphertext, nonce, sig, signer_pubkey)
 pull  ─ GET /history (for version), GET /pull, GET /password
        ─ verify sig with server-supplied pubkey → decrypt → write .env
 run   ─ same as pull; inject into child env (docker: --env K=V on argv)
 ui    ─ 127.0.0.1:4040 HTTP server; same operations plus /api/run
```

The key observation: the password travels server → client on every operation. The client does the AES work, but the server holds and releases the key. That is key escrow, not end-to-end encryption.

---

## 4. Claims vs code

| Claim (source) | Reality |
|---|---|
| "The server never sees your raw secrets" (README, CLI `Long`) | The server cannot be *blocked* from them: it stores the project password, decryptable with `SERVER_MASTER_KEY`, and sees it in plaintext on `PUT/GET /password`. Migration 002's own comment admits the changed trust model. |
| "Every API request is signed with HMAC-SHA256: replay attacks don't work" (README) | `middleware.VerifyHMAC` is never mounted. The HMAC key is the bearer token (which the server already has), with no timestamp or nonce. Even if mounted, it prevents nothing. |
| "Every push, pull, and team change is logged" (README, CLI `audit`, Guide) | Team add/remove/role, token create/revoke, login and logout are never written. `TeamHandler` does not even hold an audit service. |
| "Audit Log: immutable, never deleted" (migration 001) | Nothing enforces append-only. `project_id ... ON DELETE CASCADE` deletes logs with the project. |
| `dotsync scan` detects 20+ secret patterns (README) | No `scan` command exists anywhere in `cli/`. |
| "Roll with `dotsync init --rotate-password`" (`service.go` error) | No such flag. `init` only has `--new`. |
| Tokens are "scoped to a project+environment… read-only" (README, `tokens` help) | Not bound to a project; can call several write or sensitive endpoints (S3). |
| "A token scoped to staging cannot pull production" (Guide) | True for same-project pulls. Cross-project and password fetch are not scoped. |
| "Keys are stored server-side but never transmitted to your machine… the server does the crypto work" (Guide) | Opposite of the code: the password is transmitted to the client, which does the crypto. |
| "Decrypts them in your browser" (Guide) | Decryption happens in the local Go process. The browser receives plaintext JSON. |
| `dotsync pull --token ${{ secrets.DOTSYNC_TOKEN }}`, `dotsync secrets import < secrets.env` (Guide) | Neither flag nor command exists. The token comes from the `DOTSYNC_TOKEN` env var. |
| "Owners and admins can change roles", "Only the owner can remove members" (Guide) | Server: only the owner changes roles, and owners and admins can remove members (admins cannot remove admins or owners). |
| "A project can have 10+ environments" (Guide) | DB `CHECK (name IN ('dev','staging','production'))`. |
| "Invite by email… pending status" (Guide) | Invites are by GitHub username and take effect immediately. |
| Secrets "never appear in process listings" (Guide) | `docker run --env KEY=VALUE` puts values in argv (S5). |
| `team add --role viewer` yields a viewer (README) | The user is first added as `member` (can push), then patched, non-atomically. |
| Completely free, no tiers (README) | `users.plan` CHECK still allows pro/team/business, `gift_cards` tables exist, `render.yaml` configures LemonSqueezy and PayPal. |

---

## 5. Security findings (attacker's view)

Each finding lists: **Where**, **Attack/impact**, **Fix**.

### S1. Not zero-knowledge: server-held project password [Critical]

**Where:** `internal/service/service.go` (`PasswordService`), `internal/handler/handler.go` (`PasswordHandler.Get/Set`), migration 002, `cli/cmd/password.go`.

**Impact:**
- Any party who controls the server process (hosted-instance operator, anyone who gets RCE, a Render dashboard compromise exposing env vars) has both `SERVER_MASTER_KEY` and the DB. They can derive the per-project subkey, decrypt the password, derive the AES key (the salt is derivable from the slug) and decrypt every version of every `.env`.
- Every team member and every service token receives the plaintext password on request.
- A DB-only leak is genuinely protected (the master key is not in the DB). That part of the README is fair.

**Fix options:**
1. *Honesty first:* relabel as "encrypted at rest; server-assisted key distribution" in README, CLI help and Guide.
2. *Real E2EE:* generate a random 256-bit project key client-side. Wrap it to each member's X25519 public key (age format works well). Store only wrapped keys server-side. On member removal, generate a new key and re-wrap for the remaining members. Service tokens get their own wrapped key or a CI-held decryption identity.

### S2. GitHub token confusion on login [High]

**Where:** `auth.FetchGitHubUser`, `handler.AuthHandler.GitHubDeviceLogin`.

**Attack:** The endpoint accepts any GitHub token that resolves at `GET api.github.com/user`. It never checks that the token was issued to *your* OAuth app. A token obtained by any other OAuth app the victim authorized, or a leaked PAT (even a zero-scope one), logs the attacker into DotSync as the victim. From there: `GET /password` plus `pull`.

**Fix:** Make the server participate in the device flow: `POST /api/auth/device/start` (server calls GitHub with its `client_id`) → returns `user_code` and `verification_uri`; `POST /api/auth/device/poll` (server polls GitHub, receives the token itself). Provenance is then guaranteed and you still need no client secret. Also request an empty scope: `/user` public data does not need `read:user user:email` (S18).

### S3. Service-token scoping broken [High]

**Where:** `middleware.Authenticate`, every handler using `claims.UserID`.

**Details:**
- **(a) No project binding.** The middleware loads `st.ProjectID` from the DB but never puts it in context. Handlers then run `GetBySlug(slug, claims.UserID)` where `UserID` is the *creator's*. A token minted for project A can access project B's `production` if the creator belongs to both.
- **(b) Not read-only.** Routes without an `IsServiceTokenRequest` guard:

| Route | What a token can do |
|---|---|
| `POST /api/projects` | Create projects as the creator |
| `PUT /api/me/pubkey` | Overwrite the creator's signing key (subsequent pushes appear to come from a key the attacker controls) |
| `POST /api/auth/logout` | Revoke all of the creator's sessions (DoS) |
| `GET /api/auth/me` | Read the creator's email and GitHub ID |
| `GET /api/projects/:s/audit` | Read the audit log if the creator is owner or admin |
| `GET /api/projects/:s/team`, `/envs`, `GET /api/projects` | Enumerate team and projects |
| `GET /api/projects/:s/password` | Fetch the project-wide password (needed for CI, but not env-scoped) |

- **(c) Attribution.** Audit rows use `claims.UserID = st.CreatedBy`, so a CI pull is indistinguishable from the human's. The Guide's claim that "audit will show which token was used" is false.

**Fix:** introduce a separate principal and enforce routes by allowlist.

```go
type Principal struct {
    Kind      string // "user" | "token"
    UserID    string // only for kind=user
    TokenID   string // only for kind=token
    ProjectID string // only for kind=token
    Env       string // "*" or env
}

// token allowlist: GET /projects/{slug}/envs/{env}/pull, /pull/version,
// /history (scoped), GET /projects/{slug}/password (scoped to ProjectID)
func requireUser(next http.Handler) http.Handler { /* 403 if Kind != "user" */ }

// every project lookup compares token.ProjectID == proj.ID
// audit.Log(actor = "token:"+TokenID, onBehalfOf = creator)
```

### S4. Local dashboard: no session auth, arbitrary command execution [High]

**Where:** `cli/cmd/ui.go`: `/`, `/api/*`, `/api/run`, CSP header, `uiHandler`/`uiPostHandler`.

**Details:**
- Binds to `127.0.0.1:4040`, with no per-session secret and no `Host`, `Origin` or `Sec-Fetch-Site` validation.
- `/api/pull` and `/api/version` return plaintext secrets. `/api/push`, `/api/rollback` and `/api/team/*` mutate. `/api/run` executes `exec.CommandContext(args[0], args[1:]...)` with the decrypted environment.
- **Reachable by:** any local user on a shared machine, any local process, a malicious browser extension, and (classic DNS rebinding) a web page that rebinds a hostname to 127.0.0.1 so requests become same-origin. Modern browsers have partial mitigations **[verify]**, but you should not depend on them.
- The Content-Type check on POST stops simple form CSRF only. The CSP is `script-src 'unsafe-inline'`, so any XSS equals RCE through `/api/run`.
- `Access-Control-Allow-Origin: "http://localhost:4040 http://127.0.0.1:4040"` is not valid CORS (single origin only) and is hardcoded to 4040 even with `--port`. It is both useless and misleading.
- Google Fonts are allowed in the CSP (`fonts.googleapis.com`), which makes a "fully local" tool phone home on every open.

**Fix:**
```go
func guard(token, port string, next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        okHost := r.Host == "127.0.0.1:"+port || r.Host == "localhost:"+port
        okFetch := r.Header.Get("Sec-Fetch-Site") != "cross-site"
        okTok := subtle.ConstantTimeCompare(
            []byte(r.Header.Get("X-DotSync-Session")), []byte(token)) == 1
        if !(okHost && okFetch && okTok) { http.Error(w, "forbidden", 403); return }
        next.ServeHTTP(w, r)
    })
}
```
- Generate `token` with `crypto/rand` at startup. Open the browser to `http://127.0.0.1:4040/#t=<token>`. The page reads the fragment and sends it as `X-DotSync-Session`. The fragment is never sent in HTTP requests or logs.
- Remove `/api/run`, or require a per-request typed confirmation plus a visible command echo.
- Use a CSP nonce for scripts and remove `'unsafe-inline'`. Self-host fonts. Remove the CORS header.

### S5. `dotsync run -- docker run` puts secret values in argv [High]

**Where:** `cli/cmd/run.go`, `injectContainerEnv`.

**Details:** It builds `--env KEY=VALUE` for every secret. Values are visible in `ps`, `/proc/<pid>/cmdline` and some runtime logs. The code comment says "`--env KEY` passes them", but the code writes `k+"="+v`. The README markets this as zero-disk injection.

**Fix (one line):**
```go
for k := range secrets {
    envFlags = append(envFlags, "--env", k) // value inherited from proc.Env
}
```
`proc.Env` already contains the values, so the container runtime reads them from its own environment.

### S6. Refresh-token race logs the user out of every device [High, availability]

**Where:** `auth.RotateRefreshToken`, `cli/api/client.go` (`refreshTokens`).

**Details:** Access tokens live 15 minutes. If two CLI processes (two terminals, a watcher plus the UI, parallel scripts) hit expiry together, both send the same refresh token. The first rotates it. The second sees `revoked = true`, and the server treats that as theft: `UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`. Then the client calls `config.ClearGlobal()`. The result is a full logout on all devices, triggered by normal use.

**Fix:**
- Server: allow reuse of a just-rotated token for 10–30 seconds (store `rotated_at` and `replaced_by`, and return the same successor tokens during the grace window).
- Client: a cross-process file lock around refresh (e.g., `flock` on `~/.dotsync/refresh.lock`), re-reading the config after acquiring the lock.

### S7. `--local` mode is both insecure and data-destroying [High]

**Where:** `cli/cmd/secrets.go` (`localFlag`), `cliCrypto.EncryptEnvFile` doc comment.

**Details:** `--local` uses `cfg.AccessToken` (the bearer JWT) as the encryption password. (1) The server sees that token on every request, so this mode is not zero-knowledge. (2) The JWT rotates every 15 minutes, so anything pushed with `--local` becomes undecryptable after the next refresh. It is undocumented in the README but visible in `--help`.

**Fix:** Delete the flag. If a personal-key mode is wanted, derive from a locally generated key stored in the keyring.

### S8. Crypto binding and signature trust gaps [Medium]

**Where:** `internal/crypto.Encrypt/Decrypt`, `cli/cmd/manifest.go`, `handler.SecretsHandler.Push`, `pull/run/ui`.

| Gap | Consequence |
|---|---|
| `gcm.Seal(nil, nonce, plaintext, nil)`: no AAD | Ciphertext is not bound to project, env or version. A malicious server or member can replay or swap ciphertext (e.g., serve `dev` content as `production`, or an older version as latest) and it will decrypt. |
| Signature = `Ed25519(sha256(ciphertext))` only | Same replay: the signature is valid for those bytes in any context. |
| Verifier trusts `pushed_by_pubkey` from the server | The pusher supplies `signer_pubkey` at push time. No pinning, no TOFU, no link to the user's registered key. A member (or the server) can sign with any key and verification passes. |
| Unsigned or keyless data accepted | `verifySignature` returns `(false, nil)`. `pull` only warns, the UI silently proceeds, and only `run` and `rollback` demand `--allow-unsigned`. Stripping the signature is a downgrade attack. |
| KDF salt derived from the slug | `DeriveKey(password, projectSlug)`. The server docs say `projectID`. Slugs are guessable and can repeat across servers. |
| No password policy | `init` accepts any non-empty password. Weak passwords plus Argon2id are still brute-forceable from a DB dump of ciphertext. |

**Fix:** AAD = `"dotsync/v1|"+projectID+"|"+env+"|"+version`. Sign the same tuple plus the ciphertext hash. Pin each member's public key on first sight (store in `~/.dotsync/known_keys`) and warn on change. Reject unsigned data by default. Generate a random 256-bit project key by default rather than a user-typed password.

### S9. Spoofable client IP: rate-limit bypass and forged audit IPs [Medium]

**Where:** `cmd/dotsync/main.go` (`chimiddleware.RealIP`), `handler.realIP`, `httprate.KeyByIP`.

**Details:** `RealIP` trusts `True-Client-IP`, `X-Real-IP` and the first `X-Forwarded-For` entry from any client. The per-IP limits (20/min on auth, 200/min global) key on the rewritten address, so a client can rotate a fake header to dodge them. Audit rows store the same spoofable value. `handler.realIP()` re-implements the same trust.

**Fix:** Configure the trusted proxy (Render appends the real client IP at the end). Use the rightmost trusted hop, not the first. Delete `handler.realIP`.

### S10. Removing a member does not revoke access to secrets [Medium]

**Where:** `TeamHandler.RemoveMember`, `cli/cmd/team.go`.

**Details:** The removed user keeps the project password and anything they pulled. The CLI hint says "consider rotating secrets: `dotsync push`", but `push` does not rotate the password. No rotation command exists (`init --rotate-password` is referenced but missing), and old versions stay decryptable with the old password.

**Fix:** `dotsync team remove --rotate` should (1) generate a new project key, (2) re-encrypt the latest version of every env, (3) list every secret key that the removed user could have read so the team knows what to rotate upstream, and (4) audit the event.

### S11. Username-based identity is weak [Medium]

**Where:** migration 001 (`users.username` not unique), `AddMember/RemoveMember/UpdateRole` (`SELECT id FROM users WHERE username = $1`).

**Attack:** A GitHub user renames, someone else takes the old name and logs in. `UpsertUser` keys on `github_id`, so a second row with the same `username` is created. `team add oldname` now picks an arbitrary row (no `ORDER BY`), potentially granting access to the wrong account. Lookups are also case-sensitive although GitHub logins are not.

**Fix:** Look up by GitHub ID where possible (resolve via the GitHub API at invite time), or add a unique index on `lower(username)` and handle collisions on login by marking stale rows.

### S12. No request body limits on the server [Medium]

**Where:** all handlers; only the unmounted `VerifyHMAC` has a limit (10 MB).

**Details:** `json.NewDecoder(r.Body).Decode(&req)` with `[]byte` fields (base64) is unbounded apart from the 15 s read timeout. Any member can push oversized blobs to inflate memory and Postgres storage. History queries are also unbounded.

**Fix:** `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)` in a middleware (1 MB is plenty for a `.env`), plus a `CHECK (octet_length(encrypted_data) <= 1048576)`. Paginate `/history`.

### S13. SHA-256 of plaintext `.env` committed to the repo [Medium]

**Where:** `config.PulledState.Digest` stored in `.dotsync.json`; `init` tells users to commit that file.

**Details:** `localPullDigest(data)` is `sha256(plaintext)`. For small or low-entropy files it is a guess-confirmation oracle. It also produces constant git churn and merge conflicts in a committed file.

**Fix:** Store pull state under `~/.dotsync/state/<project-hash>.json`, using `HMAC-SHA256(local_secret, content)`.

### S14. HMAC request signing is security theater [Medium]

**Where:** `cli/api/client.go` (signs every request), `middleware.VerifyHMAC` (never mounted).

**Details:** The key is the bearer token. Anyone who has the token can sign. There is no timestamp, nonce or method/path binding, so it cannot stop replay. The client burns CPU computing signatures nobody checks, and the README advertises replay protection.

**Fix:** Delete it, or implement properly (key from a per-device secret, signature over `method|path|timestamp|nonce|body-hash`, server-side nonce cache).

### S15. Installer and self-update trust chain [Medium]

**Where:** `internal/assets/install.sh`, `install.ps1`, `cli/cmd/update.go`, `scripts/build.sh`.

| Issue | Detail |
|---|---|
| Integrity but not authenticity | `checksums.txt` is fetched from the same GitHub release as the binary. An attacker who can edit the release replaces both. |
| No signature | `selfupdate.Apply(binaryReader, selfupdate.Options{})`. `minisign` is already a transitive dependency but unused. |
| Hard-wired origin | `update` always queries `api.github.com/repos/Pruthviraj36/dotsync`, even when the user self-hosts. |
| No timeouts | Three `http.Get` calls with the default client. A stalled connection hangs forever. Unbounded `io.ReadAll`. |
| No downgrade guard | Update is skipped only on equality. A lower or tampered `tag_name` would be applied. |
| `install.sh` via the server | The installer is served by whichever server the user points at, then fetches binaries from GitHub. |
| arm64 mismatch | `install.sh` accepts `arm64/aarch64` on Linux, but `build.sh` builds no `linux/arm64` (nor `windows/arm64`), so installs 404. |
| `pipefail` under `sh` | README says `curl … \| sh`, but the script uses `set -euo pipefail`. Older dash-based `sh` rejects `-o pipefail` **[verify]**. |
| Unauthenticated GitHub API | `releases/latest` is limited to 60 requests/hour per IP, which hits CI runners behind NAT. |

**Fix:** Sign releases with minisign and verify with `selfupdate.Options{Verifier: …}`. Embed the public key in the binary. Add timeouts and size caps. Compare semver and refuse downgrades. Build all supported targets. Document `| bash` or make the script POSIX.

### S16. Deployment and configuration hygiene [Medium/Low]

| Item | Detail | Fix |
|---|---|---|
| Root container | `FROM scratch` with no `USER` runs as UID 0. | `USER 65534:65534`, read-only root FS. |
| Build mutability | `render.yaml` runs `go mod tidy && go build`, while `vendor/` is committed and the Dockerfile uses `go mod download`. | Pick one. Build with `-mod=vendor` or `-mod=readonly`. |
| `JWT_SECRET` strength | Only non-empty is checked. | Require at least 32 bytes. |
| CORS default | `FRONTEND_URL` defaults to `http://localhost:3000` with `AllowCredentials: true` in production. | Require explicit config or disable CORS. |
| Error leakage | `Create` returns `"create project failed: "+err.Error()`: SQL and constraint names, and a slug-existence oracle. `RotateRefreshToken` returns internal strings via `err.Error()`. | Generic errors plus request IDs in logs. |
| DB TLS | Not enforced in code. | Require `sslmode=require` at startup. |
| Master-key lifecycle | README: "never change after first use". Migration 007 implies rotation. No caller exists. | Add a `rotate-master-key` admin command or drop the key-version plumbing. |
| HSTS | Sent on every response, including plaintext. | Harmless, but only meaningful behind TLS. |
| No `SECURITY.md` | README says "email the maintainer directly", but gives no address. | Add contact, PGP key and disclosure policy. |

### S17. Audit log is mostly fiction [Medium]

**Where:** `SecretsHandler`, `PasswordHandler` (only these call `auditSvc`), `AuditService.GetLogs`, `cli/cmd/audit.go`.

| Gap | Detail |
|---|---|
| Missing events | invite, revoke, role change, token create and revoke, login, logout, history reads, failed auth. |
| Noise | Every `run`/`pull`/`push` writes two rows (`pull` and `password_fetch`). The API returns only the latest 50, so about 25 operations push real events out of view. |
| Client-side filtering | `audit --env X` filters the 50-row result, so older production events are invisible and the CLI says "No audit events". |
| Fail-open | Audit write errors are only logged. |
| Not immutable | No revoke or trigger. Cascade deletes with the project. |
| Spoofable IP | See S9. |
| Wrong principal | See S3(c). |
| Dead parser | The CLI renders `invite`, `revoke` and `token_*` actions the server never emits. |

**Fix:** Log every state change with `(actor_kind, actor_id, project, env, action, metadata, ip)`. Add `before`/`limit`/`action`/`env` query params. Make the table append-only with a DB trigger or role. Decide explicitly whether a failed audit write should block secret reads.

### S18. Collateral and PII [Low]

- The CLI requests the scope `read:user user:email`, and the server stores `email`, but nothing uses it. Drop the scope and the column.
- GitHub tokens are sent to whichever server the user configured (`config set-server`, no HTTPS enforcement). A malicious or misconfigured server also chooses the `client_id` returned by `/api/auth/config`. Warn on non-HTTPS, and validate the host before login.
- JWT: HS256 only, no `aud`/`jti`, a stale `plan` claim, and no revocation of live access tokens after logout (up to 15 minutes).

---

## 6. Route × principal matrix (current behavior, from code)

Legend: ✅ allowed · ❌ blocked · ⚠ allowed but shouldn't be · U=user JWT, T=service token

| Route | Required role (user) | U | T (today) | T (should be) |
|---|---|---|---|---|
| `POST /api/auth/logout` | any | ✅ | ⚠ (kills creator's sessions) | ❌ |
| `GET /api/auth/me` | any | ✅ | ⚠ | ❌ |
| `PUT /api/me/pubkey` | any | ✅ | ⚠ | ❌ |
| `POST /api/projects` | any | ✅ | ⚠ | ❌ |
| `GET /api/projects` | any | ✅ | ⚠ | ❌ |
| `GET /projects/:s/envs` | member+ | ✅ | ✅ | project-bound only |
| `GET /projects/:s/team` | member+ | ✅ | ⚠ | ❌ |
| `POST/DELETE/PATCH /projects/:s/team…` | owner (add/role), owner/admin (remove) | ✅ | ❌ | ❌ |
| `GET /projects/:s/audit` | owner/admin | ✅ | ⚠ (if creator is owner/admin) | ❌ |
| `POST/GET/DELETE /projects/:s/tokens` | owner/admin | ✅ | ❌ | ❌ |
| `GET /projects/:s/password` | member+ | ✅ | ✅ (not project-bound) | project-bound |
| `PUT /projects/:s/password` | owner/admin | ✅ | ❌ | ❌ |
| `POST …/envs/:e/push` | member+ (not viewer) | ✅ | ❌ | ❌ |
| `GET …/envs/:e/pull`, `/pull/version`, `/history` | member+ | ✅ | ✅ env-scoped, **not project-bound** | project- and env-bound |

---

## 7. Redundant API calls and wasted work

| # | Where | What happens | Cost | Fix |
|---|---|---|---|---|
| R1 | `resolvePassword` in push, pull, run, diff, rollback, and every UI endpoint | `GET /password` each time (server decrypt, an extra audit row, then Argon2id again client-side) | +1 round trip, +1 audit row, ~64 MiB KDF per call | Cache per process and per UI session. Derive once and reuse the key. Longer term, wrapped per-member key (S1). |
| R2 | `client.GetLatestVersion` | Calls `/history` (full unbounded list, JOIN to users) and keeps `history[0]` | O(n) payload for O(1) data. Used by `pull`, `status`, `rollback`, UI `/api/latest`. | New `GET …/envs/:e/head` returning `{version, pushed_by, hash}` with `LIMIT 1`. |
| R3 | `push`, `rollback`, UI push, UI rollback | `PUT /me/pubkey` on every call | Rate-limit budget, DB write, overwrites state each time | Upload on identity creation or key change only. Server should refuse silent changes. |
| R4 | UI `/api/project/` | 4 sequential calls (envs, team, tokens, audit), then client `pull(false)` adds `latest`/`pull`/`password`, about 7 requests per refresh. It re-runs after every mutation. | Latency, audit noise | Load per tab lazily and refresh only the changed section. |
| R5 | Same endpoint, member/viewer role | `tokens` and `audit` return 403, and the handler returns the first error for the whole response | Whole dashboard fails for non-admins **[verify]** | Treat per-section 403 as an empty, hidden section. |
| R6 | `init --new` | Calls `ListProjects` before the `create` branch, then discards it | 1 wasted call | Move below the branch. |
| R7 | `rollback` | Fetches password and history before the "already at vN" early exit; ignores the history error (`currentVersion, _, _ :=`) | 2 wasted calls plus an audit row | Reorder. Handle the error. |
| R8 | Server: `Pull`, `PullVersion`, `PasswordHandler.Get` | `GetBySlug` (joins `team_members`) then `IsProjectMember` (re-queries the same table), then `GetEnvironment` | 3–4 queries per request | One query returning `(project_id, role, env_id)`. |
| R9 | `ServiceTokenService.LookupByRawToken` | `UPDATE … SET last_used_at = NOW()` on every request | A write per read, row contention in CI bursts | Update at most once per minute. |
| R10 | `SecretService.PushSecrets` | `SELECT MAX(version)` then `INSERT` | 2 round trips and a race: concurrent pushes both compute N+1, the loser gets a 500 from the unique constraint | `INSERT … SELECT COALESCE(MAX(version),0)+1 …` with retry, or return 409 plus a client message. |
| R11 | `createNewProject` | `_ = crypto.DeriveKey(password, actualSlug)` | ~64 MiB Argon2id for nothing | Delete. |
| R12 | `api.Client.do` | On any 401, tries a refresh, including for service tokens (empty refresh token) | Confusing "session expired" in CI | Skip refresh when `RefreshToken == ""` or the token has the `dst_` prefix. |
| R13 | `status` | `GetLatestVersion` (full history) plus `ListTeamMembers` | 2 calls to print 2 values | Use `/head`, and make the team count optional. |
| R14 | Auth helpers | Create a new `http.Client` per call (no connection reuse) | Minor | Share one client. |
| R15 | `ParseEnvFile` | Parsed twice on pull and diff paths | Negligible CPU | Parse once. |

**Head endpoint sketch:**
```go
// GET /api/projects/{slug}/envs/{env}/head
SELECT s.version, u.username, encode(sha256(s.encrypted_data),'hex'), s.created_at
FROM secrets s JOIN users u ON u.id = s.pushed_by
WHERE s.environment_id = $1 ORDER BY s.version DESC LIMIT 1
```

**Atomic version allocation sketch:**
```sql
INSERT INTO secrets (id, environment_id, encrypted_data, data_nonce, signature, signer_pubkey, version, pushed_by)
SELECT $1, $2, $3, $4, $5, $6, COALESCE(MAX(version),0)+1, $7
FROM secrets WHERE environment_id = $2
RETURNING version;
-- on unique_violation (23505): retry once, then return 409 Conflict
```
Add optional `base_version` in the request for optimistic concurrency (see B9).

---

## 8. Dead code, drift and repo hygiene

### 8.1 Dead code

| # | Item | Evidence | Action |
|---|---|---|---|
| D1 | `legacyDashboardHTML` (`ui.go` lines 821–3887) | 3,067 lines, ~79 KB. `grep` shows a single occurrence: its own declaration. Compiled into every CLI binary. | Delete. |
| D2 | `middleware.VerifyHMAC` | Not mounted. Server `crypto.HMACVerify` is used only by it. | Delete both, plus the client-side signing. |
| D3 | `middleware.RequestID` | chi's `RequestID` is mounted. Ours is unused and uses a string context key. | Delete. |
| D4 | `PasswordService.AddKeyVersion`, `GetCurrentKeyVersion`, `keyVersions` map | No callers. Master-key rotation cannot happen. | Implement a rotation command, or remove the plumbing and migration 007. |
| D5 | `model.Plans`, `PlanLimits`, `claims.Plan`, `ProjectService.CountForUser`, `TeamService.CountMembers`, `MaxProjects`/`MaxMembers` branches | Only the `free` plan exists with `-1`, so the branches never run. | Delete. Remove `plan` from the JWT and `users`. |
| D6 | Migration 003 (`gift_cards`, `gift_card_redemptions`) | No code references. | New migration to drop (do not edit history). |
| D7 | `render.yaml` payment/license env | `PAYMENT_PROVIDER`, `LEMONSQUEEZY_*`, `LS_VARIANT_ONPREMISE`, PayPal comments, `DOTSYNC_HOSTED` ("see internal/license", package does not exist) | Remove. |
| D8 | `scripts/check-env-example.sh` | Scans for Stripe patterns. It exits 0 when `.env.example` is absent (which it is in the zip). | Update patterns. Ship `.env.example`. |
| D9 | CLI helpers unreferenced | `blue`, `boldPurple`, `boldRed`, `cyan`, `fail`, `italic`, `kv`, `kvCyan`, `kvDim`, `kvGreen`, `kvRed`, `magenta`, `purple`, `sectionTitle`, `spin`, `txStepCustom`, `truncate`, `config.DefaultServerURL`, `cliCrypto.ParseEnvFileStrict` | Delete (or *use* the strict parser, B1). |
| D10 | `pull --verify` flag | Declared (default true), never read. `--verify=false` does nothing. | Delete or implement. |
| D11 | `/api/events` SSE | Only sends pings. The JS creates an `EventSource` that never receives data. | Delete, or replace with `/head` polling. |
| D12 | `audit` parser branches | `invite`, `revoke`, `token_create`, `token_revoke` are never emitted by the server. | Implement the events (S17) or delete the branches. |
| D13 | `serviceTokenMeta.ReadOnly` | Always true, never read. | Delete. |
| D14 | `ProjectService.GetEnvironments` | `continue` on scan errors swallows failures silently. | Return the error. |
| D15 | Invalid CORS header in `uiHandler` | Space-separated origins are not valid. The page is same-origin anyway. | Delete. |
| D16 | Live dashboard patch layers | Wrappers like `projectCore=project; project=async()=>…`, `addCore`, `tokenCore`, repeated `<style>` overrides with `!important`, text overwritten at runtime, and an in-product "What changed in this UI" card | Fold into source. Remove the changelog card. |

### 8.2 Documentation drift

| Where | Says | Reality |
|---|---|---|
| README | `dotsync scan` | Not implemented |
| `service.go` | `dotsync init --rotate-password` | Not implemented |
| Guide | `dotsync pull --token`, `dotsync secrets import` | Not implemented |
| `team add` hint | `dotsync init <slug>` | `init` takes no argument and prompts interactively |
| README | `cp .env.example .env` | File not shipped |
| README / Guide | team changes audited | Not logged |
| Guide | "10+ environments", "invite by email", "pending" status, "retention policies", "cryptographic signatures on audit entries" | None exist |

### 8.3 Repo hygiene

- `CODEBASE_ANALYSIS.md` (37 KB) is an AI-generated analysis committed at the repo root; it praises the architecture and is stale. Remove it from the repo.
- `vendor/` is committed (1,031 files, ~11 MB) while the Dockerfile and Render build disagree about modules (S16).
- **Zero tests.** No CI config, `Makefile`, `CONTRIBUTING.md` or `SECURITY.md` shipped in the archive (hidden dirs may have been omitted, so please check).
- `_config.yml` (Jekyll) excludes `cli/` and `internal/`, but the README is the entire site. Fine, but its description ("sync dotfiles") no longer matches the product.
- Migration 004's header says "Migration 003".

---

## 9. Correctness bugs (senior-dev findings)

| # | Bug | Where | Impact | Fix |
|---|---|---|---|---|
| B1 | `ParseEnvFile` is naive line-based | `cli/crypto/crypto.go` | Multi-line quoted values (PEM keys, JSON creds) are truncated. Base64 lines containing `=` become bogus env vars injected by `run`. `export FOO=bar` becomes a variable named `export FOO`. Key counts and UI diffs are wrong. (Push is unaffected; it encrypts the raw file.) | Use a proper dotenv parser, validate key names, support multi-line quotes, `export`, escapes and inline comments. |
| B2 | `diff` labels inverted and unordered | `other.go`, `DiffEnvFiles(remote, local)` | "Added" means local-only but is described as "Present remotely, missing from local". Output order is random (map iteration). | Fix labels and sort keys. |
| B3 | `ensureGitignore` substring match | `init.go` | `.env.example` or `.environment` in `.gitignore` satisfies `Contains(".env")`, so the real `.env` is not ignored. | Match whole lines, accounting for negations. |
| B4 | `pull` non-interactive abort | `secrets.go` | With no `--force` and stdin EOF, prints "Operation aborted." and exits 0. A CI step silently produces no file. | Detect non-TTY and require `--force`, exit non-zero. |
| B5 | `pull`/`rollback` file writes | `os.WriteFile` | Not atomic (crash mid-write truncates local edits) and follows symlinks. A repo containing `.env -> ~/.bashrc` makes a teammate-pushed payload overwrite the symlink target. | Write to a temp file in the same dir, `fsync`, `rename`. Refuse symlink targets (`Lstat`). |
| B6 | Login dead end | `login.go`, `client.go` | Stale or revoked tokens make `login` say "already authenticated, run logout", while refresh failure says "please log in again: dotsync login". | Verify the session in `login` (`GetMe`). Re-auth if it fails. |
| B7 | `logout` | `login.go` | Revokes all sessions on all devices; swallows the server error; prints a success step regardless. | Revoke only the current refresh token. Report failures. |
| B8 | `team add --role` non-atomic | `team.go`, `TeamHandler.AddMember` | Added as `member` (push-capable) first, then patched. A failed patch leaves the user as member with only a warning. | Accept `role` in `POST /team`, validate it server-side. |
| B9 | No optimistic concurrency | `Push` | Last write wins. A stale pusher silently overwrites a teammate's changes. The Guide claims the UI detects it. | Send `base_version`. The server returns 409 if `head != base`. The CLI offers `diff`/`--force`. |
| B10 | Version allocation race | `PushSecrets` | Concurrent pushes: one gets a 500 | See R10. |
| B11 | Progress prints "done." before work | `team.go`, `tokens.go`, `rollback.go`, `secrets.go` | Misleading output on failure | Print after success only. |
| B12 | `build.sh` platform gaps | `scripts/build.sh` | No `linux/arm64`, `windows/arm64`. `sha256sum` missing on macOS dev machines. | Add targets. Use `shasum -a 256` fallback. Consider GoReleaser. |
| B13 | `install.sh` + `pipefail` | `install.sh` | See S15 | Use `| bash`, or make it POSIX. |
| B14 | Slug validation | `ProjectHandler.Create`, `init` | Server accepts any slug (length, charset, `new`, `null`). Global uniqueness makes squatting and enumeration possible. | Regex `^[a-z0-9][a-z0-9-]{1,38}$`, reserved words list. |
| B15 | `run` double signal delivery | `run.go` | Terminal Ctrl-C reaches the child's process group and the parent forwards it again, so some runtimes (Node, Python) see two SIGINTs and hard-quit. | Put the child in its own process group or only forward non-terminal signals **[verify]**. |
| B16 | UI error mapping | `uiSafeError`, `uiHandler` | Everything is HTTP 500. Substring matches (`"401"`, `"password"`) mask real causes, e.g. a 403 becomes "Failed to handle encryption". | Return typed errors with status codes. |
| B17 | `/api/workspace/link` | `ui.go` | Rewrites `.dotsync.json` without `LastPulledStates`, erasing pull state. | Merge, don't replace. |
| B18 | Cross-surface inconsistency | `run` vs `pull` vs UI | `run` refuses unsigned data without `--allow-unsigned`, `pull` warns, UI silently accepts. | One shared policy function. |
| B19 | Partial init failure | `createNewProject` | Project record created, then password set. If `setPassword` fails, the slug is taken and the project has no password. The recovery hint references a nonexistent flag. | Create project and password in one server transaction. |
| B20 | Keyring partial failure | `config.storeTokensInKeyring` | If the access token stores but the refresh token fails, the file also holds plaintext tokens and the keyring holds a stale access token that wins on load. | Store both or neither, and clear on partial failure. |
| B21 | Identity path inconsistency under sudo | `identity.Dir` vs `config.globalConfigPath` | Config honors `SUDO_USER`, identity uses `os.UserHomeDir()`. `sudo dotsync push` may generate a second signing key under `/root`. | Share one path resolver. |
| B22 | Env var injection from the server | `run.go` | Secret keys are not validated against `^[A-Za-z_][A-Za-z0-9_]*$` (the strict parser exists but is unused). | Use it. Drop invalid names with a warning. |

---

## 10. Terminal UI review and redesign

### 10.1 What is wrong today

| Issue | Evidence |
|---|---|
| `table()` is not a table | `color.go`: prints stacked `Header: value` records separated by blank lines. 50 audit events ≈ 300+ lines, 40 versions ≈ 160 lines. No alignment, no truncation, no width awareness. |
| Dishonest progress | `txStep(n,m,msg)` prints "done." before the operation in `team add`, `tokens revoke`, `rollback` upload and `push` upload. In `logout` it prints it after a swallowed error. |
| Step numbering gaps | `pull` shows `[2/3]` and `[3/3]` but never `[1/3]`. `push` never shows `[3/3]`. |
| Output volume | A successful `push` prints banner + three steps + progress bar + summary + "Complete!" (≈14 lines) for one fact. |
| stdout/stderr | Banners and results go to stdout, so `dotsync pull | …` is polluted. `run` uses stderr correctly. `isTTY` is checked on stdout only but also governs stderr colors. |
| No machine mode | No `--json`, `-q/--quiet`, `--yes`, `--no-color` flag (only `NO_COLOR` env). |
| Prompts | `fmt.Scanln` y/N prompts are not TTY-aware (see B4) and do not trim or handle `yes`. |
| Windows | Raw ANSI codes without enabling VT processing render as garbage on legacy consoles. |
| Stale comments | `color.go` documents 80-column rules and a layout `banner()` no longer draws. |
| Dead helpers | 16 unused helpers (D9). |
| Hidden completion | `completion` is hidden to reduce clutter. Shell completion is one of the highest-value CLI features. |
| Dangerous context invisible | Nothing distinguishes `production` from `dev` visually. |

### 10.2 Design principles to adopt

1. **One result line by default**, details with `-v`.
2. **Truthful progress:** spinner, then ✓/✗ when the call returns.
3. **Preview before mutate:** show a key-level diff with masked values.
4. **Danger is loud:** `production` always red and always confirmed.
5. **Errors have two lines:** `error:` what happened, `hint:` what to do.
6. **Scriptable:** `--json`, `-q`, distinct exit codes, human output on stderr when stdout is piped.

### 10.3 Proposed outputs

**push** (with preview and confirmation on production):
```
$ dotsync push -e production
  myapp / production      ⚠ PRODUCTION      remote v11 · @alice · 2h ago

  Changes
    +  STRIPE_WEBHOOK_SECRET
    ~  DATABASE_URL                ••••••••   (value changed)
    -  OLD_FEATURE_FLAG

  Push 3 changes to production? Type "production" to confirm: production
  ✓ pushed  v12 · 18 keys · 1.4 KB · a4c9…21f · signed by @pruth
```

**pull:**
```
$ dotsync pull
  ✓ .env up to date (v12, @pruth)
$ dotsync pull            # when remote moved
  ↓ v13 by @alice · +1 ~2 −0   (local edits: none)
  ✓ wrote .env (18 keys)
```

**history:**
```
 VER   AUTHOR   AGE        NOTE
 v13   @alice   12m ago    current
 v12   @pruth   2h ago
 v11   @bob     3d ago     rollback of v7
 …     (use --limit/--since; 40 total)
```

**audit:**
```
 TIME      ACTOR            ACTION        ENV          DETAIL
 2m ago    @alice           pull          production   v13 · 203.0.113.4
 1h ago    token:gha-prod   pull          production   v13 · 140.82.x.x
 3h ago    @pruth           team.remove   -            @mallory
```

**status:**
```
 Account   @pruth @ https://sync.example.com
 Project   myapp · default env: dev
 Remote    v13 by @alice (12m ago)
 Local     .env: ahead, 2 keys changed since last pull
 Next      dotsync diff · dotsync push
```

**errors:**
```
error: not authorized to push to myapp/production (role: viewer)
hint:  ask an owner to run `dotsync team role pruth member`
```

### 10.4 Concrete changes

1. Real tables with `text/tabwriter`, or lipgloss (already listed in `go.mod` as an indirect dependency). Truncate to terminal width and drop columns before wrapping.
2. Key-level diff and typed confirmation for protected environments. Add `--yes` for CI.
3. Spinner for network operations. Finish each line with ✓/✗.
4. `-q/--quiet`, `-v/--verbose`, `--json`, `--no-color`.
5. `login`: auto-open the browser, copy the code to the clipboard, show a countdown. Detect stale sessions (B6).
6. `status`: show in sync / ahead / behind / diverged using the stored digest and the cheap `/head`.
7. `history`/`audit`: `--limit`, `--since`, `--action`, server-side filters, real pagination.
8. `tokens create`: `--quiet` prints only the token, `--copy` copies it, `--ttl` sets expiry.
9. `dotsync doctor`: server reachability, TLS validity, clock skew, keyring availability, key file permissions (0600), CLI version vs latest.
10. Un-hide `completion` and document it.
11. Enable VT processing on Windows (`golang.org/x/sys/windows`), and honor `CLICOLOR_FORCE`.
12. Exit codes: 0 ok, 1 generic, 2 usage, 3 auth, 4 conflict, 5 network.
13. `integrate github-actions` should print a complete workflow (including the CLI install step and `dotsync run` rather than writing `.env` to the runner disk).

---

## 11. Web dashboard review and redesign

### 11.1 What is wrong today

| Area | Issue |
|---|---|
| Architecture | Live UI is one 99-line, ~92 KB blob of minified JS in a Go string. Behavior is patched at runtime (`projectCore`, `addCore`, `tokenCore` wrappers), static text is overwritten from JS, and 4+ stacked `<style>` blocks fight with `!important`. The legacy UI (3,067 lines) is still in the source. There is no way to unit-test or lint it. |
| Access control | Fails to load for non-admins (R5) **[verify]**. No session token (S4). |
| Theme | At least one stacked "monochrome" layer maps `--green`, `--amber` and `--red` to the same near-black and sets `.btn.danger` to normal ink. Success, warning, error and destructive actions may be indistinguishable **[verify in rendered page]**. |
| Secret exposure | The editor shows every value in plaintext in a textarea. There is no mask, no reveal timer and no clipboard clearing. |
| Safety | Production edits look identical to dev. Destructive actions use native `confirm()`. Push has no review step beyond an optional diff icon. |
| Concurrency | No conflict detection (B9). The SSE endpoint is dead (D11). |
| Content | The in-app Guide is ~12 KB of marketing-style text with the factual errors listed in §4. |
| Privacy | Loads Google Fonts despite the "everything local" promise. |
| Accessibility | Inline `onclick` handlers in the legacy UI, native dialogs, no documented focus management or reduced-motion handling. |
| Errors | All failures arrive as HTTP 500 with heuristically rewritten text (B16). |

### 11.2 Priorities

**P0 (do first)**
- Session token plus Host and Sec-Fetch checks (S4). Remove or lock `/api/run`.
- Fix non-admin loading: load sections independently, hide the sections the role cannot see.
- Delete `legacyDashboardHTML` and the changelog card. Replace the Guide with a short, accurate help panel.

**P1 (product quality)**
1. **Masked values by default.** Click to reveal one value, auto-hide after 15 s, "Reveal all" with a visible timer. Auto-clear the clipboard after 30 s. Blur the editor when the tab is hidden.
2. **Structured editor.** Table view (KEY | masked value | change chip | last changed by) alongside the raw view. Validate: duplicate keys, invalid names, trailing whitespace, unbalanced quotes. Support multi-line values.
3. **Review & Push modal.** Key-level diff (+/~/−) with masked values, counts, and the target env and version. Typed confirmation for `production`.
4. **Environment safety.** Color-code environments. Production gets a persistent red top bar and a red environment pill.
5. **Conflict handling.** Poll `/head` every 30–60 s. If remote moves, show a banner with "View changes" and "Pull and merge". Block push on a stale base (requires B9).
6. **History.** Timeline with per-version key-level diff and a restore preview. "Restore" shows exactly what will change.
7. **Team.** Inline role descriptions that match the server's rules. Remove the fictional "pending" state. "Remove" opens a rotation checklist (S10).
8. **Tokens.** Show scope, creator, last used (with IP once S3 is fixed), and a TTL. One-time reveal card with copy button and "won't be shown again".
9. **Audit.** Human sentences (`alice pulled production v13 · 12m ago · 203.0.113.4`), server-side filters, pagination, CSV export, collapse password fetches.
10. **States.** Real loading skeletons, empty states with the next action, and distinct 401/403/404/409/5xx handling.

**P2 (architecture)**
- Move to `web/index.html`, `app.js`, `app.css` embedded with `//go:embed`. Use ES modules or esbuild. Add a CSP nonce and remove `'unsafe-inline'`. Self-host fonts.
- Component-ize (render functions per view) and remove the runtime patch chains.
- Add Playwright smoke tests per role: owner, admin, member, viewer. Cover load, pull, push with diff, rollback, token create, 403 handling.
- Accessibility pass: contrast (WCAG AA), visible focus, keyboard navigation, dialog focus traps, `prefers-reduced-motion`, semantic landmarks, `aria-live` toasts.

### 11.3 Wireframe: secrets view

```
┌───────────────────────────────────────────────────────────────────────────┐
│ ▌PRODUCTION  myapp ▾  ·  v13 by @alice · 12m ago      [Pull] [Review & Push]│  ← red bar on prod
├──────────────┬────────────────────────────────────────────────────────────┤
│ Secrets      │ 🔍 filter keys…                    [Table | Raw]   [Reveal ⏱]│
│ History      │ ┌────────────────────────┬──────────────┬─────────┬───────┐ │
│ Team         │ │ KEY                    │ VALUE        │ STATUS  │ BY    │ │
│ Tokens       │ │ DATABASE_URL           │ ••••••••••   │ ~ edited│ you   │ │
│ Audit        │ │ STRIPE_WEBHOOK_SECRET  │ ••••••••••   │ + new   │ you   │ │
│ Workspace    │ │ SENTRY_DSN             │ ••••••••••   │         │ @bob  │ │
│              │ └────────────────────────┴──────────────┴─────────┴───────┘ │
│ ✓ Local-only │  ⚠ Remote moved to v14 (1 change) — [View changes] [Pull]    │
│  session     │                                                             │
└──────────────┴────────────────────────────────────────────────────────────┘
```

### 11.4 Replace the Guide

Keep a short help panel that is always true:

- Pull before you edit. Review before you push.
- Tokens are read-only for one environment.
- Removing a member does not change secrets they already saw: rotate them.
- Docs link: *(your README or site)*.

---

## 12. Remediation plan

### Phase 0: this week (trust blockers)

| Task | Refs |
|---|---|
| Make README, CLI help and Guide match reality (or ship the fixes). Remove claims about HMAC replay protection, audited team changes, zero-knowledge, scanner. | §4 |
| Lock the dashboard: session token, Host/Sec-Fetch checks, CSP nonce, remove or gate `/api/run` | S4 |
| Delete `legacyDashboardHTML` | D1 |
| Service-token principal, project binding, route allowlist, audit attribution | S3 |
| Docker `--env KEY` | S5 |
| Delete `--local` | S7 |
| Server-side device flow | S2 |
| Refresh grace window and client lock | S6 |
| Body size limits | S12 |

### Phase 1: next 2–4 weeks (stability and efficiency)

| Task | Refs |
|---|---|
| `/head` endpoint, per-process password cache, stop per-push pubkey PUT | R1–R3 |
| Non-admin dashboard load | R5 |
| Proper dotenv parser (use or delete the strict parser) | B1, B22 |
| Optimistic concurrency (`base_version`), atomic version insert | B9, B10, R10 |
| Atomic file writes, symlink refusal, non-TTY pull behavior | B4, B5 |
| Fix `diff`, `.gitignore` logic, login dead-end, logout semantics | B2, B3, B6, B7 |
| Audit: log everything, paginate, filter server-side | S17 |
| Trusted-proxy IP handling | S9 |
| Remove billing remnants, drop plan columns via a new migration | D5–D8 |
| Tests: authorization matrix (route × principal × role), crypto round-trips, parser table tests, refresh-race test | §13 |
| CI: `go vet`, `staticcheck`, `gosec`, `govulncheck`, `-race`, build matrix incl. arm64 | S15, B12 |

### Phase 2: next quarter (architecture and trust)

| Task | Refs |
|---|---|
| Per-member key wrapping (age/X25519), rotation on removal | S1, S10 |
| AAD binding, signed tuple, key pinning (TOFU), reject unsigned by default | S8 |
| Minisign-signed releases, embedded public key, downgrade guard, timeouts | S15 |
| Service-token TTL, IP allowlist option, last-used IP | S3 |
| Web UI rewrite to embedded static assets with tests | §11 |
| Terminal UI redesign (tables, preview, `--json`, doctor) | §10 |
| `SECURITY.md`, disclosure policy, third-party review of the crypto design | S16 |
| Ship `dotsync scan` plus pre-commit hook, or remove it from the README | §4 |

---

## 13. Test plan (what to write first)

**Authorization matrix** (highest value): table-driven test over every route × {owner, admin, member, viewer, non-member, service-token(same project), service-token(other project), service-token(other env), unauthenticated} with expected status codes. This single test would have caught S3 and R5.

**Crypto**
- Encrypt/decrypt round-trip. Wrong key fails. Tampered ciphertext, nonce or AAD fails.
- Ciphertext from env A must not decrypt as env B (after AAD).
- Signature verify/reject, including key mismatch and stripped signature.

**Auth**
- Refresh rotation: concurrent refresh with the same token (grace window), real reuse after the window (full revocation), expired token.
- JWT: wrong alg, expired, tampered claims.

**Parser**
- Multi-line PEM, quotes, `export`, inline comments, escapes, CRLF, empty values, duplicate keys, invalid names.

**CLI**
- `pull` non-TTY without `--force` exits non-zero. Atomic write leaves the old file intact on failure. Symlink target refused.
- `injectContainerEnv` never emits values.
- `ensureGitignore` with `.env.example`, `.environment`, negations.

**Dashboard (Playwright)**
- Per-role load. Session-token enforcement (request without header returns 403). Host-header rejection. Masked values by default. Production confirmation. Conflict banner.

**Concurrency**
- Two parallel pushes: one succeeds, one returns 409 with `base_version`. No 500s.

**CI gates:** `go test -race ./...`, `staticcheck`, `gosec`, `govulncheck`, and a build matrix covering linux/darwin/windows × amd64/arm64.

---

## 14. What I did not find (to be fair)

- No SQL injection: every user-controlled value goes through placeholders.
- No hardcoded secrets in the source files I read.
- Constant-time comparison is used for HMAC (`hmac.Equal`).
- JWT parsing restricts the algorithm family (HMAC) and checks the token's validity.
- Refresh-token handling is the strongest part of the server.
- Nonces are random per encryption, and 12-byte GCM nonces are safe at this message volume.

---

## 15. Appendix: quick patches

**Docker env (S5)**
```go
envFlags := make([]string, 0, len(secrets)*2)
for k := range secrets {
    envFlags = append(envFlags, "--env", k)
}
```

**Atomic write with symlink refusal (B5)**
```go
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
    if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
        return fmt.Errorf("%s is a symlink; refusing to overwrite", path)
    }
    tmp, err := os.CreateTemp(filepath.Dir(path), ".dotsync-*")
    if err != nil { return err }
    defer os.Remove(tmp.Name())
    if err := tmp.Chmod(perm); err != nil { return err }
    if _, err := tmp.Write(data); err != nil { return err }
    if err := tmp.Sync(); err != nil { return err }
    if err := tmp.Close(); err != nil { return err }
    return os.Rename(tmp.Name(), path)
}
```

**Whole-line `.gitignore` check (B3)**
```go
func hasIgnoreEntry(content, entry string) bool {
    for _, line := range strings.Split(content, "\n") {
        l := strings.TrimSpace(line)
        if l == entry || l == "/"+entry { return true }
    }
    return false
}
```

**Body limit middleware (S12)**
```go
func LimitBody(n int64) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            r.Body = http.MaxBytesReader(w, r.Body, n)
            next.ServeHTTP(w, r)
        })
    }
}
```

**AAD binding (S8)**
```go
aad := []byte("dotsync/v1|" + projectID + "|" + env + "|" + strconv.Itoa(version))
ciphertext = gcm.Seal(nil, nonce, plaintext, aad)
// decrypt: gcm.Open(nil, nonce, ciphertext, aad)
// sign:    ed25519.Sign(priv, sha256(aad || ciphertext))
```

**Refresh grace window (S6), sketch**
```sql
ALTER TABLE refresh_tokens ADD COLUMN rotated_at timestamptz, ADD COLUMN replaced_by text;
-- On rotate: mark old (revoked=true, rotated_at=now(), replaced_by=new_id).
-- On reuse: if now() - rotated_at < interval '30 seconds' → return the same successor pair;
--           else revoke the whole token family and require login.
```

---

*End of report. Every item cites the file or function where I observed it. Items marked **[verify]** should be confirmed by running the code.*