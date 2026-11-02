# Security Policy

## Reporting a vulnerability

Do not open a public issue for a security vulnerability. Report it privately
through GitHub Security Advisories for this repository. Include the affected
version or commit, reproduction steps, impact, and any suggested mitigation.

Please avoid including real secrets or personal data in a report. We will
acknowledge reports as soon as practical and coordinate disclosure after a fix
is available.

## Production key handling

`SERVER_MASTER_KEY` protects project passwords at rest. Treat it as a
production secret and store it outside the repository. A database backup
without the master key should not be sufficient to decrypt project passwords;
the key without a database backup is not sufficient to recover them.

For rotation, configure `SERVER_MASTER_KEYS` as comma-separated
`version:64-hex-key` entries and set `SERVER_MASTER_KEY_VERSION` to the version
used for new writes. Keep the old key available while existing rows are
re-encrypted, verify recovery from a backup, then remove the retired key.

Removing a team member cannot revoke secrets they already downloaded. Rotate
the affected upstream credentials separately.

## Release verification

Release archives are verified against the published SHA-256 checksums by the
installer and `dotsync update`. Prefer releases from the project repository
and do not install archives or scripts from untrusted mirrors.

## Supported versions

Security fixes target the latest release on the default branch. Self-hosters
should keep the server and CLI updated together and test migrations against a
restored database copy before production rollout.
