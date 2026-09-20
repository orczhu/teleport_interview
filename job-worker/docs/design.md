# Job Worker Service — Design

## Summary

This project is a single-node job worker for 64-bit Linux. It runs an arbitrary executable with literal arguments, provides a mutually authenticated gRPC API to start and stop jobs, query their status, and stream their combined standard output and standard error, and applies per-job CPU, memory, process-count, and disk-I/O limits with cgroup v2.

The implementation targets Teleport challenge level 5. It favors a small, auditable design over scalability or high availability. The central correctness properties are that a job executes inside its cgroup from its first instruction, every descendant remains in that cgroup, only one goroutine owns terminal lifecycle transitions, output is replayable from byte zero without polling, and every RPC is authenticated and authorized at the server.

## Requirements and scope

### In scope

- Go implementation running on Linux amd64 and arm64.
- A reusable worker library with start, stop, status, and output-streaming operations.
- A gRPC server and CLI client.
- TLS 1.3 mutual authentication.
- Certificate-identity-based authorization with job ownership.
- Efficient, binary-safe output replay to concurrent subscribers.
- cgroup v2 limits for CPU, memory, PIDs, and read/write I/O bandwidth.
- Whole-job termination, including descendants.
- Focused unit, integration, race, and end-to-end tests.

### Explicit non-goals

- Persistence or recovery after a worker restart.
- High availability, scheduling across nodes, or leader election.
- Linux namespaces, containers, or filesystem/network isolation.
- Interactive stdin or terminal emulation.
- Runtime configuration files, dynamic user management, certificate rotation, metrics, or tracing.
- Separate stdout and stderr streams.
- Automatic discovery of every block device used by a job.

The registry and lifecycle state are in memory. Output is stored in temporary files but is not re-adopted after restart. If the worker crashes, running processes are reparented, its registry and subscribers are lost, and job cgroups and output files may remain. A production service would persist job metadata, reconcile cgroups at startup, use pidfds and subreaper semantics where appropriate, and garbage-collect terminal jobs and output.

## Deployment and runtime model

The worker requires:

- Go 1.22 or newer.
- Linux kernel 5.14 or newer.
- A unified cgroup v2 hierarchy.
- The `cpu`, `memory`, `pids`, and `io` controllers delegated to the worker.
- Permission to create children, enable delegated controllers, migrate itself within the delegated subtree, and use `CLONE_INTO_CGROUP`.
- One configured block device for I/O throttling.

The service receives a delegated cgroup directory through `--cgroup-root`. During startup it:

1. Verifies the mount is cgroup v2 using `statfs` and the cgroup2 magic value.
2. Verifies all required controllers are available.
3. Creates a `control` leaf and moves the worker process into it.
4. Enables `+cpu +memory +pids +io` in the delegated root's `cgroup.subtree_control`.
5. Creates one sibling leaf per job.

Moving the worker into `control` is necessary because a non-root cgroup cannot both contain processes and distribute domain resources to child cgroups. A systemd deployment would provide this delegation with `Delegate=yes`; exact unit installation is documentation rather than application code.

The prototype may run with elevated privileges for cgroup setup, but submitted jobs run with the worker's OS identity. An authorized user can therefore execute anything that identity can execute. In production, the worker would run as a dedicated unprivileged service account with only a delegated cgroup subtree. Namespaces, per-job credentials, seccomp, and filesystem/network isolation are separate concerns and are not claimed here.

On SIGINT or SIGTERM, the server stops accepting RPCs, requests termination of every running job, waits for cleanup up to a fixed grace period, and then exits. A timed-out shutdown is reported and exits nonzero.

## Architecture

```text
jobctl
  |
  | gRPC over TLS 1.3 mTLS
  v
gRPC server
  |-- authentication and role interceptors
  |-- ownership checks and error mapping
  v
worker.Service
  |-- registry: UUID -> Job
  |-- Job lifecycle owner
  |-- output spool
  `-- cgroup.Manager -> per-job cgroup.Group
```

Proposed package layout:

```text
job-worker/
|-- cmd/job-worker/
|-- cmd/jobctl/
|-- internal/worker/
|-- internal/cgroup/
|-- internal/server/
|-- internal/client/
|-- proto/worker.proto
|-- proto generated Go files
|-- certs/
|-- docs/design.md
|-- README.md
|-- go.mod
`-- go.sum
```

The import direction is `cmd/job-worker -> server -> worker -> cgroup` and `cmd/jobctl -> client`. The worker library does not import the gRPC server. Small interfaces at the cgroup and process-launch boundaries permit unit tests without a privileged host.

The only non-standard runtime dependencies are gRPC/protobuf and Cobra. No shell, external binary, container runtime, or third-party cgroup/output/authorization implementation is used by the server.

