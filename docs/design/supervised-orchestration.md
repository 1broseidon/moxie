# Supervised Multi-Agent Orchestration for Non-Blocking Chat

Status: shipped (fanout MVP)

This note records a concrete design for adding research-backed orchestration patterns to Moxie without turning the main chat into a noisy agent swarm transcript.

## Goals

- keep Telegram, Slack, and Webex conversations responsive while background work runs
- let Moxie launch multiple subagents in common, bounded patterns
- preserve portable OneAgent thread behavior for every worker and merger step
- expose progress, retries, failures, and final outputs without streaming every internal turn into chat
- reuse the existing `moxie subagent` execution substrate instead of inventing a second delegation path

## Non-goals for the first rollout

- no open-ended worker-to-worker group chat
- no recursive workflow spawning from child workflows
- no unbounded planner loops
- no attempt to make multi-agent orchestration the default for simple tasks
- no raw worker transcript spam in the user chat

## Why this shape

Recent evidence points in a consistent direction:

- multi-agent workflows help most on complex or decomposable tasks, not simple ones
- bounded role structure outperforms free-form swarms on coding and composite work
- decomposition makes oversight materially easier for humans
- long discussions increase risks like drift, collapse, and monopolization
- users want controls over when and how agents speak rather than constant unsolicited output

That suggests Moxie should ship a supervisor control plane, not a shared group chat of agents.

## Existing substrate Moxie already has

The current codebase already provides most of the hard primitives:

- async subagent dispatch via `moxie subagent`
- per-job supervision with retries, progress heartbeats, and stall detection
- portable thread state through OneAgent thread files
- result artifacts for completed subagent runs
- per-conversation chat state and delivery callbacks across transports

The orchestration layer should build on top of these primitives.

## Control-plane model

Introduce a first-class workflow record as the durable source of truth for orchestration.

Suggested storage:

- `~/.config/moxie/workflows/<id>.json`
- `~/.config/moxie/workflows/<id>.events.jsonl`

Jobs remain the execution units. A workflow owns many child jobs.

### Proposed canonical workflow model

```go
type Workflow struct {
    ID                string    `json:"id"`
    ConversationID    string    `json:"conversation_id"`
    ReplyConversation string    `json:"reply_conversation,omitempty"`
    ParentJobID       string    `json:"parent_job_id,omitempty"`
    ParentThreadID    string    `json:"parent_thread_id,omitempty"`
    Pattern           string    `json:"pattern"` // fanout, review, judge
    Prompt            string    `json:"prompt"`
    CWD               string    `json:"cwd,omitempty"`
    State             State     `json:"state"`   // backend/model/thinking snapshot for final synthesis
    Notify            string    `json:"notify,omitempty"` // silent, milestones, verbose
    DeliverMode       string    `json:"deliver_mode,omitempty"` // synthesize, direct
    Status            string    `json:"status"` // planned, running, blocked, merging, completed, failed, canceled
    Created           time.Time `json:"created"`
    Updated           time.Time `json:"updated"`
    Steps             []WorkflowStep `json:"steps,omitempty"`
    FinalStepID       string    `json:"final_step_id,omitempty"`
    FinalThreadID     string    `json:"final_thread_id,omitempty"`
    FinalArtifactID   string    `json:"final_artifact_id,omitempty"`
    LastError         string    `json:"last_error,omitempty"`
}

type WorkflowStep struct {
    ID            string    `json:"id"`
    Role          string    `json:"role"` // worker, reviewer, judge, merger
    Backend       string    `json:"backend"`
    Model         string    `json:"model,omitempty"`
    Prompt        string    `json:"prompt"`
    DependsOn     []string  `json:"depends_on,omitempty"`
    JobID         string    `json:"job_id,omitempty"`
    ThreadID      string    `json:"thread_id,omitempty"`
    ArtifactID    string    `json:"artifact_id,omitempty"`
    Status        string    `json:"status"` // pending, running, completed, failed, canceled, skipped
    RetryCount    int       `json:"retry_count,omitempty"`
    StartedAt     time.Time `json:"started_at,omitempty"`
    FinishedAt    time.Time `json:"finished_at,omitempty"`
    LastProgressAt time.Time `json:"last_progress_at,omitempty"`
    LastError     string    `json:"last_error,omitempty"`
}
```

