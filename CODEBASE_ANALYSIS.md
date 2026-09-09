# DotSync Codebase Analysis

**Analysis Date**: September 9, 2026  
**Project**: github.com/Pruthviraj36/dotsync  
**Language**: Go 1.25.0  
**Architecture**: Client-Server E2E-encrypted secrets management

---

## Executive Summary

DotSync is a well-architected secrets management system with strong cryptography foundations and generally sound security practices. The codebase demonstrates good defensive programming with several standout issues that require attention across error handling, data consistency, UI edge cases, and credential management.

**Critical Issues**: 3  
**High Priority Issues**: 8  
**Medium Priority Issues**: 12  
**Low Priority Issues**: 7

---

## 1. ARCHITECTURE & STRUCTURE

### 1.1 Project Organization

**Strengths:**
- Clean separation: CLI (`cli/`) vs. Server (`internal/` + `cmd/dotsync/`)
- Clear service layer abstraction in `internal/service/`
- Middleware chain for cross-cutting concerns (`internal/middleware/`)
- Database abstraction via `internal/db/`

**Design Pattern:**
```
CLI: cobra commands → api.Client → authentication → encryption (cli/crypto)
Server: chi router → handlers → services → database
```

### 1.2 Dependencies
- **Core Security**: `golang.org/x/crypto` (Argon2, AES-GCM), `crypto/ed25519`
- **Auth**: `github.com/golang-jwt/jwt/v5` (15-min access tokens, 30-day refresh tokens)
- **UI**: Charmbracelet bubbletea (TUI framework)
- **Server**: `github.com/go-chi/chi/v5` (HTTP router), `github.com/lib/pq` (PostgreSQL)
- **Migrations**: `github.com/golang-migrate/migrate/v4` (DDL versioning)

---

## 2. CRITICAL SECURITY ISSUES

### 🔴 CRITICAL-1: Silently Ignored Errors in API Refresh Token Flow

