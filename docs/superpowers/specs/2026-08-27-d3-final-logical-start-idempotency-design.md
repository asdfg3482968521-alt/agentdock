# D3 Final Logical-Start Idempotency Design

## Purpose and scope

`agent_start` currently admits every valid public request independently. This
change adds an optional caller-supplied logical-start key so an exact retry
admits one durable AgentDock agent and therefore one OpenCode native root.

The change is limited to the public `agent_start` path and its Agent manager
admission boundary. It does not alter Desktop, the frozen OpenCode runtime,
tray behavior, model selection policy, or workspace-root semantics.

## Public contract

`agent_start` accepts an optional `client_request_id` string. It uses the
existing repository spelling used by native supervisor operations, has a
minimum length of one and a maximum length of 256, and is not required for
legacy callers.

Successful responses add optional, backward-compatible booleans:

- `created=true`, `replayed=false` for the admission owner.
- `created=false`, `replayed=true` for an exact retry.

For a given authenticated session scope and key, a request with a different
effective request returns the new stable tool error `IDEMPOTENCY_CONFLICT`.

## Durable admission model

The existing Agent record persistence remains the durable source of truth. A
record created through keyed admission stores the client request id and a
SHA-256 fingerprint of a canonical effective request. Agent manager recovery
rebuilds an in-memory index from those persisted fields; the index is only an
acceleration structure, never a source of truth.

The admission key is `resolved session authority + client_request_id`. The
fingerprint excludes transient values and includes the request fields that
change start behavior: session identity, provider, resolved model, role,
credential reference, prompt, permission, request budget, and timeout.

Keyed admission is serialized per key. The manager checks a recovered or
live record before allocating a new Agent ID, persists the winning record
atomically, and only then makes it eligible for execution. A concurrent exact
duplicate attaches to that record. A persistence failure removes the tentative
in-memory reservation before returning its failure.

Records retain their regular agent execution state. A replay of a nonterminal
record attaches to that record; a replay of a terminal failed record returns
the same record and never creates a replacement. On restart, recovery rebuilds
the same key mapping before accepting a duplicate. Existing recovery behavior
may mark an interrupted nonterminal agent terminal, but its key remains bound
to that durable result.

## OpenCode native-root invariant

The native autonomous backend already passes the stable Agent ID as the
Native Bridge `ClientRequestID`. Native Bridge durably binds that value to one
OpenCode root per AgentDock session. Keyed admission guarantees duplicates
reuse the same Agent ID before backend execution begins, so they enter this
existing native binding mechanism once and cannot create roots before
deduplication.

## Error handling

Validation still happens before admission. For an admitted key, an identical
request always resolves to its original durable Agent record. A fingerprint
mismatch is deterministic and returns `IDEMPOTENCY_CONFLICT`; it contains no
request body or secret values. The existing error envelope maps the new
sentinel error to that code.

## Testing

Focused tests cover public schema exposure and golden hash updates, first
admission, sequential and concurrent replay, conflict, session-scope
isolation, restart reconstruction, pending attachment, terminal failure
replay, and no-key compatibility. Native-backend tests use a blocking/counting
fake to prove that duplicate public calls make one Agent admission and one
native-root start. Existing Agent, Session, Job, MCP-schema, OpenCode, and
OpenCode-native suites remain regression gates.
