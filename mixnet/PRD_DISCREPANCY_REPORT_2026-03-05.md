# Mixnet PRD/Design Discrepancy Report

**Date:** 2026-03-05

This report lists discrepancies between the implementation in `/mixnet` and the PRD/design documents. Each item includes a **Parallelism** tag indicating whether it can be fixed in parallel or depends on prior work.

Legend:
- **Parallel:** Can be fixed independently.
- **Depends:** Requires earlier fixes to be implemented first.

---

## Critical Discrepancies

1. **Circuit diversity + failure semantics do not meet Req 6.2 / 6.6** — **DONE**
   - **Issue:** Circuits are built via `BuildCircuit()` from a shared pool without enforcing distinct relay sets per circuit. Partial failures are tolerated as long as threshold is met; Req 6.6 requires tearing down all circuits on any failure.
   - **Files:** `mixnet/upgrader.go` (EstablishConnection), `mixnet/circuit/manager.go` (BuildCircuit)
   - **PRD:** Req 6.2, 6.6
   - **Parallelism:** **Depends** (requires relay selection + pool shaping fixes first to enforce diversity deterministically)

2. **Relay discovery does not enforce PRD filtering + pool sizing (Req 4.2–4.5)** — **DONE**
   - **Issue:** `discoverRelays` does not exclude origin/destination and does not enforce “3× required relays” pool size before proceeding.
   - **Files:** `mixnet/upgrader.go` (discoverRelays)
   - **PRD:** Req 4.2–4.5
   - **Parallelism:** **Parallel**

3. **Latency-based selection is effectively unused (Req 5)** — **DONE**
   - **Issue:** `SelectRelays` sorts by latency but no RTT measurement is performed during discovery. `relayInfos` have zero RTT.
   - **Files:** `mixnet/upgrader.go` (discoverRelays), `mixnet/discovery/dht.go` (SelectRelays)
   - **PRD:** Req 5.1–5.4
   - **Parallelism:** **Parallel**

4. **Onion routing header leaks final destination to entry relay (Req 14.1–14.3)** — **DONE**
   - **Issue:** All hop destinations are set to `dest.String()`, so the outermost layer reveals the final destination.
   - **Files:** `mixnet/upgrader.go` (Send)
   - **PRD:** Req 14.1–14.3
   - **Parallelism:** **Depends** (requires correct per-hop routing header format and relay forwarding changes)

5. **Inbound reconstruction flow incomplete (Req 9)** — **DONE**
   - **Issue:** Key material is never parsed or assigned to `DestinationHandler` (`SetKeys` unused). No 30s shard buffering timeout. Reconstruction attempted immediately per shard.
   - **Files:** `mixnet/upgrader.go` (handleIncomingStream, TryReconstruct)
   - **PRD:** Req 9.1–9.6
   - **Parallelism:** **Depends** (requires Send header format and key exchange protocol to be finalized)

6. **Relay forwarding buffers full payload and closes stream (Req 7.4–7.5)** — **DONE**
   - **Issue:** `io.ReadAll` reads full payload into memory and closes stream after forwarding; violates streaming/keep-open requirements.
   - **Files:** `mixnet/relay/handler.go`
   - **PRD:** Req 7.4–7.5
   - **Parallelism:** **Depends** (depends on final hop header format for streaming parsing)

7. **Noise XX key exchange not implemented; keys not ephemeral per circuit (Req 16.1–16.5)** — **DONE**
   - **Issue:** Key derivation is deterministic from prologue; no Noise handshake. `KeyManager` exists but is unused.
   - **Files:** `mixnet/noise_key_exchange.go`, `mixnet/onion.go`, `mixnet/relay/key_exchange.go`, `mixnet/relay/handler.go`, `mixnet/upgrader.go`
   - **PRD:** Req 16.1–16.5
   - **Parallelism:** **Depends** (requires protocol definition changes and relay key distribution)

---

## High Severity Gaps

8. **Stream Upgrader not integrated with libp2p upgrader flow (Req 13)** — **DONE**
   - **Fix:** Removed fake upgrader methods; added protocol-level `OpenStream` with `MixStream` to provide read/write semantics over mixnet without transport-upgrader misuse.
   - **Files:** `mixnet/stream.go`, `mixnet/upgrader.go`, `mixnet/failure_detection.go`
   - **PRD:** Req 13.1–13.5
   - **Parallelism:** **Parallel**