## Job identity and lifecycle

Each job receives a server-generated UUID. PIDs are never used as public identities and are never returned by the API.

Client-visible states are:

- `RUNNING`: the leader started and no stop has been requested.
- `STOPPING`: termination was requested but lifecycle cleanup is incomplete.
- `EXITED`: the leader exited without a stop request and the whole job is gone.
- `STOPPED`: termination was requested and the whole job is gone.
- `FAILED`: the worker terminated or lost control of the job because an internal policy or lifecycle operation failed.

`Start` is synchronous with process creation: success means `fork/exec` completed successfully. A start failure returns a gRPC error and no job ID, so `FAILED_TO_START` is not a persisted job state.

### Start sequence

1. Validate the executable, literal argv, and resource limits.
2. Generate the UUID and create the output spool.
3. Create the job cgroup and write all limits before starting user code.
4. Open the job cgroup directory and retain its file descriptor.
5. Create one OS pipe and connect its write end to both child stdout and stderr.
6. Configure `syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}`.
7. Call `cmd.Start`. Go uses `clone3(CLONE_INTO_CGROUP)`, so the leader is born in the job cgroup before executing user code.
8. Close the parent's pipe write end, add the job to the registry, start its output reader, and start its lifecycle goroutine.
9. Return the UUID.

There is deliberately no fallback that starts the command and later writes its PID to `cgroup.procs`. That sequence permits execution and forks before resource control. If atomic placement is unsupported or denied, Start fails and removes the cgroup and spool.

### Terminal lifecycle

Exactly one lifecycle goroutine calls `cmd.Wait`. After the leader exits, it kills any remaining descendants, waits for the output pipe to reach EOF, waits until the cgroup is unpopulated, removes the cgroup, publishes the terminal state, and closes `done` exactly once. Killing descendants before waiting for output EOF is important because a descendant may have inherited the pipe's write descriptor.

If the leader exits naturally, any surviving descendants are killed. This defines a job as the leader invocation and its descendants, not as an indefinitely surviving orphan tree. `EXITED` is published only after those descendants are gone and output has reached EOF.

`Stop` changes `RUNNING` to `STOPPING`, writes `1` to `cgroup.kill`, and waits on either `job.done` or the RPC context. It never publishes `STOPPED` itself. This prevents status from claiming that a job is stopped while processes remain alive. A canceled Stop RPC does not cancel server-side termination.

Stop is idempotent for `STOPPING` and `STOPPED`. Stopping an `EXITED` job returns `FailedPrecondition`; an unknown job returns `NotFound`. If `cgroup.kill` fails, the RPC returns an error and the job remains `STOPPING` because termination may have partially progressed; its lifecycle owner continues observing it.

The cgroup manager observes `cgroup.events` rather than enumerating descendants. `populated=0` is the whole-tree completion condition. It uses kernel event notification rather than a busy loop.

Terminal jobs remain as in-memory tombstones for the lifetime of this prototype so status and output remain available. Production would apply a retention policy and delete the spool when its tombstone expires.

## Output storage and streaming

Stdout and stderr are intentionally combined. The worker preserves the bytes delivered by the shared pipe, but does not label their source or promise ordering beyond the order observed from that pipe. No UTF-8, line, ANSI, or record assumptions are made.

Each job has an append-only spool file with mode `0600`, plus this small in-memory state:

```go
type Output struct {
    mu     sync.Mutex
    size   int64
    notify chan struct{}
    closed bool
    err    error
}
```

One goroutine per job reads the merged pipe and appends to the spool. After an append it locks `mu`, advances `size`, closes the current `notify` channel, replaces it with a new channel, and unlocks. On EOF it records `closed` and similarly notifies readers.

Each subscriber owns an independent byte offset. It snapshots the current size and notification channel under the mutex, reads the available range with `ReadAt` outside the mutex, sends bounded gRPC chunks, and then waits with:

```go
select {
case <-notify:
case <-ctx.Done():
}
```

This provides replay from offset zero, concurrent readers at different speeds, prompt cancellation, and event-driven discovery without a goroutine-per-subscriber watchdog or a `sync.Cond` lost-wakeup risk. A slow subscriber does not block the writer or other subscribers; gRPC backpressure blocks only that subscriber's handler.

The prototype enforces a server-configured maximum output size per job. Reaching it requests job termination and records an explicit output-limit failure. This bounds damage from a job that prints indefinitely while preserving all bytes produced up to the enforced limit. Production could instead allocate output quotas in durable object storage.

## Cgroup management

`cgroup.Manager` owns the delegated root and configured I/O device. `Manager.NewGroup(uuid, limits)` creates a leaf and returns a `Group` used by one job.

