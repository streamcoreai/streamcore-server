**English** | [简体中文](./developer-agent.zh-CN.md)

# Developer agent

Two optional integrations that let a voice conversation reach a real codebase:
GitHub, for reading CI and opening pull requests, and Codex, for investigating
and fixing things in an isolated checkout.

```text
User: "Why did StreamCore CI fail?"
   → github.latest_ci_failure → reduced log excerpt → the agent explains it

User: "Can Codex investigate?"
   → isolated worktree → Codex thread → root cause → the agent explains it

User: "Fix it."   → confirmation → Codex edits the worktree and runs the tests
User: "Open a PR."→ confirmation → branch pushed, pull request opened
```

Both are off by default. Neither can fail server startup, and neither touches
the media path: a Codex turn that takes four minutes does not delay a single
audio frame. Tool results are ordinary structured JSON — the agent turns them
into speech, and the existing display projector turns the answer into a card.

---

## GitHub

### Authentication

A **GitHub App**, not a personal access token. The App's private key is
exchanged for installation access tokens that live one hour and are scoped to
one repository and one permission set:

```text
app_id + private key  →  RS256 JWT (9 min)
                      →  POST /app/installations/{id}/access_tokens
                      →  installation token (1 h, one repo, least privilege)
                      →  GitHub API
```

Tokens are cached per repository *and* per permission set, so the read path can
never be served the write token, and are re-minted ten minutes before expiry.
The private key is read once at startup and never leaves the process — not into
a prompt, a tool result, the DataChannel, a log line, or a test snapshot.

### Permissions

Two sets, requested per operation:

| Operation | Permissions | Why |
|---|---|---|
| Read (investigation) | `actions:read`, `contents:read`, `pull_requests:read`, `checks:read`, `metadata:read` | Workflow runs and job logs; file and commit reads; pull request reads; check-run annotations |
| Write (pull request) | `contents:write`, `pull_requests:write`, `metadata:read` | Push a branch, open a pull request |

Not requested, ever: `administration`, `secrets`, `members`, `actions:write`,
`workflows`. This phase does not modify workflow files or repository settings.

### Repository access

Two conditions, both required:

1. the repository is in `github.repositories`
2. the App installation can actually reach it

Configuration is a statement of intent; the installation is the grant. Access is
proven by minting a read token for the repository — GitHub refuses to scope a
token to a repository the installation cannot see — and the result is cached
until GitHub answers 401 or 404.

### Tools

| Tool | What it does |
|---|---|
| `github.latest_ci_failure` | The most recent failed run, with failed jobs, the failing step, annotations, and a reduced log excerpt |
| `github.repo_status` | Default branch, open issues, state of the latest run |
| `github.workflow_run` | List recent runs, or describe one by id |
| `github.workflow_logs` | Reduced log for one run or one job |
| `github.pull_request` | Read one pull request |
| `github.commit` | Read one commit |
| `github.file` | Read one file at a ref |
| `github.diff` | Unified diff between two refs |
| `github.create_pull_request` | **Confirmation gated.** Push the developer task's branch and open a pull request |

### Log reduction

Raw job logs are megabytes; almost none of it explains a failure. Before
anything reaches the model the log is reduced deterministically:

- GitHub's per-line timestamps and ANSI escapes are stripped
- lines matching failure signals anchor a window of surrounding context —
  `FAIL`, panics, compiler `file:line:col:` messages, assertion and
  expected/actual output, `##[error]`, non-zero exit codes, stack traces
- recognised noise is dropped even when it lands inside a window: dependency
  downloads, progress percentages, `PASS`/`ok` lines, group markers
- a line repeated more than twice is collapsed
- the result is capped at **60 lines / 4000 bytes**, trimmed from the front,
  because CI writes its summary after it has finished failing

The same log always reduces to the same excerpt, so a failing turn can be
reproduced without re-running CI.

---

## Codex

### Authentication

Codex signs in with **your ChatGPT subscription**, through the official
"Sign in with ChatGPT" flow. StreamCore does not hold those credentials:

- there is no `api_key` in `[codex]`, and `OPENAI_API_KEY` is not read
- an API-key account is reported as *unauthenticated*, not used as a fallback
- when Codex asks StreamCore to supply refreshed ChatGPT tokens, StreamCore
  declines — Codex owns its own credential state
- `codex.status` reports availability, plan type, and busy state, and nothing
  else: no tokens, no account id, no credential paths

Sign in once, as the OS user that will run StreamCore:

```bash
codex login          # add --device-auth on a headless host
codex login status   # expect: Logged in using ChatGPT
```

Codex keeps that state in its own home (`~/.codex` by default). If StreamCore
runs as a dedicated service account, the sign-in has to happen **as that user** —
root's login does not apply to anyone else.

### Provider pinning matters

`model_provider` and `model` are passed on the Codex command line, not left to
`~/.codex/config.toml`. If your Codex defaults point at a third-party provider,
an unpinned StreamCore would run every developer task there and never touch your
ChatGPT plan, silently.

The model must be one your ChatGPT plan allows. The `gpt-5.1-codex` family is
rejected for ChatGPT accounts with *"not supported when using Codex with a
ChatGPT account"*; `gpt-5.6-terra` is the tested default.

### Process

