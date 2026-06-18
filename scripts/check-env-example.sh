#!/usr/bin/env bash
# scripts/check-env-example.sh
#
# Prevents accidentally committing real secrets into .env.example.
# .env.example must only ever contain placeholder values (things like
# <your_value_here>, sk_test_..., your_jwt_secret_here) — never live keys,
# passwords, or tokens.
#
# Install as a pre-commit hook:
#   ln -s ../../scripts/check-env-example.sh .git/hooks/pre-commit
#   chmod +x .git/hooks/pre-commit
#
# Or run manually any time before pushing:
#   ./scripts/check-env-example.sh

set -euo pipefail

FILE=".env.example"

if [ ! -f "$FILE" ]; then
  exit 0
fi

FAIL=0

# Flag values that look like real secrets rather than placeholders:
# long hex strings (JWT secrets), GitHub OAuth secrets (40 hex chars),
# GitHub OAuth client IDs (Ov23li... pattern), Stripe live/test keys with
# real-looking suffixes, and Postgres URLs with non-placeholder credentials.
PATTERNS=(
  '[A-Za-z0-9]{40,}'                         # long opaque tokens/secrets
  'Ov23li[A-Za-z0-9]+'                       # GitHub OAuth App client ID
  'sk_(test|live)_[A-Za-z0-9]{10,}'          # real Stripe secret key
  'whsec_[A-Za-z0-9]{10,}'                   # real Stripe webhook secret
  'postgresql://[a-zA-Z0-9_]+:[^<][^@]*@'    # DB URL with a real (non "<...>") password
)

while IFS= read -r line; do
  # Skip comments and blank lines
  [[ "$line" =~ ^[[:space:]]*# ]] && continue
  [[ -z "${line// }" ]] && continue

  for pattern in "${PATTERNS[@]}"; do
    if echo "$line" | grep -qE "$pattern"; then
      # Allow obvious placeholders even if they happen to match a pattern
      if echo "$line" | grep -qE '<.*>|your_|_here|xxx|\.\.\.'; then
        continue
      fi
      echo "❌ $FILE looks like it contains a REAL secret, not a placeholder:"
      echo "   $line"
      FAIL=1
    fi
  done
done < "$FILE"

if [ "$FAIL" -eq 1 ]; then
  echo ""
  echo "Replace real values in $FILE with placeholders before committing."
  echo "If a real secret was already pasted here, rotate it immediately —"
  echo "even removing it from this commit does not undo prior exposure"
  echo "in git history."
  exit 1
fi

exit 0
