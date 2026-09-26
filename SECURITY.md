# Security policy

**English** | [简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Please don't report security problems in public issues. Use [private vulnerability reporting](https://github.com/SherlockGougou/itemory-agent/security/advisories/new) instead, and include if you can:

- the affected version (shown under **Diagnostics** in the web console, or `"version"` in `/api/v1/health`);
- steps to reproduce, or a proof of concept;
- what an attacker could achieve.

## Supported versions

Only the latest release receives security fixes. The `:1` image tag always points to it.

## How access works

- **The iPhone app** uses a device token that it receives when it scans a pairing QR code. Pairing codes are single-use, expire after 5 minutes and are rate-limited. Device tokens can be revoked at any time under **Pairing & devices**.
- **The web console** uses an administrator account. The password is stored only as a PBKDF2-HMAC-SHA256 hash, and a signed-in session is kept in an HttpOnly cookie. Opening the pairing window and viewing or revoking paired devices require this session. Scans and settings can be managed either from the console or from a paired app.
- The two credentials are separate: the app never receives the administrator password, and a device token can't be used to sign in to the console.
- Photo folders are mounted read-only. The agent only writes to its data folder, runs as a non-root user, and the templates drop all Linux capabilities.

## Recommendations

- Use the agent inside your home network. Don't forward port 8787 from your router to the internet; use a VPN to reach it from outside.
- The console uses plain HTTP. On a network you don't fully trust, put it behind your NAS's reverse proxy with HTTPS, as described in [HTTPS and remote access](docs/configuration.md#https-and-remote-access).
- Keep the agent up to date, see [Upgrading](docs/upgrading.md#upgrade).