Limits map to cgroup v2 files:

- CPU: `cpu.max` as integer quota microseconds and a fixed 100 ms period.
- Memory: `memory.max` in bytes.
- Process count: `pids.max`.
- Disk bandwidth: `io.max` with `MAJOR:MINOR rbps=N wbps=N`.

The server resolves `--io-device` to a block-device major/minor number once at startup. An I/O limit is rejected if no valid device is configured. Supporting several devices or discovering overlay/device-mapper backing devices is future work; I/O control is not silently skipped.

Empty client limits select server defaults: 1 CPU, 512 MiB memory, 1,000 PIDs, and configured default read/write bandwidth. The protobuf uses optional fields so omission is distinguishable from invalid zero or negative values. CPU is represented as integer millicores at the API boundary and converted to `cpu.max`; floating-point values, NaN, and rounding ambiguity are avoided.

The server validates limits and rejects values outside its policy. It does not silently clamp them, and the CLI does not compare requested CPU with its own machine because the server may have different capacity.

`Group.Kill` writes `1` to `cgroup.kill`, which targets the entire cgroup subtree. `Group.WaitEmpty` observes `cgroup.events`. `Group.Close` removes only a verified-empty directory. Cleanup has a single owner: the job lifecycle goroutine.

## API and wire format

```protobuf
service JobWorker {
  rpc Start(StartRequest) returns (StartResponse);
  rpc Stop(StopRequest) returns (StopResponse);
  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);
  rpc StreamOutput(StreamOutputRequest) returns (stream OutputChunk);
}
```

`StartRequest` contains `repeated string argv` and optional `ResourceLimits`. The server requires at least one argv element and passes it directly to `exec.Command` without shell interpolation.

`GetStatusResponse` contains UUID, state, owner, timestamps, exit code or terminating signal when available, and a terminal failure reason. `OutputChunk` contains binary `bytes data` and its starting `uint64 offset`. Chunk boundaries have no semantic meaning. Normal stream EOF means all job output is complete; cancellation affects only that subscriber.

Errors use standard gRPC status codes:

- `InvalidArgument`: invalid argv, UUID, or resource limits.
- `Unauthenticated`: no verified client identity.
- `PermissionDenied`: authenticated identity lacks permission.
- `NotFound`: the job does not exist.
- `FailedPrecondition`: operation conflicts with terminal state or host capability.
- `ResourceExhausted`: a configured server resource policy rejects the request.
- `Internal`: unexpected process, filesystem, cgroup, or output failure.

No List, Watch, Restart, Delete, or stdin RPC is included.

## Transport security

The server and CLI use TLS 1.3 only. TLS 1.3 mandates forward-secret key exchange and AEAD cipher suites; Go selects its TLS 1.3 cipher suites, so `CipherSuites` is not presented as a configurable TLS 1.3 whitelist. Curve preferences are X25519 followed by P-256.

The server uses `RequireAndVerifyClientCert` and a dedicated client CA pool. The client verifies the server CA and hostname; the development server certificate includes DNS `localhost` and IP `127.0.0.1` SANs. Certificates use ECDSA P-256 and appropriate server-auth or client-auth extended key usage.

Development certificates are pre-generated for reviewer convenience and a script documents reproducible generation. The running server does not depend on that script. Before committing private development keys, approval will be requested from the interview team; production keys would never be committed and would be issued and rotated by a real PKI.

## Authentication and authorization

After TLS verification, interceptors extract identity only from the verified leaf certificate. The prototype maps unique certificate common names to hard-coded roles:

```text
alice -> user
bob   -> user
admin -> admin
```

An unknown but CA-signed identity receives no role and is denied. Start records the caller as the immutable owner. Users may query, stream, or stop only their own jobs; admins may operate on every job. Ownership is checked inside each handler because it depends on the requested job. The streaming interceptor wraps `grpc.ServerStream` to provide the authenticated context.

The threat model includes unauthenticated peers, a client with an unknown CA-signed identity, and a user attempting to access another user's job. TLS protects data in transit, the role map is server-authoritative, job ownership is enforced on every RPC, UUIDs avoid PID-reuse identity errors, and cgroup-wide termination avoids PID-tree discovery races.

The prototype does not defend against a compromised authorized client executing malicious code within the worker's OS permissions, a compromised host, stolen certificate revocation, or denial of service within granted quotas. It also does not provide audit logs. These are stated limitations rather than security guarantees.

A production identity flow could exchange an OIDC identity for a short-lived X.509 workload/user certificate and encode identity in a URI SAN. That preserves the mTLS wire model, but replacing CN-based roles with SAN or group claims requires an authentication/authorization adapter change; it is not merely a CA deployment change.

