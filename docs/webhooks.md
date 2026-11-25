# Webhooks: events in and out

Settings › Webhook connects Crewbase to other systems, per team. The plugins are `@crewbase/webhooks` and `@crewbase/webhooks-api`. The older `POST /api/hooks/{id}` trigger for routines and workflows still works as before.

## Inbound

An inbound endpoint is an address a service such as GitHub, GitLab, Sentry, Linear or Stripe sends its webhooks to:

```
POST /api/webhooks/ingest/<endpoint id>                 this computer
POST https://<relay>/v1/hooks/<token>                   through the relay (opt-in, see below)
```

Each request goes through these steps in order. Nothing wakes an agent before step 1 passes.

1. **Verify.** The signature is checked with the provider's own scheme, in constant time.

   | Provider | Checked |
   |---|---|
   | github | `X-Hub-Signature-256` (HMAC-SHA256 of the body) |
   | gitlab | `X-Gitlab-Token` |
   | sentry | `Sentry-Hook-Signature`, `Sentry-Hook-Timestamp` (±5 min) |
   | linear | `Linear-Signature`, `webhookTimestamp` (±1 min) |
   | stripe | `Stripe-Signature` `t=…,v1=…` (±5 min) |
   | hmac-sha256 | `X-Crewbase-Signature-256` `t=…,v1=…` (Crewbase's own, ±5 min) |
   | bearer | `Authorization: Bearer <secret>` |

   The secret lives in the vault (`vault:webhooks/<id>`). It is never stored in the settings and never shown to agents. A body that does not verify is not kept.
2. **De-duplicate.** A delivery id seen in the last 48 hours is answered and dropped. The id comes from `X-GitHub-Delivery`, a Stripe event id, and so on.
3. **Match.** The event type must be in the endpoint's list, for example `pull_request` or `issue.*`. The event must also meet its condition: a small expression over the JSON with no code, such as `action == "opened" && pull_request.base.ref == "main"` or `labels contains "urgent"`. The condition can use:
   - operators: `== != in contains > < >= <= && || !`
   - dotted paths, with `[n]` for list items
   - `header.<Name>` to read a header
   - `event` for the event type
4. **Hold back.** A cooldown per key (e.g. `repository.full_name`) and a rate limit per minute (30 by default) stop alert storms from waking agents again and again.
5. **Fan out.** The event goes to any number of targets:
   - **a channel:** a message to its owner or to the @member you chose
   - **a routine:** a check that stays silent with `NO_ISSUES` when nothing is wrong
   - **a workflow:** a run, with the prompt as `{{input}}`

What the agents receive is the person's instruction, with `{{path}}` placeholders filled from the event (quoted). After it comes the event itself, fenced as `EXTERNAL EVENT DATA` and labelled as data, not instructions. The work it starts runs in sandbox mode, or read-only if you choose. It never runs in host mode.

Replies: `200` when the event was handled or deliberately not acted on (filtered, duplicate, cooldown), `401` when the signature fails, `404` for an unknown or disabled endpoint.

### Through the relay

A computer without a public address can receive webhooks through its relay. Turn on 经中继接收公网事件 on the endpoint to get a `/v1/hooks/<token>` address. The relay:
- forwards the raw headers and body to the host over its existing outbound link
- answers with the host's status
- rate-limits each token (120 requests a minute) and caps bodies at 1 MB

Unlike device traffic, the sender cannot encrypt end to end, so **the event passes through the relay in plain text**, over TLS in transit. That is why the address is opt-in per endpoint and its token can be reset. The host still verifies every signature. A relay operator can switch the intake off with `crew-relay --no-hooks`.

## Example: GitHub issues and pull requests

A walkthrough that has an agent review new pull requests and triage new issues.

**1. A public address.** GitHub must reach this computer, and `127.0.0.1` is not reachable from outside. Either:
- use the relay: connect to one in 设置 › 主机与设备 › 远程连接, then turn on 经中继接收公网事件 under the endpoint's 限流与权限. Crewbase runs no public relay; you need your own.
- or use a tunnel (cloudflared, ngrok) to `127.0.0.1:7788`, with `https://<tunnel>/api/webhooks/ingest/<endpoint id>` as the address. This exposes the whole port. The other routes still need the token, but the relay is the cleaner way.

**2. One endpoint per kind of work.** Reviewing a PR and triaging an issue need different instructions, so make two endpoints. In 设置 › Webhook › 新建入站, pick GitHub and:
- leave 密钥 empty. One is generated and shown once; copy it.
- under 事件, pick `pull_request` for one endpoint and `issues` for the other.
- set 条件 to keep only the actions you care about:
  ```
  action in ["opened", "reopened", "ready_for_review"] && !pull_request.draft && pull_request.base.ref == "main"
  ```
  ```
  action == "opened" || (action == "labeled" && issue.labels contains "bug")
  ```
  `labels contains "bug"` matches GitHub's label objects by name.
- under 交给, pick a channel. The event goes to its owner, or to the member you choose.
- write 让 Agent 做什么. `{{path}}` takes a value from the event, and the whole event is attached after it:
  ```
  {{repository.full_name}} has a new PR #{{pull_request.number}}: {{pull_request.title}}
  {{pull_request.html_url}}
  Check out the branch, review it, and report the main problems in the channel.
  ```
  ```
  New issue #{{issue.number}}: {{issue.title}} ({{issue.html_url}})
  Decide whether it is a bug or a feature request, estimate its reach, and post the triage in the channel.
  ```
- for a busy repository, set a cooldown under 限流与权限 with `pull_request.number` as its key, so pushes to the same PR are handled once.

**3. GitHub's side.** In the repository, open Settings → Webhooks → Add webhook and fill in:
- **Payload URL:** the address from step 1.
- **Content type:** `application/json`. With the form encoding, the body is not JSON, and neither conditions nor `{{path}}` can read it.
- **Secret:** the one you copied.
- **Which events:** Let me select individual events, then Pull requests or Issues.

**4. Check it.**
- GitHub first sends a `ping`. 投递记录 shows it as 已过滤 · 不在关注的事件里（ping）, which means the address and the signature both work. 签名不符 means the secrets differ.
- Open a test PR or issue. It should show 已处理, and the agent starts in the channel.
- While adjusting a condition, open a delivery in 投递记录 and use 试运行 to see where it would go without running anything, then 重新处理. GitHub's Recent Deliveries can send one again too.

For the agent to comment or push on GitHub as well, give github.com a token under 推送凭据 in Git settings ([git](git.md)). Without one it reports in the channel only.

## Outbound

A subscription sends internal events to a URL:

| Event | When |
|---|---|
| `workflow.finished` | a workflow run ends (completed, failed, cancelled) |
| `routine.finished` | a routine run ends (quiet, attention, failed) |
| `approval.asked` / `approval.decided` | an agent waits for approval / it was decided |
| `agent.turn_ended` | an agent finished a turn |
| `channel.owner_changed` | a channel changed hands |
| `git.merged` / `git.review` | code was merged / a review changed ([git](git.md)) |
| `agent.emit` | an agent sent it with `webhook_emit` (asks first) |

Each delivery is JSON shaped like `{"id", "event", "time", "team", "data"}`, with these headers:

```
X-Crewbase-Event: routine.finished
X-Crewbase-Delivery: dl…
X-Crewbase-Signature-256: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>." + body)>
```

The receiver verifies the signature with the secret shown once when the subscription was made. A failed delivery is retried with backoff: 30 s, 1 min, 2 min … up to 1 h, for 8 attempts by default. A 4xx answer other than 408 or 429 is not retried. A delivery that gives up is kept as dead and can be sent again from 投递记录.

Deliveries only reach public addresses: this computer, private networks and link-local addresses (cloud metadata) are refused after the name is resolved. To deliver to your own network, list its ranges in 出站设置; only admins can change this.

## Tools

- `webhook_deliveries` (read): recent deliveries both ways and what happened, so an agent can find out why an event did or did not arrive.
- `webhook_emit` (publish, asks): send a notification to the subscriptions that take `agent.emit`.

## Teams and audit

Endpoints, subscriptions and deliveries belong to a team. Members see their team's and change them if they may work in it. The following are written to the audit log:
- `webhook/received`, `webhook/rejected`, `webhook/delivered`, `webhook/replayed`, `webhook/emitted`
- every saved or deleted endpoint and subscription
