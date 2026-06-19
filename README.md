# 🔐 DotSync

> *End-to-end encrypted secret sync for dev teams — uncrackable military-grade security.*

**DotSync** syncs `.env` secrets across your team. The server stores only encrypted blobs — it **never** sees your raw secret values. Now powered by **Argon2id**, making it practically uncrackable.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Version](https://img.shields.io/badge/version-v0.2.0-yellow.svg)
![Security](https://img.shields.io/badge/security-Argon2id%20%7C%20AES--256--GCM-success.svg)

---

## ⚡ Quick Start

### 1. Install & Authenticate

```bash
# Install the CLI
go install github.com/Pruthviraj36/dotsync/cli/dotsync@latest

# Login with GitHub
dotsync login
```

### 2. Initialize a Project

In your project directory, run `init`. You will be prompted to create or enter a **Project Password**. This password is used to derive the encryption key for your whole team.

```bash
dotsync init
# Prompts for: Project slug, Project Password, Default env
```

### 3. Sync Secrets

**Team Mode (Default)**
Encrypts using the shared Project Password. Anyone on your team with the password can pull these secrets.
```bash
dotsync push          # Encrypt and upload your .env for the team
dotsync pull          # Download and decrypt to .env
```

**Personal Mode (The `--local` flag)**
Encrypts using your personal GitHub Access Token. Only **you** can pull and decrypt these secrets. Perfect for personal overrides.
```bash
dotsync push --local  # Encrypt for personal use only
dotsync pull --local  # Decrypt a personal push
```

### 4. Advanced Commands

```bash
dotsync version                      # See the current CLI version (commit hash)
dotsync push --env production        # Push to a specific environment
dotsync push --file .env.prod        # Push a specific file
dotsync diff                         # See what changed vs remote
dotsync history                      # View version history
dotsync envs                         # List all environments
```

---

## 🔒 Uncrackable Security Model (Argon2id)

DotSync uses state-of-the-art cryptography to ensure your secrets are safe, even if the database is compromised.

1. **Argon2id Key Derivation**: We use the winner of the Password Hashing Competition (Argon2id) to derive your encryption keys. It is *memory-hard*, rendering GPU/ASIC brute-force attacks practically impossible.
2. **AES-256-GCM Encryption**: The actual encryption is done using AES-256 in Galois/Counter Mode, the industry standard for Authenticated Encryption.
3. **Zero-Knowledge**: The server never sees your keys, your password, or your raw `.env` data. It only sees AES-256-GCM encrypted bytes and random nonces.
4. **HMAC Request Signing**: Every single CLI request is signed with an HMAC-SHA256 signature to prevent tampering in transit.
