# Hub protocol v0 schemas

JSON Schemas (draft 2020-12) for the messages of
`plans/system/hub-protocol.md`. The document and these files change in
the same PR.

| Schema | Message |
| ------ | ------- |
| `enroll.request` | `POST enroll` body |
| `enroll.response` | `POST enroll` 200 body |
| `config.response` | `GET config` 200 body |
| `status.request` | `POST status` body (heartbeat) |
| `status.response` | `POST status` 200 body |
| `runs.request` | `POST runs` body |
| `runs.response` | `POST runs` 200 body |
| `ws.command` | command, hub to daemon, on the WebSocket |
| `ws.ack` | acknowledgement, daemon to hub, on the WebSocket |
| `error` | body of every HTTP error response |

There is no WebSocket hello message: the upgrade request carries the
headers.

Each schema is self-contained: no `$ref` to another file. Every object
has `additionalProperties: false`.

## Examples

- `examples/<message>.json` and `examples/<message>.<variant>.json` are
  valid.
- `examples/invalid/<message>.<case>.json` are invalid; `<case>` names
  the rule they break.

An example belongs to the schema whose name is the longest prefix of the
file name.

## Go types and the contract test

The Go types in `internal/hubproto` are written by hand; nothing is
generated from these files. The repository has no JSON Schema validator
dependency. A contract test in `internal/hubproto` reads each schema
with `encoding/json` and checks:

- every property of every object (including `$defs`) is a JSON field of
  the matching Go struct, and every JSON field is a property;
- a property in `required` has no `omitempty` or `omitzero`, and an
  optional one has one of them;
- each `enum` equals the Go constants (outcome classes, loop states and
  details, command types, ack and error codes);
- every valid example decodes with `DisallowUnknownFields` and encodes
  back to equal JSON (both sides decoded to `any` and compared).

Conditional rules (`if`/`then`/`else`, `dependentRequired`, patterns,
bounds) are not checked by the contract test. The invalid examples
document them, and the fake hub enforces them where the protocol
depends on them. `ws.command` `args` maps to one Go struct per argument
shape: `$defs/loop_args` for the loop commands, an empty object for
`pause`, `resume` and `reload_config`.