## CLI UX

```text
jobctl [global flags] <command> [arguments]

Global flags:
  --server ADDRESS
  --cert PATH
  --key PATH
  --ca PATH

Commands:
  jobctl start [limit flags] -- COMMAND [ARG...]
  jobctl stop JOB_ID
  jobctl status JOB_ID
  jobctl output JOB_ID
```

Examples:

```bash
JOB_ID=$(jobctl start --cpu=500m --mem=512MiB -- /usr/bin/find /var/log -type f)
jobctl status "$JOB_ID"
jobctl output "$JOB_ID" > job-output.bin
jobctl stop "$JOB_ID"
```

The `--` separator is required. Arguments are passed literally and no shell is invoked by the worker. A user may explicitly choose `/bin/sh -c ...` as the executable if authorized, with the same implications as running any other arbitrary executable.

`start` prints only the UUID to stdout for shell composition. `status` is human-readable; structured JSON is future work. `output` writes raw bytes to stdout. Ctrl-C cancels or detaches that CLI operation and never implicitly stops the remote job. This matches the server-owned lifecycle and ensures one subscriber cannot stop a job observed by others. Default Go SIGPIPE behavior is retained for Unix pipeline compatibility. Errors go to stderr as `jobctl: <message>` and every failure returns a nonzero exit status.

## Concurrency and ownership

- The registry map is protected by one `sync.RWMutex`; no lock is held across process, filesystem, or network operations.
- Immutable job fields require no lock after construction.
- A job mutex protects state, stop intent, timestamps, and terminal result.
- Exactly one lifecycle goroutine calls `cmd.Wait`, performs cgroup cleanup, publishes the terminal state, and closes `done`.
- Exactly one output goroutine appends to the spool and publishes size changes.
- Subscribers own their offsets and never share mutable reader state.
- Closing a notification channel broadcasts to all subscribers; replacement occurs under the same mutex as the size/closed predicate, preventing lost wakeups.
- RPC cancellation detaches the caller but does not cancel a job or its ongoing server-side cleanup.

These invariants will be documented next to the owning types and exercised with the race detector.

## Testing strategy

Unit tests cover:

- State transitions, concurrent and repeated Stop calls, and start failures.
- Output replay from byte zero, binary data, chunk boundaries, slow and concurrent subscribers, cancellation, EOF, and the output limit.
- Limit parsing and exact cgroup file encodings.
- Authorization for owner, non-owner, admin, unknown identity, and missing identity.
- gRPC error mapping.

Linux integration tests, gated by `//go:build linux && integration`, run in a delegated cgroup fixture and cover:

- Atomic placement: the leader is present in the job cgroup at first observable execution.
- Descendant inheritance and `cgroup.kill` cleanup.
- A leader that exits while a descendant remains.
- CPU, memory, PIDs, and I/O limit file setup and representative enforcement failures.
- Missing controller, invalid device, insufficient permission, unavailable `clone3`, and cleanup errors.

TLS tests cover valid mTLS, untrusted client and server certificates, hostname mismatch, wrong EKU, expired certificates, and TLS versions below 1.3. End-to-end tests exercise all four CLI operations and two concurrent output clients.

CI runs formatting, vetting, ordinary tests, `go test -race ./...`, protobuf generation verification, and Linux integration tests in a suitably privileged runner. Reproducible commands are documented in the README.

## Trade-offs and future work

The design intentionally remains single-node and in-memory for job metadata. It does not recover after crashes, rotate certificates, isolate filesystems or networks, stream stdin, distinguish stdout from stderr, discover multiple I/O devices, or retain jobs according to a durable policy. Each is valuable in a production service but not required to demonstrate the core L5 properties.

The disk spool is chosen over an unbounded in-memory byte slice because job memory limits do not charge bytes retained by the worker. It is chosen over per-subscriber fan-out channels because a slow subscriber should not block execution or force silent output loss. Replaceable notification channels avoid polling and make context cancellation part of the normal wait operation.

Atomic cgroup placement is chosen over post-start PID migration because migration cannot retroactively constrain work or descendants created before the move. Requiring a modern kernel is preferable to presenting best-effort resource and cleanup guarantees.

## Pull-request plan

1. Design document and reviewer decisions.
2. Worker core: job model, registry, process/output lifecycle, and unit tests.
3. Cgroup v2 manager, atomic placement, whole-job termination, and Linux integration tests.
4. Protobuf API, gRPC server, TLS, authorization, and security tests.
5. CLI, end-to-end wiring, documentation, and reproducible build/test workflow.

Implementation begins only after the design receives the requested approvals. Any reviewer-requested change to lifecycle, cgroup placement, or output semantics will be reflected in this document before code is written.