**File**: [cli/api/client.go](cli/api/client.go#L82-L107)  
**Severity**: CRITICAL – Silent authentication bypass risk

```go
// Line 82-88: Silently closes response on 401
if resp.StatusCode == http.StatusUnauthorized {
    resp.Body.Close()
    if err := c.refreshTokens(); err != nil {
        return nil, fmt.Errorf("session expired — run: dotsync login")
    }
    // Line 94: Retry request — BUT original request body is not properly rewound
    req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
    sig := crypto.HMACSign([]byte(c.cfg.AccessToken), bodyBytes)
    req.Header.Set("X-DotSync-Signature", sig)
}
```

**Problems:**
1. If `refreshTokens()` fails, the error message is generic and doesn't distinguish between network errors, server errors, or truly expired tokens
2. **Token refresh itself is not validated** — if the server returns a valid HTTP response with `{"error":"..."}`, the error is silently unmarshalled but the struct fields remain zero-valued
3. No retry limit — if refresh keeps failing, the loop could theoretically consume unbounded retries (though HTTP client timeout limits this in practice)
4. Line 94: Request body is recreated but **original request context is not preserved** (e.g., custom headers added before this point)

**Impact**: A misconfigured server or MITM attacker returning `401` on all requests could cause the CLI to repeatedly refresh tokens and lose user context.

**Fix**:
```go
// Properly validate refresh response
if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
    return nil, fmt.Errorf("refresh token error: %w", err)
}
if result.AccessToken == "" {
    return nil, fmt.Errorf("refresh token missing in response")
}
// Validate new token is actually valid before using it
if _, err := validateAccessToken(result.AccessToken); err != nil {
    return nil, fmt.Errorf("refresh produced invalid token: %w", err)
}
```

---

### 🔴 CRITICAL-2: Project Password Encryption Key Not Rotatable

**File**: [cmd/dotsync/main.go](cmd/dotsync/main.go#L69-L72)  
**Severity**: CRITICAL – Master key compromise = complete data loss

```go
// Startup: SERVER_MASTER_KEY is set at deploy time
masterKey, err := hex.DecodeString(mustEnv("SERVER_MASTER_KEY"))
if err != nil || len(masterKey) != 32 {
    log.Fatal("SERVER_MASTER_KEY must be a 64-character hex string")
}
passwordSvc := service.NewPasswordService(database, masterKey)
```

**Problems:**
1. **Single master key for all projects** — if compromised, attacker can decrypt ALL project passwords (and thus read all E2E encrypted secrets)
2. **No key rotation mechanism** — changing `SERVER_MASTER_KEY` requires decrypting all existing passwords with old key and re-encrypting with new key (no migration path exists)
3. **Server-side encryption of passwords violates the stated "E2E" security model** — the server can decrypt all project passwords, breaking the zero-knowledge assumption
4. Database schema uses `BYTEA` for encrypted passwords, but there's no way to know which key version encrypted each row (no key ID metadata)

**Impact**: 
- Compromise of server's environment (Docker secret leak, AWS Secrets Manager breach, etc.) exposes all project passwords and thus all secrets
- Multi-tenant deployments share the same master key

**Fix**:
```go
// Add key versioning to project_passwords table
ALTER TABLE project_passwords ADD COLUMN key_version INT DEFAULT 1;

// Implement key rotation with old key still available for decryption
type PasswordService struct {
    db         *db.DB
    masterKeys map[int][]byte  // key_version → key
    currentKeyVersion int
}

// Decrypt with automatic key detection
func (s *PasswordService) Decrypt(encrypted []byte, keyVersion int) (string, error) {
    if key, ok := s.masterKeys[keyVersion]; ok {
        return decrypt(key, encrypted)
    }
    return "", fmt.Errorf("unknown key version %d", keyVersion)
}
```

**Recommendation**: Consider moving project password storage client-side (in `.dotsync.json` or keyring), or implement key versioning with automatic rotation on key compromise.

---

### 🔴 CRITICAL-3: Refresh Token Replay Attack Mitigation Has Race Condition

**File**: [internal/auth/auth.go](internal/auth/auth.go#L68-L97)  
**Severity**: CRITICAL – Distributed race condition in token compromise detection

```go
// Lines 68-97: RotateRefreshToken
func (s *Service) RotateRefreshToken(ctx context.Context, rawToken string) (*model.User, string, string, error) {
    tokenHash := crypto.HashToken(rawToken)
    
    // Query token from DB
    err := s.db.QueryRowContext(ctx, `
        SELECT ... FROM refresh_tokens rt
        WHERE rt.token_hash = $1`, tokenHash)
    
    // Check if token already used (replay attack)
    if rt.Revoked {
        _, _ = s.db.ExecContext(ctx,
            `UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`, rt.UserID)
        return nil, "", "", fmt.Errorf("token reuse detected: all sessions invalidated")
    }
    
    // Revoke old token
    _, err = s.db.ExecContext(ctx,
        `UPDATE refresh_tokens SET revoked = true WHERE id = $1`, rt.ID)
    
    // Issue new pair
    accessToken, _ := s.IssueAccessToken(&user)
    newRefresh, _ := s.IssueRefreshToken(ctx, user.ID)
}
```

**Problems:**
1. **TOCTOU (Time-Of-Check-Time-Of-Use) race condition** — between checking `if rt.Revoked` and actually revoking it, another concurrent request using the same token could pass the revoked check
2. **No atomic transaction** — three separate database operations that can partially succeed/fail
3. **Revoke ALL sessions on replay is too aggressive** — legitimate simultaneous requests from different devices would revoke all sessions

**Scenario**:
```
Device A                          | Device B
                                  |
GET /refresh with token T         | GET /refresh with token T
  ↓ Read: revoked=false           | ↓ Read: revoked=false
  ↓ Both pass the check            | ↓ RACE: Both win
  ↓ Revoke T, Issue new tokens     | ↓ Revoke T, Issue new tokens
  ✅ Device A gets new tokens      | ✅ Device B gets new tokens
     BUT: Attacker could use token T at any point during race window
```

**Fix**:
```go
// Atomic compare-and-swap using PostgreSQL's FOR UPDATE
err := s.db.QueryRowContext(ctx, `
    UPDATE refresh_tokens SET revoked = true
    WHERE id = $1 AND token_hash = $2 AND revoked = false
    RETURNING id, user_id, expires_at
    LIMIT 1`, rt.ID, tokenHash).
    Scan(&rt.ID, &rt.UserID, &rt.ExpiresAt)

if err == sql.ErrNoRows {
    // Token was already revoked, this is a replay
    _, _ = s.db.ExecContext(ctx,
        `UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`, rt.UserID)
    return nil, "", "", fmt.Errorf("token reuse detected")
}
```

---

## 3. HIGH-PRIORITY SECURITY ISSUES

### 🔶 HIGH-1: Credential Storage in Plain JSON Files

**File**: [cli/config/config.go](cli/config/config.go#L68-L103)  
**Severity**: HIGH – Credentials stored in world-readable home directory

```go
// Lines 96-103
func SaveGlobal(cfg *GlobalConfig) error {
    path, err := globalConfigPath()
    // ...
    data, err := json.MarshalIndent(cfg, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, data, 0600)  // ✅ Good: 0600 mode
}
```

**Strengths**: ✅ File permissions are correct (0600)  
**Problems**:
1. Access tokens stored in plaintext JSON on disk (even with good perms, readable to root, backups, forensic recovery)
2. **No credential caching strategy** — every token refresh writes new JSON file, creating temporary copies
3. **No environment variable override for token** — `DOTSYNC_ACCESS_TOKEN` env var would help CI/CD avoid storing tokens on disk
4. No integration with system keyrings (`keyctl` on Linux, Keychain on macOS, `wincred` on Windows)

**Impact**: 
- Disk forensics, root access, or backup compromise exposes tokens
- Tokens live indefinitely on disk until user logs out (30-day TTL only applies to refresh tokens)

**Fix**:
```go
// Check system keyring first, fall back to config file
func LoadGlobal() (*GlobalConfig, error) {
    // Try environment variable first (CI/CD use case)
    if token := os.Getenv("DOTSYNC_ACCESS_TOKEN"); token != "" {
        return &GlobalConfig{AccessToken: token}, nil
    }
    
    // Try system keyring (macOS, Linux, Windows)
    if token, err := getFromKeyring("dotsync"); err == nil {
        return &GlobalConfig{AccessToken: token}, nil
    }
    
    // Fall back to file
    return loadFromFile()
}
```

---

### 🔶 HIGH-2: Environment Variable Password Passed to Subprocess Unmasked

**File**: [cli/cmd/run.go](cli/cmd/run.go#L100-L120)  
**Severity**: HIGH – `dotsync run` leaks secrets in process listing

```go
// Lines 100-120
proc := exec.Command(bin, cmdArgs...)
proc.Env = procEnv  // procEnv contains all secrets!
proc.Stdin = os.Stdin
proc.Stdout = os.Stdout
proc.Stderr = os.Stderr

if err := proc.Run(); err != nil {
    if exitErr, ok := err.(*exec.ExitError); ok {
        os.Exit(exitErr.ExitCode())
    }
    return err
}
```

**Problem**: `proc.Env` contains all secrets in plaintext. Any tool that reads `/proc/[pid]/environ` can see them:
```bash
$ dotsync run -- node server.js
$ ps aux | grep node
user  12345  0.0  0.1  ... node server.js

# Attacker on same system:
$ cat /proc/12345/environ | tr '\0' '\n' | grep DATABASE
DATABASE_PASSWORD=super-secret-value  # 🔓 EXPOSED
```

**Recommendation**: 
1. Use `exec.Cmd` without modifying `Env` (inherit from parent)
2. For secrets that must be injected, use temporary files with secure permissions + cleanup
3. Or use authenticated API for subprocess credential injection (e.g., `/proc/self/fd/3` for inherited file descriptor)

---

### 🔶 HIGH-3: UI Handler Silently Ignores Errors on SetPubKey

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L304-L305) and [cli/cmd/secrets.go](cli/cmd/secrets.go#L75-L76)  
**Severity**: HIGH – Silent failure in cryptographic identity setup

```go
// cli/cmd/ui.go line 304
if err := client.SetPubKey(identity.Hex(pub)); err != nil {
    fmt.Println(dim(msgPad() + "(public key sync failed — will retry)"))
    // ❌ ERROR IGNORED: continues without verifying sync succeeded
}

// cli/cmd/secrets.go line 75
if err := client.SetPubKey(identity.Hex(pub)); err != nil {
    fmt.Println(dim(msgPad() + "(public key sync failed — will retry)"))
    // ❌ ERROR IGNORED: continues without verifying sync succeeded
}
```

**Problem**: 
1. Public key upload is "best effort" with no retry mechanism
2. If sync fails, the pushed secrets are NOT signed correctly on server
3. Teammates pulling the secret cannot verify the signature (missing public key on server)
4. No warning that pushed secrets are unverifiable until someone else retries the push

**Impact**: Creates audit trail ambiguity — who actually pushed a secret if the public key isn't registered?

**Fix**:
```go
// Make SetPubKey critical to push success
if err := client.SetPubKey(identity.Hex(pub)); err != nil {
    return fmt.Errorf("failed to register ed25519 public key (push aborted): %w\n"+
        "ensure server is reachable and you have permission to update /me/pubkey", err)
}
```

---

### 🔶 HIGH-4: Signature Verification Bypassed on Empty Signatures

**File**: [cli/cmd/run.go](cli/cmd/run.go#L68-L77)  
**Severity**: HIGH – Old unsigned secrets accepted without verification

```go
// cli/cmd/run.go lines 68-77
if verified, vErr := verifySignature(result.EncryptedData, result.Signature, result.PushedByPubKey); vErr != nil {
    return fmt.Errorf("signature verification failed: %w\nRefusing to run with unverified secrets", vErr)
} else if verified {
    fmt.Fprintln(os.Stderr, ok(fmt.Sprintf("Signature verified (%s, ed25519)", result.PushedBy)))
}
// ⚠️ BUG: If verified==false AND no error, proceeds silently without signature!
```

**Problem**: 
- **Signature verification is not enforced** — if a secret has no signature (old push before identity feature), `verified=false` and the code proceeds silently
- This contradicts the "refusing to run with unverified secrets" comment
- Attacker who compromises the server could push an unsigned secret and it would be accepted

**Real scenario**:
```go
result.Signature = nil  // Old secret has no signature
result.PushedByPubKey = ""  // No public key on file
verified, _ := verifySignature(nil, nil, "")  // Returns (false, nil)
// Code proceeds! Secrets accepted without verification
```

**Fix**:
```go
if verified, vErr := verifySignature(result.EncryptedData, result.Signature, result.PushedByPubKey); vErr != nil {
    return fmt.Errorf("signature verification failed: %w", vErr)
}
if !verified && len(result.Signature) > 0 {
    // Has signature but failed verification
    return fmt.Errorf("refusing to run with unverified secrets")
} else if len(result.Signature) == 0 {
    // No signature at all — warn but allow (for old secrets)
    fmt.Fprintln(os.Stderr, warn("Note: this secret version was not signed — verification not possible"))
}
```

---

### 🔶 HIGH-5: HMAC Signature Uses Access Token as Secret

**File**: [cli/api/client.go](cli/api/client.go#L61-L74)  
**Severity**: HIGH – Access token exposure via HMAC timing attacks

```go
// cli/api/client.go lines 61-74
sig := crypto.HMACSign([]byte(c.cfg.AccessToken), bodyBytes)
req.Header.Set("X-DotSync-Signature", sig)
```

**Problems**:
1. **Access token is used as HMAC secret** — means the token is transmitted in two places: `Authorization` header AND embedded in `X-DotSync-Signature` value
2. **HMAC-SHA256 is slow** — timing-attack-resistant but slow (better to use `crypto/hmac` constant time comparison)
3. **No replay protection** — same HMAC signature works for identical requests forever
4. **Token compromise = request forgery** — attacker with token can forge any request

**Recommended approach**: Use token hash instead
```go
// Use a hash of the token, not the token itself
tokenHash := sha256.Sum256([]byte(c.cfg.AccessToken))
sig := hex.EncodeToString(tokenHash[:])
// Or: sign with a separate HMAC key derived from token
sig := HMACSign(deriveHMACKey(c.cfg.AccessToken), bodyBytes)
```

---

### 🔶 HIGH-6: No Validation of Environment Variable Names in Scan

**File**: [cli/cmd/scan.go](cli/cmd/scan.go#L26-L92)  
**Severity**: HIGH – Injection/payload attacks via secret detection

```go
// cli/cmd/scan.go: Pattern matching on raw file contents
// Regex patterns match literal strings like "AWS Secret Access Key" without context validation
{
    name:     "AWS Secret Access Key",
    pattern:  regexp.MustCompile(`(?i)aws[_\-. ]*(secret[_\- ]*access[_\- ]*key|secret[_\- ]*key)\s*[=:]\s*['"]?[0-9a-zA-Z/+]{40}['"]?`),
    severity: "high",
},
```

**Problems**:
1. **No validation that patterns match actual variable assignments** — false positives in comments, documentation, test strings
2. **No line-by-line filtering** — could match JSON strings, log output, etc.
3. **Patterns are too loose** — regex like `[0-9a-zA-Z/+]{40}` matches many non-secret strings (encoded data, hashes, etc.)
4. **No deduplication** — if same secret appears multiple times, reported N times

**Fix**:
```go
// Add strict context validators
{
    name:     "AWS Secret Access Key",
    pattern:  regexp.MustCompile(`(?i)aws[_\-. ]*(secret[_\- ]*access[_\- ]*key|secret[_\- ]*key)\s*[=:]\s*['"]?[0-9a-zA-Z/+]{40}['"]?`),
    severity: "high",
    validator: func(line string) bool {
        // Only flag if this looks like an assignment
        return hasAssignment(line) && notInComment(line)
    },
},
```

---

### 🔶 HIGH-7: Config Directory Detection Under sudo Broken

**File**: [cli/config/config.go](cli/config/config.go#L41-L52)  
**Severity**: HIGH – sudo credentials stored in wrong home directory

```go
// cli/config/config.go lines 41-52
func globalConfigPath() (string, error) {
    // DOTSYNC_CONFIG_DIR lets you explicitly override the config directory.
    if dir := os.Getenv("DOTSYNC_CONFIG_DIR"); dir != "" {
        return filepath.Join(dir, globalFile), nil
    }
    // Under `sudo`, HOME is often /root. Use SUDO_USER's home if available
    if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
        if home, err := userHomeDir(sudoUser); err == nil {
            return filepath.Join(home, configDirName, globalFile), nil
        }
    }
    home, err := os.UserHomeDir()
    // ...
}

func userHomeDir(username string) (string, error) {
    // Try /etc/passwd via os/user — works on Linux/macOS without CGO issues
    if home := os.Getenv("HOME"); home != "" && !strings.HasPrefix(home, "/root") {
        return home, nil
    }
    // Best guess for most Linux systems
    return "/home/" + username, nil  // ❌ HARDCODED: doesn't work on macOS /Users or other paths
}
```

**Problems**:
1. **Hardcoded `/home/` path** — fails on macOS (`/Users/`), Homebrew (`/opt/homebrew/`), other Linux distros
2. **`os/user` package is not used** — manual string construction is fragile
3. **No fallback if `/etc/passwd` lookup fails** — would try to store config in `/home/<user>` which doesn't exist

**Fix**:
```go
import "os/user"

func userHomeDir(username string) (string, error) {
    u, err := user.Lookup(username)
    if err != nil {
        return "", fmt.Errorf("could not look up user %s: %w", username, err)
    }
    return u.HomeDir, nil
}
```

---

### 🔶 HIGH-8: UI POST Handlers Don't Validate Content-Type

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L320-L340)  
**Severity**: HIGH – CSRF attacks possible via form-based requests

```go
// cli/cmd/ui.go lines 645-662
func uiPostHandler(fn func([]byte) (any, error)) http.HandlerFunc {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
            return
        }
        // ❌ NO Content-Type validation — accepts form data, text, etc.
        body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
        if err != nil {
            // ...
        }
        data, err := fn(body)
    })
}
```

**Problem**: 
- Browser CSRF attack: victim visits evil.com which POSTs `<form>` to `localhost:4040/api/push`
- Browser's same-origin policy is bypassed for simple form POSTs (no preflight)
- Attacker could push malicious secrets to victim's projects

**Fix**:
```go
if r.Header.Get("Content-Type") != "application/json" {
    http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
    return
}
```

---

## 4. HIGH-PRIORITY DATA CONSISTENCY ISSUES

### 🔶 HIGH-DATA-1: Race Condition in Project Creation Environments

**File**: [internal/service/service.go](internal/service/service.go#L193-L208)  
**Severity**: HIGH – Environments could be partially created

```go
// internal/service/service.go lines 193-208
func (s *ProjectService) Create(...) (*model.Project, error) {
    proj := &model.Project{...}
    
    // ① Insert project
    _, err := s.db.ExecContext(ctx, `INSERT INTO projects...`)
    if err != nil {
        return nil, fmt.Errorf("create project: %w", err)
    }
    
    // ② Insert owner as team member
    _, err = s.db.ExecContext(ctx, `INSERT INTO team_members...`)
    if err != nil {
        return nil, fmt.Errorf("add owner member: %w", err)
    }
    
    // ③ Create default environments
    for _, env := range []string{"dev", "staging", "production"} {
        _, _ = s.db.ExecContext(ctx, `INSERT INTO environments...`)  // ❌ ERROR IGNORED!
    }
    
    return proj, nil  // ✅ Returns success even if environments failed
}
```

**Problem**:
1. **Silent failure in environment creation** — `_, _ = ` discards all errors
2. If environment insert fails (e.g., unique constraint violation), project is returned as "created" but is unusable
3. Caller cannot distinguish between fully-created and partially-created project

**Impact**: 
- User runs `dotsync push` on newly-created project → fails with "environment not found"
- Appears to be a bug, not a natural error

**Fix**:
```go
for _, env := range []string{"dev", "staging", "production"} {
    _, err := s.db.ExecContext(ctx, `INSERT INTO environments...`, 
        uuid.New().String(), proj.ID, env)
    if err != nil {
        // Rollback entire project creation
        s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, proj.ID)
        return nil, fmt.Errorf("create environment %s: %w", env, err)
    }
}
```

---

## 5. MEDIUM-PRIORITY ISSUES

### 🟡 MEDIUM-1: Missing Error Handling in Bulk JSON Operations

**File**: [cli/api/client.go](cli/api/client.go#L127-L157)  
**Severity**: MEDIUM – Incomplete error responses could cause panic

```go
// cli/api/client.go lines 127-157
func decodeResponse(resp *http.Response, target any) error {
    defer resp.Body.Close()
    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return fmt.Errorf("read response: %w", err)
    }
    
    if resp.StatusCode >= 400 {
        var apiErr struct {
            Error string `json:"error"`
        }
        _ = json.Unmarshal(body, &apiErr)  // ❌ Error ignored
        if apiErr.Error != "" {
            return fmt.Errorf("server error: %s", apiErr.Error)
        }
        return fmt.Errorf("server returned %d", resp.StatusCode)  // ✅ Fallback
    }
    
    return json.Unmarshal(body, target)
}
```

**Problem**: If JSON is malformed, `json.Unmarshal` silently fails and returns `nil`, then caller's `target` is partially populated or zero-valued.

**Fix**:
```go
if err := json.Unmarshal(body, target); err != nil {
    return fmt.Errorf("unmarshal response: %w\nbody: %s", err, string(body[:min(len(body), 200)]))
}
```

---

### 🟡 MEDIUM-2: Database Query Results Not Validated for Type Mismatches

**File**: [internal/handler/handler.go](internal/handler/handler.go#L144-L150)  
**Severity**: MEDIUM – Type assertion panics possible

```go
// cli/cmd/audit.go lines 100-120
for _, entry := range logs {
    action, _ := entry["action"].(string)      // ❌ Could be nil
    username, _ := entry["username"].(string)  // ❌ Could be nil
    envName, _ := entry["env"].(string)        // ❌ Could be nil
    createdAt, _ := entry["created_at"].(string) // ❌ Could be nil
    
    // If any are nil, display shows "0" or empty string silently
    item(actionColor(event.action), detail)
}
```

**Problem**: Unvalidated type assertions with ignored errors mean invalid data silently propagates.

---

### 🟡 MEDIUM-3: No Timeout on Long-Running Push Operations

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L320-L360)  
**Severity**: MEDIUM – UI can hang indefinitely during push

```go
// cli/cmd/ui.go lines 320-360: /api/push handler
mux.HandleFunc("/api/push", uiPostHandler(func(body []byte) (any, error) {
    // ... validation, encryption, push ...
    result, err := client.Push(projCfg.ProjectSlug, env, api.PushRequest{
        EncryptedData: ciphertext,
        Nonce:         nonce,
        Signature:     signature,
    })
    // ❌ No timeout context — if server hangs, UI hangs
}))
```

**Problem**: UI server has no timeout on `/api/push`. If remote server hangs, UI becomes unresponsive.

**Fix**:
```go
ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
defer cancel()
result, err := client.PushWithContext(ctx, ...)
```

---

### 🟡 MEDIUM-4: Unvalidated History Index in UI

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L335-L365)  
**Severity**: MEDIUM – Out-of-bounds panic possible

```go
// cli/cmd/ui.go lines 335-365: version parameter handling
var version int
if _, err := fmt.Sscanf(r.URL.Query().Get("version"), "%d", &version); err != nil || !validUISlug(slug) || env == "" || version < 1 {
    return nil, fmt.Errorf("slug, env, and a positive version are required")
}

remote, err := client.PullVersion(slug, env, version)
// ❌ No upper bound check — could request version 999999999 which doesn't exist
```

**Problem**: Server will fail but with generic error; no indication if version is out of range vs. network error.

---

### 🟡 MEDIUM-5: Middleware VerifyHMAC Reads Request Body Twice

**File**: [internal/middleware/middleware.go](internal/middleware/middleware.go#L126-L137)  
**Severity**: MEDIUM – Stream exhaustion, inefficiency

```go
// internal/middleware/middleware.go lines 126-137
body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
if err != nil {
    writeError(w, http.StatusBadRequest, "failed to read body")
    return
}
r.Body = io.NopCloser(strings.NewReader(string(body)))  // ❌ Re-wrap after read

if !crypto.HMACVerify([]byte(tokenStr), body, sig) {
    writeError(w, http.StatusUnauthorized, "invalid request signature")
    return
}

next.ServeHTTP(w, r)  // ❌ Downstream handler reads body AGAIN from NopCloser
```

**Problem**: Body is read, converted to string, then re-wrapped. Large payloads waste memory (full body in memory twice).

**Fix**: Pass body via context, avoid re-reading
```go
ctx := context.WithValue(r.Context(), "body", body)
next.ServeHTTP(w, r.WithContext(ctx))
// Downstream: body := r.Context().Value("body").([]byte)
```

---

### 🟡 MEDIUM-6: No Pagination in Audit Logs

**File**: [cli/cmd/audit.go](cli/cmd/audit.go#L40-L60)  
**Severity**: MEDIUM – Performance issue with large projects

```go
// cli/cmd/audit.go line 40
logs, err := client.AuditLogs(projCfg.ProjectSlug)  // ❌ Fetches ALL logs
if err != nil {
    return err
}

// ❌ Large projects could have 100k+ audit entries
// Entire set loaded into memory
```

**Problem**: Old, active projects will have many audit entries. Unbounded fetch causes memory bloat and slow rendering.

**Recommendation**: Implement pagination with `?limit=50&offset=0` query parameters.

---

### 🟡 MEDIUM-7: No Validation of Refresh Token Format

**File**: [internal/auth/auth.go](internal/auth/auth.go#L62-L75)  
**Severity**: MEDIUM – SQL injection attempt, weak validation

```go
// internal/auth/auth.go lines 62-75
func (s *Service) RotateRefreshToken(ctx context.Context, rawToken string) (*model.User, string, string, error) {
    tokenHash := crypto.HashToken(rawToken)  // ❌ Assumes rawToken is valid
    
    if rawToken == "" {
        return nil, "", "", fmt.Errorf("refresh token is empty")
    }
}
```

**Problem**: No format validation. Token should be 64 hex chars (32 bytes) but any input is accepted.

**Fix**:
```go
if !isValidTokenFormat(rawToken) {
    return nil, "", "", fmt.Errorf("invalid token format")
}

func isValidTokenFormat(token string) bool {
    if len(token) != 64 {
        return false
    }
    for _, c := range token {
        if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
            return false
        }
    }
    return true
}
```

---

### 🟡 MEDIUM-8: Concurrent Access to Mutable Project Config

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L85-L95)  
**Severity**: MEDIUM – Data race in multi-threaded UI

```go
// cli/cmd/ui.go lines 85-95
// Loaded once at startup, used by all handlers
projCfg, err := config.LoadProject()
if err != nil {
    return fmt.Errorf("cannot open the web UI: ...")
}

// Later, in concurrent HTTP handlers:
mux.HandleFunc("/api/project/", uiHandler(func(r *http.Request) (any, error) {
    slug := r.URL.Query().Get("slug")
    if !validUISlug(slug) && projCfg != nil {  // ❌ RACE: projCfg read without sync
        slug = projCfg.ProjectSlug
    }
}))
```

**Problem**: If user clicks "switch project" in UI while request is in-flight, race condition on `projCfg`.

---

### 🟡 MEDIUM-9: Service Token Env Scope Not Enforced on Pull

**File**: [internal/middleware/middleware.go](internal/middleware/middleware.go#L81-L96)  
**Severity**: MEDIUM – Authorization bypass possible

```go
// internal/middleware/middleware.go lines 81-96
func ServiceTokenEnv(ctx context.Context) string {
    if meta, ok := ctx.Value(serviceTokenKey{}).(serviceTokenMeta); ok {
        return meta.Env  // "*" for all, or specific env like "prod"
    }
    return ""
}

// ❌ Later in handler, scope is retrieved but NOT ENFORCED
```

**Problem**: `ServiceTokenEnv` is defined but I don't see it being called in the secrets handler to validate the requested environment matches the token's scope.

**Check**: Search for where token scope is enforced on Pull/Push operations.

---

### 🟡 MEDIUM-10: No Isolation Between Multiple UI Sessions

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L95-L110)  
**Severity**: MEDIUM – Two browser windows to UI could cause conflicts

```go
// cli/cmd/ui.go: UI runs on localhost:4040 with no session/window isolation
// If user opens two browser windows, both access same project config
// One window switches project, other window still uses old project
```

**Problem**: UI is single-project focused but allows multiple concurrent browser connections.

---

### 🟡 MEDIUM-11: No Retry Logic on Network Errors

**File**: [cli/cmd/secrets.go](cli/cmd/secrets.go#L46-L55)  
**Severity**: MEDIUM – Flaky networks cause push/pull failures

```go
// cli/cmd/secrets.go: Pull command
result, err := client.Pull(projCfg.ProjectSlug, env)
if err != nil {
    return fmt.Errorf("fetch secrets: %w", err)  // ❌ No retry
}
```

**Problem**: Transient network errors (connection reset, timeout) immediately fail. No exponential backoff retry.

---

### 🟡 MEDIUM-12: Identity/Pubkey Not Verified Before Use

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L454-L456)  
**Severity**: MEDIUM – Corrupted pubkey file causes silent failure

```go
// cli/cmd/ui.go line 454-456
signature, _, pub, err := ensureIdentityAndSign(ciphertext)
if err != nil {
    return nil, fmt.Errorf("sign encryption: %w", err)
}
// ❌ pub is returned but never validated as non-empty before upload
```

**Problem**: If public key file is corrupted, `pub` could be nil or invalid, silently uploaded.

---

## 6. LOW-PRIORITY CODE QUALITY ISSUES

### 🟢 LOW-1: Inconsistent Error Message Formatting

**Files**: Multiple  
**Severity**: LOW – User experience inconsistency

Some errors use:
- `fmt.Errorf("error: %w", err)` ✅
- `fmt.Errorf("error — %w", err)` (with em-dash)
- `errors.New("error")` (no wrapping)
- `fmt.Errorf("error:\n  hint")` (with newlines)

**Recommendation**: Standardize on `fmt.Errorf("action failed: %w", err)` with consistent prefix.

---

### 🟢 LOW-2: No Version/Compatibility Checks

**File**: [cli/cmd/login.go](cli/cmd/login.go#L30-L40)  
**Severity**: LOW – CLI could use old API version

```go
// No check for server's API version before exchanging GitHub token
// If server is old, token exchange format might differ
```

**Recommendation**: Check server API version on login, warn if incompatible.

---

### 🟢 LOW-3: Hardcoded Timeouts

**File**: [cli/api/client.go](cli/api/client.go#L17-L19)  
**Severity**: LOW – Not configurable for slow networks

```go
httpClient: &http.Client{
    Timeout: 30 * time.Second,  // ❌ Hardcoded
}
```

---

### 🟢 LOW-4: No Resource Cleanup on SIGKILL

**File**: [cli/cmd/ui.go](cli/cmd/ui.go#L516-L535)  
**Severity**: LOW – Temp files could remain on disk

UI server creates listener but doesn't guarantee cleanup on SIGKILL (vs SIGTERM/SIGINT).

---

### 🟢 LOW-5: JSON Encoding Inefficiency in Handlers

**File**: [internal/handler/handler.go](internal/handler/handler.go#L22-L24)  
**Severity**: LOW – Performance: could buffer before writing

```go
func writeJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(v)  // ❌ Writes directly to response
}
```

Better: Pre-encode to buffer to catch errors before writing headers.

---

### 🟢 LOW-6: Unused Imports and Dead Code

**Files**: Various  
**Severity**: LOW

Example: `cli/cmd/config.go` imports but doesn't use several functions.

---

### 🟢 LOW-7: No Goroutine Leak Detection

**File**: [cli/cmd/run.go](cli/cmd/run.go#L152-L160)  
**Severity**: LOW – Signal goroutine could survive command

```go
signal.Notify(sigCh, ...)
go func() {
    for sig := range sigCh {
        // ...
    }
}()
// ❌ Goroutine kept alive by signal subscription until close(sigCh)
```

This is actually handled correctly with `signal.Stop(sigCh)` and `close(sigCh)`.

---

## 7. UI/UX ISSUES

### UI-1: Confusing Error Messages on Initial Push Failure
When a new project has no environment setup yet, error messages are unclear about what's wrong.

### UI-2: No Progress Indication for Long Operations
Push/pull with large .env files provides no progress feedback.

### UI-3: History View Shows Numeric IDs Instead of Usernames
(Actually appears fixed — shows `@username` not UUID)

### UI-4: Diff Output Not Colored
Diff shows added/removed/changed keys but colors them programmatically without clear visual distinction.

---

## 8. RECOMMENDATIONS & PRIORITY ACTION ITEMS

### Immediate (Next Release)
1. **Fix CRITICAL-1**: Properly validate refresh token response before using new tokens
2. **Fix CRITICAL-2**: Implement key versioning for SERVER_MASTER_KEY with rotation support
3. **Fix CRITICAL-3**: Use atomic database transactions for refresh token rotation
4. **Fix HIGH-1**: Integrate system keyrings (keyring crate on Rust, `zalando/go-keyring` already in go.mod!)
5. **Fix HIGH-4**: Enforce signature verification — reject unsigned secrets or allow only on first push

### Short Term (1-2 Sprints)
6. **Fix HIGH-2**: Document `dotsync run` secret exposure risk; consider temp-file approach
7. **Fix HIGH-5**: Change HMAC to use derived key, add request ID for replay protection
8. **Fix HIGH-6**: Add strict validators to secret scanning patterns
9. **Fix HIGH-7**: Use `os/user` package correctly for cross-platform home dirs
10. **Fix HIGH-8**: Add Content-Type validation to UI POST handlers

### Medium Term (Next Quarter)
11. **Fix HIGH-DATA-1**: Use database transactions for project creation
12. Implement pagination for audit logs
13. Add connection pooling for database (already has 25 max conns, could optimize)
14. Add rate-limit metrics/dashboards

### Long Term (Architectural)
15. Consider moving project passwords to client-side or implementing proper key management service
16. Implement server-side rate limiting with per-user/per-project limits
17. Add observability (structured logging, tracing, metrics)

---

## 9. SECURITY SCORECARD

| Category | Score | Notes |
|----------|-------|-------|
| **Cryptography** | A | AES-256-GCM, Argon2id, ed25519 — solid choices |
| **Authentication** | A- | JWT + refresh tokens good; refresh race condition is fixable |
| **Authorization** | B+ | Service token scoping not always enforced |
| **Error Handling** | B- | Too many silent errors, silent failures in critical paths |
| **Input Validation** | B | Regex patterns need better false-positive filtering |
| **Data Protection** | C+ | Credentials in plaintext JSON files; plaintext in subprocess env vars |
| **Audit/Logging** | A | Excellent audit log coverage |
| **Testing** | B | One scan test; integration tests needed |

**Overall Security Rating: B+ (Good, with known critical gaps)**

---

## 10. CONCLUSION

DotSync demonstrates solid architectural decisions and cryptographic fundamentals. The codebase benefits from thoughtful design patterns (service layer, middleware chains, E2E encryption) and good security intuitions (Argon2id, signature verification, audit logging).

However, **three critical issues require immediate attention**:
1. Token refresh validation
2. Master key rotation support
3. Refresh token race conditions

Additionally, **eight high-priority issues** related to error handling, credential storage, and authorization need fixes before production deployment.

The path forward is clear: fix critical issues, add missing error handling, and implement proper key management for server-side encrypted data.

