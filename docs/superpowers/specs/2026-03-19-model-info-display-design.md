# Model Information Display

## Problem

agentsview parsers (Claude, Codex, Gemini) already extract per-message
model information and persist it in the `messages.model` column. Copilot
sessions also contain model data but the parser does not yet extract it.
The frontend does not display model information anywhere.

## Goals

1. Add model extraction to the Copilot parser
2. Display model information in the frontend for all agents that provide
   it
3. Keep the approach simple — derive session-level model from existing
   per-message data, no schema changes

## Non-Goals

- Adding a `main_model` column to the sessions table (can be added
  later as a cached derivation if client-side computation proves too
  slow)
- Unifying parser extraction logic (each agent format is different;
  shared `ComputeMainModel()` in Go is unnecessary when the frontend
  computes it)
- Showing model info in the session list sidebar
- Model name shortening or vendor prefix stripping

## Design

### Backend: Copilot Parser

The Copilot JSONL format includes `session.model_change` events that
carry a `newModel` field. The parser tracks these to know which model is
active.

Changes to `internal/parser/copilot.go`:

- Add `currentModel string` field to `copilotSessionBuilder`
- Handle `session.model_change` events: extract `newModel` from payload,
  update `b.currentModel`
- Stamp `b.currentModel` on each assistant `ParsedMessage.Model` when
  building messages

No other backend changes. No schema migration. No `MainModel` field on
`ParsedSession`. No `ComputeMainModel()` helper in Go. The per-message
`model` field already flows through the existing sync engine and DB
layer.

### Frontend: Compute Main Model

New function `computeMainModel(messages: Message[]): string` in a
frontend utility module:

- Filter to assistant messages with non-empty `model`
- Count occurrences of each model string
- Return the most frequent (alphabetic tie-break), or `""` if none

This runs client-side over the already-loaded messages array. No
additional API calls.

### Frontend: Session Detail Header

In `SessionBreadcrumb.svelte`, derive the main model from the messages
store. When non-empty, display it as a badge in the session header. Show
the full model string (no shortening).

### Frontend: Per-Message Badges

In `MessageContent.svelte`, compare `message.model` against the
computed main model for the active session. Show a badge with the full
model string only when:

- The message is from an assistant (not user)
- The message has a non-empty model
- The model differs from the session's main model

This highlights model switches without cluttering single-model sessions.

Per-message badges are only shown in the top-level session view. Inside
subagent inline expansions, no per-message badges are rendered — the
subagent toggle header already shows model info.

### Frontend: Subagent Toggle Header

In `SubagentInline.svelte`, derive the child session's main model from
its messages and display it in the toggle header. Full model string, no
shortening.

Subagent messages are only loaded when the user expands the subagent
inline. The model badge appears after expansion, once messages are
available to compute from. This is the natural behavior — no extra API
calls or schema changes needed.

### Removed from Original Branch

The original branch included several features that are dropped in this
design:

- `shortModelName()` utility — display full model strings instead
- `owningSession` prop on `MessageContent` — unnecessary without
  subagent per-message badges
- `ComputeMainModel()` in Go (`types.go`) — computed client-side
  instead
- `MainModel` field on `ParsedSession` — not needed
- `main_model` column on sessions table — no schema migration
- Shutdown metrics accumulation in Copilot parser — unnecessary
  complexity; `session.model_change` events are sufficient
- `shutdownModelCounts` and backfill logic — removed with shutdown
  metrics

## Future Path

If client-side main model computation becomes a performance concern, or
if model info is needed in the session list sidebar, a `main_model`
column can be added to the sessions table and populated from existing
message data without a full resync:

```sql
ALTER TABLE sessions ADD COLUMN main_model TEXT NOT NULL DEFAULT '';

UPDATE sessions SET main_model = COALESCE(
  (SELECT model FROM messages
   WHERE messages.session_id = sessions.id
     AND role = 'assistant' AND model != ''
   GROUP BY model ORDER BY COUNT(*) DESC, model LIMIT 1),
  ''
);
```

The same approach works for PostgreSQL.

## Testing

### Go Tests

Copilot parser tests (`copilot_test.go`):

- Single model session: `session.model_change` sets model, all
  assistant messages get it
- Model switch mid-session: two `session.model_change` events,
  messages before and after get correct models
- No model data: session without `session.model_change` events,
  messages have empty model

### Frontend Tests

`computeMainModel()` tests:

- Empty message array returns `""`
- Single model returns that model
- Mixed models returns most frequent
- Tie-break is alphabetic
- User messages and empty models are ignored

Component tests:

- `SessionBreadcrumb`: badge appears when main model is non-empty,
  hidden when empty
- `MessageContent`: badge appears when message model differs from main
  model, hidden when same, hidden for user messages, hidden when no
  model data
- `SubagentInline`: toggle header shows child session model