One long-lived `codex app-server --stdio` child process, spoken to over
JSON-RPC. Threads are multiplexed over it — a new process is not started per
request. On shutdown, in-flight turns are interrupted through the official
`turn/interrupt` method and the child is closed cleanly; nothing is left behind.

### Isolation

Codex never touches the live server checkout.

```text
<workspace_root>/
  repo-cache/streamcoreai__streamcore-server/   one clone per repository
  worktrees/task_a1b2c3d4e5f60718/              one git worktree per task
```

Every path is generated internally from a repository name and a task id; no
path from voice or model input reaches the filesystem. Containment is checked
by resolving symlinks on the deepest existing ancestor, so `../`, an absolute
path, or a symlink planted mid-path are all refused — including on delete.

The clone is performed by StreamCore with a read-scoped token supplied through
`GIT_ASKPASS`, and the remote is then rewritten without credentials. Nothing
inside a worktree can authenticate to GitHub.

Sandboxes follow the task:

| Tool | Sandbox | Approval policy |
|---|---|---|
| `codex.analyze` | `readOnly` | `never` |
| `codex.fix`, `codex.test` | `workspaceWrite`, writable root = this task's worktree | `never` |

`never` means Codex does not ask and cannot escalate. Should an approval request
arrive anyway, it is declined: a spoken "fix the failing test" authorises edits
and test commands inside one worktree, and anything Codex needs to ask about is
outside it by definition. Pushing, reading host secrets, touching unrelated
repositories, and changing system configuration are all out of reach.

### Sessions and threads

```text
StreamCore session → developer task → Codex thread → isolated worktree
```

The mapping is keyed by conversation, so "have Codex investigate", "fix it",
"run the tests" and "show me the diff" all continue the same investigation, and
two callers can never share a worktree.

### Events

The Codex event stream is reduced to a handful of developer states —
`analyzing`, `editing`, `running_tests`, `analysis_ready`, `fix_ready`,
`failed`, `cancelled` — plus the touched files, the commands run, whether tests
passed, and Codex's own final message. Reasoning items are dropped outright and
never reach the voice model.

### Tools

| Tool | What it does |
|---|---|
| `codex.status` | Available, authenticated, busy |
| `codex.analyze` | Investigate read-only and explain the root cause |
| `codex.fix` | **Confirmation gated.** Edit the worktree and run the tests |
| `codex.test` | Re-run the tests for the current change |
| `codex.diff` | Show the current change |
| `codex.cancel` | Interrupt the running Codex turn |

There is no shell tool. Codex uses its own tools inside the worktree it was
given; the voice model cannot run commands.

`codex.cancel` is a deliberate command, not wired to voice barge-in. Talking
over the agent does not abandon a five-minute fix.

---

## Confirmation

`codex.fix` and `github.create_pull_request` set `confirmation_required` in the
plugin manifest, which the server enforces as a two-call gate. The first call
returns a prompt for the agent to read out and executes nothing; only a second
call carrying the server-issued token runs. The token is single-use, expires
after five minutes, and is bound to the conversation and to a hash of the
arguments — so a model cannot invent one, replay one, borrow another caller's,
or change the repository between the question and the answer. See
[Plugins → Confirmation](../../docs/plugins.md#confirmation).

## Separation

```text
                    StreamCore
              /                     \
      GitHub integration        Codex integration
             |                        |
       GitHub App auth        ChatGPT subscription
```

Codex receives an isolated checkout, the CI failure evidence, and a task. It
gets no GitHub token, no private key, and no ability to push. The GitHub side
receives a worktree path, a branch, a summary, and whether tests were run — it
gets no Codex credentials. Neither Go package imports the other; the adapter
between them lives in `main`, so there is no import edge to abuse later.

Pull request creation refuses unless: the repository is allowlisted and
installed, the branch is not protected (`main`, `master`, `production`,
`release`, `trunk`, `develop`), a diff exists, no obvious secret file is
touched, and the task's tests were actually run. There is no merge path, and
nothing pushes to a default branch.

## Failure modes

| Situation | What still works |
|---|---|
| GitHub misconfigured | Codex, voice, display |
| Codex not signed in | GitHub, voice, display — Codex tools return a clear "run `codex login`" error |
| Codex auth expired | Detected on the next turn; Codex marked auth-required, everything else unaffected |
| Both disabled | The server behaves exactly as it did before this existed |

## Cleanup

Abandoned worktrees are swept hourly, 24 hours after last use — long enough
that a worktree behind an open pull request survives the conversation that
produced it. A task StreamCore still owns is never removed, and no path outside
`workspace_root` can be, whatever it is handed.

## Setup

See [`config.toml.example`](../config.toml.example) for the annotated
`[github]` and `[codex]` blocks.

1. Create a GitHub App with the read permissions above (add the write pair only
   if you want pull request creation), generate a private key, and install it on
   the repositories you want reachable.
2. Note the installation id from the installation URL, or
   `GET /repos/{owner}/{repo}/installation`.
3. Store the PEM somewhere only the server can read (`chmod 600`), and point
   `github.private_key_path` at it.
4. Install Codex and sign in with ChatGPT as the OS user that runs StreamCore.
5. Set `github.enabled` and `codex.enabled`, then restart — plugins and tools
   are discovered at startup.
