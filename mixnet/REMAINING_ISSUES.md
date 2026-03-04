# Remaining Issues Report - Requirements 11-20

**Date:** 2026-03-05
**Focus:** Maximum Code Reuse from libp2p - No Reinventing the Wheel

---

## Executive Summary

The implementation has been significantly improved with proper code reuse from libp2p:
- Uses golang.org/x/crypto/chacha20poly1305 (same cipher as Noise)
- Uses github.com/libp2p/go-libp2p/p2p/protocol/ping for RTT (not raw TCP)
- Uses github.com/klauspost/reedsolomon for erasure coding
- Uses standard libp2p host.SetStreamHandler() for protocol registration

---

## Issues Requiring Fixes

### CRITICAL - Not Wired Up

1. Protocol Verification (Req 12) - Peers not verified to advertise /lib-mix/1.0.0
   Should use: h.Peerstore().Protocols(peerID)
   Location: mixnet/upgrader.go:discoverRelays()

2. Graceful Shutdown Ack (Req 18) - Ack channel created but nothing sends
   Should use: libp2p connection notifiers
   Location: mixnet/upgrader.go:Close()

3. Metrics Exposure (Req 17, 20.5) - Metrics collected but not exposed
   Should use: prometheus client_golang
   Location: mixnet/metrics.go

4. Secure Key Erasure (Req 16.3, 18.4) - SecureErase() could be optimized
   Location: mixnet/ces/encryption.go

### HIGH - Partially Wired

5. Stream Upgrader Interface (Req 13) - Custom interface doesn't integrate with libp2p
6. Active Failure Detection (Req 10.1) - DetectFailure() only checks state flag
7. Key Exchange (Req 14, 16) - Keys generated but never transmitted

### LOW - Configuration/Polish

8. Runtime Config Changes (Req 15.5) - No mechanism to update config
9. Multiaddr Parsing (Req 11.4) - Manual string parsing in some places

---

## What's Already Properly Wired Up

| Component | Implementation | libp2p Reuse |
|----------|---------------|----------------|
| Encryption | ChaCha20-Poly1305 | Same cipher as Noise |
| Key Derivation | HKDF-SHA256 | Same as Noise Protocol |
| RTT Measurement | libp2p ping protocol | p2p/protocol/ping |
| Erasure Coding | Reed-Solomon | klauspost/reedsolomon |
| Protocol Registration | SetStreamHandler() | Standard libp2p |
| Circuit Limits | maxCircuits check | Enforced in handler |
| Bandwidth Limits | rateLimitedWriter | Per-circuit |
| Config Validation | HopCount 1-10, CircuitCount 1-20 | Validated |

---

## Summary

| Priority | Count | Can Fix With libp2p |
|----------|-------|---------------------|
| Critical | 4 | Yes - use Peerstore, metrics, notifiers |
| High | 3 | Partial - some need custom protocol |
| Low | 2 | Documentation/config |

Total Remaining: 9 issues
Code Reuse Status: ~85% of crypto/networking properly reuses libp2p modules
