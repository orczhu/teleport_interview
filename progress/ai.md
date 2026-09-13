The four important corrections
1. Eliminate the post-start cgroup race
The current proposal is:
cmd.Start() → write PID to cgroup.procs
Calling the race “accepted” is risky for L5. Between those operations, the process can consume resources, fork descendants outside the job cgroup, or exit. That weakens both resource enforcement and child cleanup.
Go’s Linux syscall.SysProcAttr already exposes:
UseCgroupFD bool
CgroupFD    int
This makes Go use clone3(CLONE_INTO_CGROUP), placing the child in the cgroup atomically at creation. Your kernel baseline of 5.14 is sufficient. I would make this the only supported path and fail clearly if clone3 or cgroup placement is unavailable—no racy fallback. Go implementation, Linux cgroup documentation
Revised lifecycle:
create cgroup
→ write limits
→ open cgroup directory FD
→ cmd.Start with UseCgroupFD/CgroupFD
→ register running job
→ wait
This is both simpler and stronger than documenting the race as a TODO.
2. The current state machine reports stopped too early
This invariant is problematic:
Stop reserves stopped under lock before killing.

At that moment the process might still be alive. Worse, cgroup.kill might fail, leaving a live job represented as stopped.
Use a nonterminal stopping state or an internal stopRequested flag:
running
 ├── leader exits naturally ──→ exited
 ├── Stop requested ──────────→ stopping
 │                              └── cgroup empty → stopped
 └── worker failure ──────────→ failed
Only the waiter/reaper should publish a terminal state. Stop should:
Atomically mark stopRequested.
Write 1 to cgroup.kill.
Wait for terminal completion, subject to the RPC context.
Return success only once the job is no longer running.
Also define idempotency:
Stop on stopping: wait for the existing stop.
Stop on stopped: success.
Stop on exited: probably FailedPrecondition.
Unknown/inaccessible job: NotFound or PermissionDenied, consistently.
failed_to_start is currently contradictory with synchronous Start. If Start returns an RPC error and no job ID, it should not create a client-visible job. Remove that state unless you intend to return an ID for failed starts.
3. sync.Cond plus an unlocked cancellation broadcast has a lost-wakeup race
Appending data under the mutex and broadcasting afterward can be correct because the data length is the protected predicate.
Cancellation is different. If the cancellation goroutine calls Broadcast without acquiring the condition mutex, it can broadcast after the reader checks ctx.Err() but just before the reader enters Wait(). The reader may then sleep forever.
If you retain sync.Cond, cancellation must acquire the same lock before broadcasting. You also need a way to terminate the per-subscriber watchdog when the stream returns.
A cleaner implementation is a replaceable notification channel:
type output struct {
    mu     sync.Mutex
    data   []byte
    notify chan struct{}
    closed bool
}
On append, under the lock:
data = append(data, p...)
close(notify)
notify = make(chan struct{})
A reader snapshots notify, unlocks, then selects on:
select {
case <-notify:
case <-ctx.Done():
}
That gives cancellation without a watchdog goroutine and avoids the Cond edge case. If you keep Cond, document the predicate loop precisely. Go explicitly requires observing the condition under the associated lock. Go sync.Cond documentation
4. Unbounded output is more dangerous than the draft suggests
An arbitrary job can print indefinitely. Because the output buffer belongs to the worker process, not the job cgroup, memory.max does not protect the service. One authorized user can OOM the entire worker and affect every job.
A disk-backed append-only spool is a better small-scope design:
One file per job.
One in-memory length/closed state and notification primitive.
Writer appends bytes sequentially.
Each subscriber has an independent offset and uses ReadAt.
New-data notification remains event-driven.
Late subscribers replay from offset zero.
Server memory remains bounded.
Delete the file when the tombstone is garbage-collected.
You do not need inotify: the writer is inside the same process and can signal readers directly.