# Model Information Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Display model information in the frontend for all agents that
provide per-message model data, and add Copilot model extraction.

**Architecture:** The branch currently has a complex implementation that
needs to be simplified. We reset the branch to origin/main and rebuild
with: (1) lean Copilot parser model tracking via `session.model_change`
events, (2) client-side `computeMainModel()` to derive session-level
model from loaded messages, (3) model badges in session header and on
off-main-model messages. No DB schema changes.

**Tech Stack:** Go (parser), Svelte 5, TypeScript

**Spec:**
`docs/superpowers/specs/2026-03-19-model-info-display-design.md`

---

## File Structure

### Files to create

| File | Responsibility |
| ---- | -------------- |
| `frontend/src/lib/utils/model.ts` | `computeMainModel()` utility |
| `frontend/src/lib/utils/model.test.ts` | Tests for `computeMainModel()` |

### Files to modify

| File | Change |
| ---- | ------ |
| `internal/parser/copilot.go` | Add `currentModel` tracking from `session.model_change`, stamp on assistant messages |
| `internal/parser/copilot_test.go` | Tests for model tracking |
| `frontend/src/lib/components/layout/SessionBreadcrumb.svelte` | Model badge in session header |
| `frontend/src/lib/components/content/MessageContent.svelte` | Off-main-model badge on messages |
| `frontend/src/lib/components/content/SubagentInline.svelte` | Model badge in toggle header (after expand) |

### Files NOT to modify (revert branch changes)

These files were modified on the branch but should match origin/main:

- `internal/parser/types.go` — no `MainModel` field or
  `ComputeMainModel()` in Go
- `internal/db/db.go` — no schema version bump or migration
- `internal/db/schema.sql` — no `main_model` column
- `internal/db/sessions.go` — no `MainModel` field on Session struct
- `internal/sync/engine.go` — no `MainModel` mapping
- `frontend/src/lib/api/types/core.ts` — no `main_model` on Session
- `frontend/src/lib/utils/format.ts` — no `shortModelName()`

---

## Task 0: Reset branch to origin/main

Before implementing, reset the branch so we start clean. The current
branch has the old implementation that we're replacing.

- [ ] **Step 1: Reset branch to origin/main**

```bash
git reset --hard origin/main
```

- [ ] **Step 2: Re-add the spec and plan docs**

