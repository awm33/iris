// Package queue implements the Postgres-native job queue:
// FOR UPDATE SKIP LOCKED claims + LISTEN/NOTIFY wakeups (TDD §3.1).
//
// Jobs are rows in generation_jobs (and later media/render job tables sharing
// the same claim semantics). Workers block on LISTEN with a poll fallback, so
// a missed NOTIFY only delays work by the poll interval, never loses it.
package queue

// Claim SQL (the core of the design — kept here as the single source of truth
// until sqlc generates typed wrappers in M2):
//
//   WITH next AS (
//     SELECT id FROM generation_jobs
//     WHERE state = 'queued'
//       AND not_before <= now()
//       AND (depends_on_job_id IS NULL OR depends_on_job_id IN
//            (SELECT id FROM generation_jobs WHERE state = 'complete'))
//     ORDER BY created_at
//     FOR UPDATE SKIP LOCKED
//     LIMIT $1
//   )
//   UPDATE generation_jobs j
//   SET state = 'dispatched', claimed_by = $2, claimed_at = now(),
//       attempts = attempts + 1, updated_at = now()
//   FROM next WHERE j.id = next.id
//   RETURNING j.*;
//
// Completion runs in the SAME transaction as the domain writes it produces
// (take rows, asset versions, lineage edges), followed by:
//
//   SELECT pg_notify('jobs', json_build_object('id', $1, 'state', $2)::text);
//
// so dependency release and WS fan-out are atomic with the results they announce.
//
// Retry/backoff: on transient failure, state -> 'queued', not_before -> now() + backoff(attempts);
// attempts > max  -> state 'failed'.
//
// Implementation lands in M2 (see docs/design/04-implementation-plan.md).
