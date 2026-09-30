# Desk

AdHunters Desk: ask for work by talking. A person writes what they want in
a conversation; Desk reads the apps to prepare it, proposes a plan, and after
the person's OK carries out each step through the same door the app's own
button uses. It can do what a teammate can do in the apps, and nothing a
person has to confirm. Words are in [GLOSSARY.md](../GLOSSARY.md#desk).

The design is the Desk design page; the research behind its limits is in
research/agent-guardrails/ in the project files.

## What runs

| Binary | Does | Listens |
|---|---|---|
| `desk-agent run` | Answers the conversations waiting for it, on Claude (`-turns`, 2 at once), and moves OK'd plans forward one step at a time. | ops on `OPS_ADDR` (9120) |
| `desk-web` | The pages under `/desk/`, in the Frame: conversations, plans to OK, choices, the to-do list, and the settings with the stop switch. | `127.0.0.1:8092`, ops on 9121 |

`desk-agent migrate` applies the migrations (the unit runs it before each
start). Units and example settings are in [deploy/](deploy/).

## How a request moves

1. A person writes in a conversation. Only they see it, and Desk acts for
   them, with their rights (their email goes to every app as who asked).
2. Desk takes a turn: the model gets what is new and may call its tools:
   every read in the [action catalog](../contract/actions/README.md), plus
   `propose_plan`, `plan_status`, `add_todo`, `todos` and `done_todo`.
   Reads only look, so they run at once.
3. Anything that changes something is a plan: a goal and up to 12 steps.
   A step is an action (a change or an ask from the catalog), a choice
   (the rows of a read, for the person to pick among) or a person's to-do.
   A step can take a value from an earlier one: the id a change returned,
   or the ids the person chose. Every input is checked against the catalog
   before the plan is shown, and again before the call.
4. The plan shows with every input. Only the conversation's person can OK
   it or say no, and the OK is bound to the plan as they saw it: a plan that
   changed since is not approved. A new plan replaces one still waiting.
5. desk-agent runs the plan in order:
   - **Action**: calls the app's function in `<app>_api` in the same
     transaction that records the call and moves the step, with
     `desk:step:<id>` as the origin, so a step never calls twice. Then it
     follows the app's view until one of the action's done or failed
     states. An ask ends when a person confirms it on the app's own screen
     (Launch's Send paused); Desk never confirms anything.
   - **Choice**: shows the rows with the ones Desk would pick and why (one
     more call to Claude), puts "Escolher" on the person's to-do list, and
     waits for their pick.
   - **Person**: puts the to-do on its holder's list and waits until it is
     done.

   Each step's news is an event in the conversation. When the plan ends,
   Desk takes a turn to say what came of it, with the links.

A step that waits is looked at again every 30 s; a pick or a to-do marked
done moves it at once. An app that refuses a call (its own check, a
constraint), or a function Desk may not call, fails the step and the plan
with the app's words; after any other error the step is tried again a
minute later.

## Limits that hold whatever the model says

- **The catalog is the only door.** Desk reaches an app only through what
  `contract/actions/<app>.json` lists, each one a view or function the app
  publishes. Confirming, turning spending on and raising bids or caps are
  never listed, so Desk can never be given them.
- **Taboola.** Every Taboola write goes through Launch, where a person
  confirms it; campaigns and ads are always made paused and only a person
  starts them, in Taboola. Headlines are always in English. Taboola's
  policies are warnings; the AI label is the person's choice.
- **Plans first.** Changes and asks run only as steps of a plan its person
  OK'd. Each action has a `per_day` cap on Desk's calls. Reads run in a
  read-only transaction, so a read changes nothing even if its view or
  function would.
- **Outside text is data.** Columns a read marks `outside` (a competitor's
  headline, a landing page's title) reach the model cut to 600 characters
  and marked as data, never as instructions.
- **Stop switch.** Anyone on the team can stop Desk on the settings page: no
  call to Claude and no step runs until someone starts it again. A person
  can also stop one conversation, which stops its plan.
- **Spending.** `daily_usd` caps Claude spending per UTC day: past it Desk
  says so and waits for the next day. `turn_calls` caps the calls to Claude
  in one turn; the last one must answer in words.
- **Nothing is rewritten.** Messages, the model's side of each conversation,
  every call to Claude and every call to an app are only ever added
  (triggers refuse the rest).

## Claude

Desk talks to Claude through the Go SDK, streaming, with adaptive thinking
at the effort in the settings. The instructions and tools are cached, and so
is the conversation up to its last message. The model's replies are stored
exactly as the API returned them and sent back unchanged, reasoning
included; each turn records the instructions and tools it was made under,
and turns made under older ones (before a deploy) go back without their
reasoning. If the API still refuses a reasoning block, the call is tried
once without any.

Each call's tokens and cost are in `desk.model_call`, and its cost goes to
`/metrics` (`ops.Spent("anthropic", …)`); a refusal for lack of credit
raises the out of credit alert.

## Settings

In `desk.setting`, changed on the settings page (each change records who
made it and when), read on every turn:

| Name | Default | |
|---|---|---|
| `stopped` | `false` | The stop switch. |
| `model` | `claude-opus-5-5` | |
| `effort` | `medium` | `low`, `medium`, `high`, `xhigh` or `max`. |
| `daily_usd` | `20` | Most spent on Claude in a UTC day. |
| `turn_calls` | `12` | Most calls to Claude in one turn. |

Environment:

| Name | Default | |
|---|---|---|
| `DATABASE_URL` | required | Both binaries; the session runs in UTC. |
| `ANTHROPIC_API_KEY` | required | desk-agent. |
| `DESK_WEB_ADDR` | `127.0.0.1:8092` | desk-web. |
| `OPS_ADDR` | `127.0.0.1:9100` | `/healthz`, `/metrics`: 9120 for desk-agent, 9121 for desk-web on a box. |
| `DESK_DEV_PERSON` | unset | desk-web on a laptop without Access: the email every visitor is. Never on a server. |

Who is asking is Cloudflare Access's `Cf-Access-Authenticated-User-Email`
header; desk-web listens on localhost behind the tunnel. Checking Access's
signed token as well is still to do. Forms posted from another site are
refused.

## What it reads and publishes

Reads and calls only what `contract/actions` lists, through each app's
`<app>_api`: the login holds each app's `<app>_api_read` role (Raposa, Spy
and Tracks today) and owns the `desk` schemas.

Publishes `desk_api` (granted to `desk_api_read`), with a copy of each
definition in [contract/sql/desk](../contract/sql/desk): `todo_v1`, the
to-do list, and `add_todo_v1`, for another app to put a to-do on someone's
list (Launch asking a person to start a pair in Taboola, say).

## Not done yet

- **Launch's and Create's actions.** Desk makes creatives and pairs once
  `contract/actions/launch.json` and `create.json` list them, after Launch
  and Create publish their `_api` functions (`launch_api.new_request_v1`
  and the like). Until then Desk reads Spy, Raposa and Tracks and asks for
  Raposa investigations.
- **Deploy.** Not deployed, and not in `platform/servers/setup.sh` yet.
  Deploying needs the owner's word, then: a `desk` login and the
  `desk_api_read` role, the `raposa_api_read`, `spy_api_read` and
  `tracks_api_read` roles granted to it, the two units, `ops_ports` lines
  (`desk-agent 9120`, `desk-web 9121`), the Claude key in
  `/etc/adhunters/desk-agent.env`, and in the tunnel the path `desk/*` on
  the apps' hostname to `http://localhost:8092`, behind the same Access
  application. Then Desk's `ready` becomes true in the Frame
  (`shared/frame/assets/core.js`).
- **Open decisions** (asked 2026-09-30; Desk is built on the first answer
  of each): its own app, not a panel in every app nor inside Launch; a
  person confirms Desk's pairs on Launch's review page, not in the chat;
  one to-do list for people and Desk; who confirms raising spend in Launch
  (Launch's rule; Desk never confirms).

## Run it

On a laptop, with a Postgres you can write to:

```
export DATABASE_URL=postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable
go run ./desk/cmd/desk-agent migrate
ANTHROPIC_API_KEY=sk-ant-... OPS_ADDR=127.0.0.1:9120 go run ./desk/cmd/desk-agent run
DESK_DEV_PERSON=you@example.com OPS_ADDR=127.0.0.1:9121 go run ./desk/cmd/desk-web
```

Then open http://127.0.0.1:8092/desk/. A read of an app whose `_api` is not
in that database fails, and Desk says so in the conversation.

## Tests

```
cd desk && PG_TEST_URL=postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable go test ./...
```

Each test gets its own database. The agent runs on a scripted model (no
calls to Claude), and plans run against a stand-in app (`internal/testdb`):
a brief made, a pick among its options, and a pair asked for and
confirmed on the app's side.