The reset will remove our spec and plan. Re-create them from the
commits we made (they're in the reflog).

```bash
git cherry-pick <spec-commit-sha> --no-verify
git cherry-pick <plan-commit-sha> --no-verify
```

- [ ] **Step 3: Verify clean state**

```bash
git diff origin/main --stat
```

Should only show the docs/ files.

---

## Task 1: Copilot parser model tracking

**Files:**

- Modify: `internal/parser/copilot.go`
- Modify: `internal/parser/copilot_test.go`

### Step 1: Write failing tests for model tracking

- [ ] **Step 1a: Add test for single model session**

Add to `copilot_test.go`:

```go
func TestParseCopilotSession_ModelChange(t *testing.T) {
	path := writeCopilotJSONL(t,
		`{"type":"session.start","data":{"sessionId":"model-test"},"timestamp":"2025-01-15T10:00:00Z"}`,
		`{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"timestamp":"2025-01-15T10:00:01Z"}`,
		`{"type":"user.message","data":{"content":"Hello"},"timestamp":"2025-01-15T10:00:02Z"}`,
		`{"type":"assistant.message","data":{"content":"Hi there"},"timestamp":"2025-01-15T10:00:03Z"}`,
	)

	_, msgs := parseAndValidateHelper(t, path, "m", 2)

	assertEqual(t, "claude-sonnet-4.6", msgs[1].Model, "msgs[1].Model")
	assertEqual(t, "", msgs[0].Model, "msgs[0].Model")
}
```

- [ ] **Step 1b: Add test for no model data**

```go
func TestParseCopilotSession_NoModel(t *testing.T) {
	path := writeCopilotJSONL(t,
		`{"type":"session.start","data":{"sessionId":"no-model"},"timestamp":"2025-01-15T10:00:00Z"}`,
		`{"type":"user.message","data":{"content":"Hello"},"timestamp":"2025-01-15T10:00:01Z"}`,
		`{"type":"assistant.message","data":{"content":"Hi"},"timestamp":"2025-01-15T10:00:02Z"}`,
	)

	_, msgs := parseAndValidateHelper(t, path, "m", 2)
	assertEqual(t, "", msgs[1].Model, "msgs[1].Model")
}
```

- [ ] **Step 1c: Add test for mid-session model switch**

```go
func TestParseCopilotSession_ModelMidSessionChange(t *testing.T) {
	path := writeCopilotJSONL(t,
		`{"type":"session.start","data":{"sessionId":"switch-test"},"timestamp":"2025-01-15T10:00:00Z"}`,
		`{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"timestamp":"2025-01-15T10:00:01Z"}`,
		`{"type":"user.message","data":{"content":"First"},"timestamp":"2025-01-15T10:00:02Z"}`,
		`{"type":"assistant.message","data":{"content":"Reply one"},"timestamp":"2025-01-15T10:00:03Z"}`,
		`{"type":"session.model_change","data":{"newModel":"claude-haiku-4.5"},"timestamp":"2025-01-15T10:00:04Z"}`,
		`{"type":"user.message","data":{"content":"Second"},"timestamp":"2025-01-15T10:00:05Z"}`,
		`{"type":"assistant.message","data":{"content":"Reply two"},"timestamp":"2025-01-15T10:00:06Z"}`,
	)

	_, msgs := parseAndValidateHelper(t, path, "m", 4)

	assertEqual(t, "claude-sonnet-4.6", msgs[1].Model, "msgs[1].Model")
	assertEqual(t, "claude-haiku-4.5", msgs[3].Model, "msgs[3].Model")
}
```

- [ ] **Step 1d: Run tests to verify they fail**

```bash
CGO_ENABLED=1 go test -tags fts5 ./internal/parser/ -run "TestParseCopilotSession_Model" -v
```

Expected: FAIL (copilot parser doesn't handle `session.model_change`)

### Step 2: Implement model tracking in Copilot parser

- [ ] **Step 2a: Add model_change constant and currentModel field**

In `copilot.go`, add to the event constants:

```go
copilotEventModelChange = "session.model_change"
```

Add to `copilotSessionBuilder` struct:

```go
currentModel string
```

- [ ] **Step 2b: Handle model_change events in processLine**

Add case to the switch in `processLine`:

```go
case copilotEventModelChange:
    if m := data.Get("newModel").Str; m != "" {
        b.currentModel = m
    }
```

- [ ] **Step 2c: Stamp model on assistant messages**

In `handleAssistantMessage`, add `Model: b.currentModel` to the
`ParsedMessage` struct literal (alongside `ToolCalls`).

- [ ] **Step 2d: Run tests to verify they pass**

```bash
CGO_ENABLED=1 go test -tags fts5 ./internal/parser/ -run "TestParseCopilotSession" -v
```

Expected: all pass (including existing tests)

- [ ] **Step 2e: Run full parser test suite**

```bash
CGO_ENABLED=1 go test -tags fts5 ./internal/parser/ -v
```

- [ ] **Step 2f: Run go vet and go fmt**

```bash
go fmt ./internal/parser/... && go vet ./internal/parser/...
```

- [ ] **Step 2g: Commit**

```bash
git add internal/parser/copilot.go internal/parser/copilot_test.go
git commit -m "feat: extract model from Copilot session.model_change events"
```

---

## Task 2: Frontend computeMainModel utility

**Files:**

- Create: `frontend/src/lib/utils/model.ts`
- Create: `frontend/src/lib/utils/model.test.ts`

### Step 1: Write failing tests

- [ ] **Step 1a: Create test file**

Create `frontend/src/lib/utils/model.test.ts`:

```typescript
import { describe, it, expect } from "vitest";
import { computeMainModel } from "./model.js";
import type { Message } from "../api/types.js";

function msg(role: string, model: string): Message {
  return {
    id: 0,
    session_id: "",
    ordinal: 0,
    role,
    content: "",
    timestamp: "",
    has_thinking: false,
    has_tool_use: false,
    content_length: 0,
    model,
    context_tokens: 0,
    output_tokens: 0,
  };
}

describe("computeMainModel", () => {
  it("returns empty string for empty array", () => {
    expect(computeMainModel([])).toBe("");
  });

  it("returns the single model", () => {
    expect(
      computeMainModel([
        msg("assistant", "claude-sonnet-4.6"),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("returns most frequent model", () => {
    expect(
      computeMainModel([
        msg("assistant", "claude-sonnet-4.6"),
        msg("assistant", "claude-sonnet-4.6"),
        msg("assistant", "claude-haiku-4.5"),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("breaks ties alphabetically", () => {
    expect(
      computeMainModel([
        msg("assistant", "b-model"),
        msg("assistant", "a-model"),
      ]),
    ).toBe("a-model");
  });

  it("ignores user messages", () => {
    expect(
      computeMainModel([
        msg("user", "some-model"),
        msg("assistant", "claude-sonnet-4.6"),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("ignores empty model strings", () => {
    expect(
      computeMainModel([
        msg("assistant", ""),
        msg("assistant", "claude-sonnet-4.6"),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("returns empty when no model data", () => {
    expect(
      computeMainModel([
        msg("assistant", ""),
        msg("user", ""),
      ]),
    ).toBe("");
  });
});
```

- [ ] **Step 1b: Run tests to verify they fail**

```bash
cd frontend && npx vitest run src/lib/utils/model.test.ts
```

Expected: FAIL (module not found)

### Step 2: Implement computeMainModel

- [ ] **Step 2a: Create model.ts**

Create `frontend/src/lib/utils/model.ts`:

```typescript
import type { Message } from "../api/types.js";

/**
 * Compute the most frequently used model across assistant messages.
 * Returns empty string if no model data is present.
 * Tie-break: alphabetically first model wins.
 */
export function computeMainModel(messages: Message[]): string {
  const counts = new Map<string, number>();
  for (const m of messages) {
    if (m.role === "assistant" && m.model) {
      counts.set(m.model, (counts.get(m.model) ?? 0) + 1);
    }
  }
  let best = "";
  let bestN = 0;
  for (const [model, n] of counts) {
    if (n > bestN || (n === bestN && model < best)) {
      best = model;
      bestN = n;
    }
  }
  return best;
}
```

- [ ] **Step 2b: Run tests to verify they pass**

```bash
cd frontend && npx vitest run src/lib/utils/model.test.ts
```

Expected: all pass

- [ ] **Step 2c: Commit**

```bash
git add frontend/src/lib/utils/model.ts frontend/src/lib/utils/model.test.ts
git commit -m "feat: add computeMainModel frontend utility"
```

---

## Task 3: Session header model badge

**Files:**

- Modify: `frontend/src/lib/components/layout/SessionBreadcrumb.svelte`

### Step 1: Add model badge to session breadcrumb

- [ ] **Step 1a: Import computeMainModel and messages store**

Add to the `<script>` section of `SessionBreadcrumb.svelte`:

```typescript
import { computeMainModel } from "../../utils/model.js";
import { messages as messagesStore } from "../../stores/messages.svelte.js";
```

- [ ] **Step 1b: Add derived main model**

Add a derived value:

```typescript
let mainModel = $derived(
  computeMainModel(messagesStore.messages),
);
```

- [ ] **Step 1c: Add model badge to template**

After the token badge block (`{#if sessionContextTokens + ...}`), add:

```svelte
{#if mainModel}
  <span class="model-badge" title={mainModel}>{mainModel}</span>
{/if}
```

- [ ] **Step 1d: Add CSS for model-badge**

Add to the `<style>` section (reuse token-badge styling pattern):

```css
.model-badge {
  font-size: 10px;
  color: var(--text-muted);
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--bg-tertiary);
  white-space: nowrap;
  flex-shrink: 0;
}
```

- [ ] **Step 1e: Verify frontend builds**

```bash
cd frontend && npm run build
```

- [ ] **Step 1f: Commit**

```bash
git add frontend/src/lib/components/layout/SessionBreadcrumb.svelte
git commit -m "feat: show model badge in session header"
```

---

## Task 4: Per-message off-main-model badge

**Files:**

- Modify: `frontend/src/lib/components/content/MessageContent.svelte`

### Step 1: Add off-main-model badge

- [ ] **Step 1a: Import computeMainModel and messages store**

Add to the `<script>` section:

```typescript
import { computeMainModel } from "../../utils/model.js";
import { messages as messagesStore } from "../../stores/messages.svelte.js";
```

- [ ] **Step 1b: Add derived main model and off-main-model logic**

```typescript
let mainModel = $derived(
  computeMainModel(messagesStore.messages),
);

let offMainModel = $derived.by((): string => {
  if (isUser || !message.model) return "";
  return message.model !== mainModel ? message.model : "";
});
```

- [ ] **Step 1c: Add badge to template**

After the timestamp span in `.message-header`, add:

```svelte
{#if offMainModel}
  <span class="message-model" title={offMainModel}>
    {offMainModel}
  </span>
{/if}
```

- [ ] **Step 1d: Add CSS for message-model**

```css
.message-model {
  font-size: 10px;
  color: var(--text-muted);
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--bg-tertiary);
  white-space: nowrap;
  flex-shrink: 0;
  opacity: 0.8;
}
```

- [ ] **Step 1e: Verify frontend builds**

```bash
cd frontend && npm run build
```

- [ ] **Step 1f: Commit**

```bash
git add frontend/src/lib/components/content/MessageContent.svelte
git commit -m "feat: show model badge on off-main-model messages"
```

---

## Task 5: Subagent toggle model badge

**Files:**

- Modify: `frontend/src/lib/components/content/SubagentInline.svelte`

### Step 1: Add model badge to subagent toggle (computed on expand)

- [ ] **Step 1a: Import computeMainModel**

Add to the `<script>` section:

```typescript
import { computeMainModel } from "../../utils/model.js";
```

- [ ] **Step 1b: Add derived subagent model**

Derive from the lazily-loaded messages array:

```typescript
let subagentModel = $derived(
  messages ? computeMainModel(messages) : "",
);
```

- [ ] **Step 1c: Add model badge to toggle header**

After the token count span inside the `{#if subagentSession}` block,
add:

```svelte
{#if subagentModel}
  <span class="toggle-model" title={subagentModel}>
    {subagentModel}
  </span>
{/if}
```

- [ ] **Step 1d: Add CSS for toggle-model**

```css
.toggle-model {
  font-size: 10px;
  color: var(--text-muted);
  white-space: nowrap;
  flex-shrink: 0;
}
```

- [ ] **Step 1e: Verify frontend builds**

```bash
cd frontend && npm run build
```

- [ ] **Step 1f: Commit**

```bash
git add frontend/src/lib/components/content/SubagentInline.svelte
git commit -m "feat: show model in subagent toggle header on expand"
```

---

## Task 6: Final verification

- [ ] **Step 1: Run Go tests**

```bash
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
```

- [ ] **Step 2: Run frontend tests**

```bash
cd frontend && npx vitest run
```

- [ ] **Step 3: Build frontend**

```bash
cd frontend && npm run build
```

- [ ] **Step 4: Run go vet**

```bash
go vet ./...
```

- [ ] **Step 5: Verify git log**

```bash
git log --oneline origin/main..HEAD
```

Should show the spec/plan docs + 5 implementation commits.