9. **Failure detection is implemented but not wired to run (Req 10.1–10.4)** — **DONE**
   - **Fix:** `CircuitFailureNotifier` now starts during `NewMixnet`, stops during `Close`, disconnection-to-circuit mapping was corrected, and heartbeat monitoring is started after successful circuit establishment with deduping.
   - **Files:** `mixnet/failure_detection.go`, `mixnet/upgrader.go`
   - **PRD:** Req 10.1–10.4
   - **Parallelism:** **Parallel**

10. **Shard distribution does not honor “exact circuit count” (Req 2.4, 8.1–8.2)** — **DONE**
    - **Issue:** Excess shards are dropped when circuits < shards.
    - **Files:** `mixnet/upgrader.go` (Send)
    - **PRD:** Req 2.4, 8.1–8.2
    - **Parallelism:** **Depends** (needs circuit construction & CES shard count alignment)

11. **Graceful shutdown lacks acknowledgments (Req 18.2–18.5)** — **DONE**
   - **Issue:** Close sends a signal but no relay ack protocol exists; CloseCircuitWithContext only closes locally.
   - **Files:** `mixnet/upgrader.go`, `mixnet/circuit/manager.go`
   - **PRD:** Req 18.2–18.5
   - **Parallelism:** **Depends** (requires relay protocol changes)

12. **Metrics collected but not exposed (Req 17)** — **DONE**
    - **Fix:** Added Prometheus exporter with /metrics endpoint and Mixnet helpers to serve metrics.
    - **Files:** `mixnet/metrics.go`, `mixnet/metrics_exporter.go`, `mixnet/upgrader.go`
    - **PRD:** Req 17.1–17.6
    - **Parallelism:** **Parallel**

---

## Reinvented Wheel / Missed libp2p Reuse (No Justification)

13. **Custom RTT ping duplicates libp2p ping service** **(DONE)**
    - **Issue:** `LatencyMeasurer.MeasureRTT` uses a custom “ping” stream instead of libp2p `p2p/protocol/ping` used elsewhere.
    - **Files:** `mixnet/relay_discovery.go`, `mixnet/discovery/dht.go`
    - **Parallelism:** **Parallel**

14. **Resource limits implemented outside libp2p resource manager** — **DONE**
    - **Fix:** Relay handler now uses libp2p rcmgr for outbound stream admission (`OpenStream` + protocol/service tagging) and per-frame inbound memory reservation on stream scopes. `NewMixnetWithResources` now creates an rcmgr-backed resource manager and wires relay resource mode to libp2p by default.
    - **Files:** `mixnet/resource_management.go`, `mixnet/relay/handler.go`
    - **Parallelism:** **Parallel**

15. **Duplicate relay discovery layers** — **DONE**
    - **Fix:** `mixnet/relay_discovery.go` now delegates to canonical `mixnet/discovery/*` implementation; duplicate filtering/RTT/selection logic removed.
    - **Files:** `mixnet/relay_discovery.go`, `mixnet/discovery/dht.go`
    - **Parallelism:** **Parallel**

16. **Transport capability detection is unused** — **DONE**
    - **Issue:** `privacy_transport.go` has transport detection/verification helpers with no call sites.
    - **Files:** `mixnet/privacy_transport.go`
    - **Parallelism:** **Parallel**

---

## Other Gaps

17. **Config immutability during active circuits not enforced (Req 15.5)**
    - **Issue:** Config setters allow changes while circuits are active; PRD says reject config changes while active.
    - **Files:** `mixnet/config.go`
    - **Parallelism:** **Parallel**
    - **Status:** **DONE (2026-03-05)**

18. **Structured error model exists but not used consistently (Req 19)** — **DONE**
    - **Fix:** Core entrypoints now return `MixnetError` codes for discovery, circuit, encryption, sharding, and transport failures.
    - **Files:** `mixnet/upgrader.go`
    - **Parallelism:** **Parallel**

19. **Privacy padding/cover traffic defined but not wired** — **DONE**
    - **Issue:** `EncodePrivacyShard` and padding config exist but are unused in Send.
    - **Files:** `mixnet/privacy_transport.go`, `mixnet/upgrader.go`
    - **Parallelism:** **Depends** (ties into shard header and relay parsing)