### Why a separate workflow record

Keeping workflows separate from `PendingJob` preserves a clean split:

- `PendingJob` = one executable unit dispatched to one backend
- `Workflow` = orchestration graph, progress state, and chat notification policy

This avoids overloading the existing job file with multi-child lifecycle concerns.

## Event model

Write append-only workflow events to `events.jsonl`.

Suggested event types:

- `workflow.created`
- `step.queued`
- `step.started`
- `step.progress`
- `step.retrying`
- `step.completed`
- `step.failed`
- `workflow.blocked`
- `workflow.completed`
- `workflow.failed`
- `workflow.canceled`

This event stream powers:

- CLI tail/watch
- transport milestone notifications
- recovery after restart
- future analytics without parsing free-form logs

## Chat delivery model

The main chat should remain non-blocking by default.

### Default user-facing behavior

Workflows are **quiet by default**. When a workflow starts, Moxie sends one short acknowledgement and then goes silent until the final result is ready:

```text
Launched wf-123 (fanout, 3 workers). I’ll report back when there’s progress.
```

The next user-visible message is the merged result delivered to the parent thread. Moxie does **not** stream per-worker progress or intermediate milestones into chat unless something goes wrong (stall, retry, failure) or the user explicitly watches with `moxie workflow watch`.

This quiet-by-default contract is intentional: it keeps the chat non-blocking and avoids spending parent-model tokens on routine bookkeeping.

### Important design rule

Milestone notifications (stall, retry, blocked) are transport-level status messages, not full synthesis turns on the parent thread. Only the final merged result triggers a synthesis turn.

### Final delivery

Two modes are useful:

- `synthesize` (default for chat-initiated work): final merger/judge output is handed back to the parent thread for a polished answer in-context
- `direct`: final merger/judge output is delivered directly to the user without an extra synthesis step

## CLI UX

Add a new top-level workflow namespace while continuing to use `moxie subagent` for actual child execution.

```bash
moxie workflow run <pattern> [flags]
moxie workflow list [--all]
moxie workflow show <id>
moxie workflow watch <id>
moxie workflow cancel <id>
```

### `moxie workflow run fanout`

```bash
moxie workflow run fanout \
  --workers codex,codex,claude \
  --merge claude \
  --text "Research agent supervision patterns for coding workflows"
```

Semantics:

- dispatch N independent workers on the same task or rubric slices
- gather their outputs
- run one merger step that synthesizes findings

### `moxie workflow run review`

```bash
moxie workflow run review \
  --worker codex \
  --reviewer claude \
  --text "Patch the scheduler retry race and review the change"
```

Semantics:

- one executor step
- one reviewer step against a fixed rubric
- optional one-pass repair if the reviewer requests changes
- merger/finalizer produces the user-facing answer

### `moxie workflow run judge`

```bash
moxie workflow run judge \
  --workers claude,codex,pi \
  --judge claude \
  --text "Propose the safest rollout plan for orchestration templates"
```

Semantics:

- multiple candidates generated independently
- one judge selects, ranks, or synthesizes
- final answer returns the chosen plan plus rationale

### `moxie workflow list`

Show active workflows by default:

```text
wf-123  fanout   running    2m   2/3 steps done   Research agent supervision...
wf-124  review   blocked    30s  reviewer waiting Patch the scheduler retry race...
```

### `moxie workflow show`

Show:

- workflow status
- pattern
- parent conversation/thread
- notify mode
- final thread id if available
- child step table: role, backend, status, age, artifact/thread ids
- last error

### `moxie workflow watch`

Tail the event log in real time:

```text
12:04:11 workflow.created  pattern=fanout workers=3
12:04:12 step.started      step=w1 backend=codex
12:04:12 step.started      step=w2 backend=codex
12:04:13 step.started      step=w3 backend=claude
12:04:48 step.completed    step=w2 thread=repo-audit-2
12:05:10 step.retrying     step=w1 reason="no progress for 45s"
12:05:44 workflow.completed final_thread=wf-123-merge
```

