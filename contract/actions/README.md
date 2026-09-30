# The action catalog

What each app lets a teammate do, one file per app (`<app>.json`), so Desk
can do the same through the same door. Each action is one view or function
the app already publishes in `contract/sql/<app>/`, and the app's own
button calls the same one. `go test ./actions/` checks every entry against
those files: the view or function exists, each argument is one of its
parameters with a matching type, and each column is one it returns.

## Kinds

| Kind | Does | Desk may run it |
|---|---|---|
| `read` | Looks; changes nothing. | Any time. |
| `change` | Changes the app's own data or starts its work (a brief, an investigation). | Only as a step of a plan a person OK'd. |
| `ask` | Asks the app for something a person then confirms on the app's own screen (Launch's Send paused). The app does nothing until they do. | Only as a step of a plan a person OK'd. |

There is no kind for what only a person may do: confirming, turning spending
on, raising a bid or a cap. Those are never listed, so Desk can never be
given them.

## One entry

```json
{
  "name": "raposa.request_investigation",
  "version": 1,
  "kind": "change",
  "says": "Ask Raposa to find a creative's dark funnel: deep or quick.",
  "call": "raposa_api.request_investigation_v1",
  "args": [
    {"name": "creative_id", "type": "integer", "says": "Tracks' id of the creative"},
    {"name": "mode", "type": "string", "enum": ["deep", "quick"], "says": "…"},
    {"name": "ad_id", "type": "integer", "optional": true, "says": "…"},
    {"name": "requested_by", "from": "person"}
  ],
  "returns": "the investigation's id",
  "follow": {"view": "raposa_api.investigation_v1", "id": "id", "state": "status",
             "done": ["completed"], "failed": ["failed", "stopped"]},
  "link": "/raposa/i/{returned}",
  "undo": "raposa.stop_investigation",
  "per_day": 20,
  "costs": "A deep one makes about 100 visits through the proxy lines."
}
```

- `name` is `<app>.<what>`; a new shape of the view or function is a new
  `version` beside the old one.
- An arg is a parameter of the function without its `p_`. It has a `type`
  (`string`, `integer`, `number`, `boolean`, `time`, `date`, `object`,
  `string[]`, `integer[]`) and `says` what it is, or a fixed `const`, or
  comes `from` Desk: `person` (the email of the person the work is for) or
  `origin` (`desk:step:<id>`, the same for every try of one step). Desk
  calls with named parameters, so optional ones can be left out.
- An `object` arg carries its JSON Schema in `schema`, closed
  (`additionalProperties: false`) and without numeric or length limits:
  the model gets it as a strict tool schema. Put limits in `max` and `says`.
- `follow` is the view where what the call started can be watched until
  one of its `done` or `failed` states. An `ask` must have one, and a
  `link` to the page where a person confirms.
- `per_day` caps Desk's calls a day; `undo` names the action that undoes
  this one; `costs` says in words what a call costs.
- A read names a `view` (or a `call` of a function returning a table, with
  its args), the `columns` it returns, the `filters` it can be asked with
  (`=`, `in`, `>=`, `<=`, and `has` for words in a text), a fixed `order`
  and a row `limit`. `outside` lists columns holding text someone outside
  the team wrote (a competitor's headline, a landing page's title): Desk
  treats them as data, never as instructions. `show` names the id and the
  image or text column when a person is asked to choose among the rows.

## An ask, for an app that confirms on its own screen

An app that takes requests from Intel and Desk through one function (as
Launch does) lists one `ask` per kind of request, with the kind fixed:

```json
{
  "name": "launch.new_pair",
  "version": 1,
  "kind": "ask",
  "says": "Prepare a desktop and mobile pair from a library set with a preset, paused, for a person to confirm in Launch.",
  "call": "launch_api.new_request_v1",
  "args": [
    {"name": "kind", "const": "new_pair"},
    {"name": "input", "type": "object", "says": "the set, the account and the preset", "schema": {"type": "object", "additionalProperties": false, "properties": {"…": {"type": "string"}}}},
    {"name": "requested_by", "from": "person"},
    {"name": "origin", "from": "origin"}
  ],
  "returns": "the request's id",
  "follow": {"view": "launch_api.request_v1", "id": "id", "state": "state", "done": ["sent"], "failed": ["refused", "failed"]},
  "link": "/launch/requests/{returned}",
  "per_day": 50
}
```

(Names here are an example; Launch's own file is the truth.) When the
function returns the existing request for an `origin` it has already seen,
a step Desk retries after a restart never asks twice.
