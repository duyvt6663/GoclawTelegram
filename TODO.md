# TODO

## Feature Build Dashboard And Live Codex Progress

Status: planned, not implemented.

### Current constraints

- `internal/beta/feature_requests/tool_build.go` runs Codex with `cmd.Run()` and buffers all `stdout` and `stderr` in memory, so there is no incremental progress signal while a build is running.
- `internal/beta/feature_requests/tool_detail.go` only exposes a monolithic `build_log`.
- The current completion hook only fires after the build exits; it does not provide mid-run visibility.
- The repo already has a usable realtime transport path through:
  - `pkg/protocol/events.go`
  - `cmd/gateway.go`
  - `ui/web/src/components/providers/ws-provider.tsx`
  - `ui/web/src/pages/events/events-page.tsx`
  - `ui/web/src/pages/teams/board/board-container.tsx`

### Goal

Build a dashboard that shows:

- all requested features
- which features are currently building
- live Codex output / recent progress
- build phases, retries, checkpoints, and verification state
- final success / failure / stalled status

### Backend plan

1. Split feature summary state from build-run state.
   - Keep `beta_feature_requests` as the summary row.
   - Add `beta_feature_build_runs` for one row per build attempt.
   - Add `beta_feature_build_events` as an append-only event stream.

2. Replace the buffered Codex runner in `internal/beta/feature_requests/tool_build.go`.
   - Use `StdoutPipe` and `StderrPipe`.
   - Persist output incrementally while the process is running.
   - Update run phase and `last_output_at` as chunks arrive.
   - Keep a compact final `build_log` tail for `feature_detail`.

3. Improve process supervision.
   - Ensure timeout / cancel kills the whole Codex process tree, not just the wrapper process.
   - Add stale-run detection when a feature is still marked `building` but no process or output is alive.

4. Emit dedicated feature build WS events instead of overloading generic session events.
   - `feature.build.started`
   - `feature.build.output`
   - `feature.build.phase`
   - `feature.build.checkpoint`
   - `feature.build.repair.started`
   - `feature.build.verification.started`
   - `feature.build.verification.finished`
   - `feature.build.finished`
   - `feature.build.stalled`

5. Add snapshot APIs for initial page load and refresh.
   - `GET /v1/beta/features`
   - `GET /v1/beta/features/:id`
   - `GET /v1/beta/features/:id/runs`
   - `GET /v1/beta/build-runs/:runId/events?after=<seq>`
   - Beta features can register isolated HTTP routes through `internal/beta/feature.go`.

### Frontend plan

1. Add a dedicated web page under Monitoring for feature builds.
   - Add route and sidebar entry.
   - Use HTTP for initial data and WS for live updates.

2. Page layout should include:
   - feature list with filters by status
   - active build list / attempts
   - selected run timeline
   - live terminal tail
   - verification state, retry count, and last output time

3. Reuse existing WS infrastructure.
   - Extend `ui/web/src/api/protocol.ts`
   - Subscribe with `ui/web/src/hooks/use-ws-event.ts`
   - Invalidate / refresh queries similarly to `ui/web/src/hooks/use-query-invalidation.ts`

### Checkpoints / plan mode

After raw streaming works, add structured progress markers:

- derive coarse phases from the runner itself:
  - `queued`
  - `planning`
  - `implementing`
  - `verifying`
  - `repairing`
  - `completed`
  - `failed`
- optionally ask Codex to emit sentinel markers such as:
  - `BUILD_PLAN:`
  - `BUILD_CHECKPOINT:`

Important:

- the dashboard must not depend on model compliance for basic progress
- structured markers are an enhancement, not the transport backbone

### Suggested implementation order

1. Add build run / event tables and store methods.
2. Convert the Codex runner to streamed output.
3. Emit `feature.build.*` WS events.
4. Add snapshot HTTP endpoints.
5. Build the minimal dashboard page.
6. Add structured checkpoints and richer UX.
7. Add optional actions such as cancel / retry / download full log.

### Notes

- Existing `agent` WS events are session-centric and are not a clean fit for a cross-feature dashboard.
- A dedicated `feature.build.*` event family is the cleaner model.
- This work should also fix the current blind spot where a build can appear stuck in `building` with no observable inner-loop progress.