## Chat UX

Phase 1 should keep the slash surface small.

Recommended additions:

- `/jobs` — list active workflows and subagents
- `/cancel <id>` — cancel a workflow or job

Everything else can initially be natural language:

- “use a fanout workflow on this”
- “run a reviewer after codex finishes”
- “cancel wf-123”

Phase 2 can add explicit approval commands if needed.

## Execution rules

To avoid the failure modes seen in open-ended multi-agent systems:

- workers do not talk directly to each other
- all coordination goes through the workflow supervisor
- mergers/judges see structured summaries of child outputs, not full raw chatter by default
- every pattern has a bounded step count
- every repair loop has a hard retry cap
- workflow nesting is disabled in the first rollout

## Patterns to implement first

### 1) Fanout

Best use cases:

- research sweeps
- repo audits by independent workers
- alternative solution proposals
- parallel evidence gathering

Why first:

- strongest payoff-to-complexity ratio
- maps well onto current async subagent infrastructure
- easy to supervise with milestone updates

Recommended defaults:

- max workers per workflow: 4
- one merger step required
- notify on 50% complete and 100% complete

### 2) Review

Best use cases:

- code changes
- config edits
- migration plans
- any task where “do the work, then critique it” is safer than one-shot execution

Why first:

- directly supports oversight and supervision
- simple fixed DAG
- high practical value for coding tasks

Recommended defaults:

- one executor
- one reviewer
- optional one repair pass
- reviewer emits structured verdict: pass, fix, block

### 3) Judge

Best use cases:

- design choices
- architecture proposals
- ranking candidate solutions
- choosing between backend-specific outputs

Why first:

- captures the “multiple views + arbiter” pattern without open-ended debate
- aligns with evidence that aggregation/judging helps on hard tasks
- easier to bound than free-form discussion

Recommended defaults:

- 2-3 independent candidates
- one judge
- judge must produce explicit winner or synthesis

## Deferred pattern: planner/decompose

Research strongly supports decomposition, but general decomposition is harder to ship safely.

Defer a true planner template until the fixed-DAG patterns above are working. When added, it should be tightly bounded:

- planner produces at most 5 subtasks
- subtasks become child workflow steps
- one merger/reviewer closes the loop
- no recursive planning in phase 1

## Guardrails and limits

Reuse existing limits and add workflow-specific ceilings.

Suggested additions to config:

```json
{
  "max_workflows_per_conversation": 3,
  "max_workers_per_workflow": 4,
  "max_workflow_steps": 8,
  "workflow_notify": "milestones"
}
```

Existing limits still apply underneath:

- `subagent_max_depth`
- `subagent_max_attempts`
- `subagent_stall_timeout`
- `subagent_progress_timeout`
- `max_pending_subagents`
- `max_jobs_per_minute`

## Recovery model

On service restart:

- load workflow files
- reconcile each step against its child job file
- continue any runnable next step
- re-emit only the next meaningful milestone, not the whole history

This should mirror the current subagent recovery philosophy.

## Implementation status

### Shipped (MVP)

- workflow store and event log
- `moxie workflow run fanout` — bounded parallel workers + merge step
- `moxie workflow list/show/watch/cancel`
- quiet-by-default delivery: one launch ack, then silence until the merged result
- milestone notifications on stall, retry, and failure

### Deferred (Phase 2)

- review pattern
- judge pattern
- planner/decompose pattern
- approval gates
- workflow artifacts in `moxie result`
- chat commands beyond `/jobs` and `/cancel`

## Bottom line

The shipped fanout MVP delivers a bounded workflow layer over supervised subagent jobs.

Moxie now provides:

- real multi-agent leverage on hard tasks via `moxie workflow run fanout`
- a quiet, non-blocking main chat by default — one ack, then the merged result
- inspectable progress via `moxie workflow watch` and `show`
- portable worker threads through OneAgent state
- a clean path to review, judge, and planner patterns in later phases
