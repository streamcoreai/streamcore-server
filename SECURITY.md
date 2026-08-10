# Security Policy

streamcore-server sits on the network path between untrusted callers and your AI
providers. It terminates WebRTC, runs a STUN/TURN server, parses SDP, RTP and
Opus from anyone who can reach `/whip`, executes plugin code, and holds provider
API keys in memory. Reports against that surface are taken seriously.

## Reporting a vulnerability

**Do not open a public issue, discussion, or pull request for a security
issue.**

Use GitHub private vulnerability reporting: the **Security** tab →
[**Report a vulnerability**](https://github.com/streamcoreai/streamcore-server/security/advisories/new).
The report stays private to you and the maintainers until an advisory is
published.

If the issue is in a client SDK, the SIP bridge, the plugin SDKs, or the ESP32
firmware, report it on that repo under [`streamcoreai`](https://github.com/streamcoreai).
If you're unsure, report it here and we'll route it.

Include:

- affected commit or released version
- your `config.toml` shape with **all secrets redacted** — provider names and
  feature flags matter, keys never do
- reproduction steps, ideally a minimal SDP, RTP capture, or client script
- what an attacker gains: crash, unauthorized session, key disclosure, traffic
  interception, resource exhaustion

Test against your own instance. Please don't scan hosted demo deployments, and
don't access, modify, or exfiltrate other people's session data.

## What to expect

| Stage | Target |
|---|---|
| Acknowledgement | 3 business days |
| Initial assessment and severity | 10 business days |
| Fix or documented mitigation | depends on severity; we'll keep you updated |
| Public advisory | coordinated with you, within 90 days of the report |

We'll credit you in the advisory unless you'd rather stay anonymous. There is no
paid bug bounty.

## Supported versions

Pre-1.0 and moving fast. Security fixes land on `main` and in the next release —
there are no backported patch branches. If you're on an older tag, upgrading is
the fix. Pin your version in production and watch releases.

## In scope

- Authentication bypass on `/whip` or the short-lived token endpoint when JWT
  auth is enabled
- Remote crash, panic, or memory exhaustion reachable from a WebRTC peer,
  including malformed SDP, RTP, and Opus payloads
- Abuse of the built-in STUN/TURN server as an open relay or amplifier
- Session isolation failures — reading, injecting into, or hijacking another
  peer's session, transcript, or DataChannel events
- Disclosure of provider API keys or JWT signing secrets via logs, error
  responses, DataChannel events, or `/health`
- Plugin runtime issues that grant a plugin more than the server already grants
  it, or manifest handling that loads unintended code from untrusted input
- Prompt-injection paths that reach beyond the model — where injected content
  causes tool or plugin execution the operator did not authorize

## Out of scope

- **Installed plugins are trusted code.** They run with the server's privileges
  by design; the runtime is not a sandbox. "A malicious plugin can read the
  server's environment" is expected behaviour. Audit what you install.
- Misconfiguration we already document against: JWT auth disabled on a public
  address, committing `config.toml` (it is gitignored), or exposing TURN with
  static shared credentials.
- Denial of service requiring privileged network position or unbounded traffic
  volume against one instance. Session state is in-memory and single-process
  today — a known architectural limit tracked in the README roadmap, not a
  security finding.
- Vulnerabilities in upstream providers (OpenAI, xAI, Deepgram, and so on).
  Report those to the provider — though we do want to hear about it if
  StreamCore mishandles their credentials or responses.
- Code under `plugins/` samples and anything illustrative rather than deployed.
- Missing hardening headers, best-practice suggestions, and scanner output with
  no demonstrated impact.

## Deploying safely

- **Enable JWT auth on `/whip`** for anything internet-reachable, with
  short-lived tokens. It is optional, and off means anyone who can reach the
  endpoint can open a session against your provider keys.
- **Keep credentials out of the repo.** `config.toml` is gitignored; prefer
  environment variables or a secrets manager in production.
- **Terminate TLS in front of the server**; use `https`/`wss` end to end.
- **Rotate any key** you have pasted into a log, issue, or CI output.
- **Treat plugins as dependencies**: pin them, review them, and don't load
  manifests from sources you don't control.
- **Watch spend as a security signal.** An unauthenticated WHIP endpoint shows
  up as a provider bill before it shows up anywhere else.
