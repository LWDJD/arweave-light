# Project Roadmap

## Current Status (2026-05-07)

**Version**: v0.3.x — MVP stage. Core sync loop works, still iterating on peer discovery & consensus.

### ✅ Done
- [x] Basic CLI framework (flags, commands)
- [x] Multi-peer discovery from arweave.net
- [x] Peer store with EMA credibility scoring
- [x] Peer persistence (peers.json)
- [x] Consensus voting (simple majority)
- [x] Block data types & parsing
- [x] Block sync via single peer
- [x] Logging system (levels, verbose/quiet, file output)
- [x] Default data directory (./ar-data)
- [x] --info uses local peers (not hardcoded arweave.net)
- [x] Remove broken PoW difficulty check (VDF not compatible)
- [x] Fix tx_root validation for light nodes (structural check only)
- [x] Peer discovery on low peer count
- [x] Immediate peer refresh after startup

### 🔄 In Progress (Pro working on)
- [ ] Independent peer discovery loop (separate goroutine, every 10s)
- [ ] Security level system (--security low|high, ratio-based consensus)
- [ ] Weighted consensus voting (by credibility score)
- [ ] Hardcoded trusted seed peers
- [ ] Checkpoint-based trust (no consensus override)

### 📋 Planned
- [ ] Proper error handling & crash recovery
- [ ] Config file support (arweave-light.toml)
- [ ] Graceful shutdown with state save
- [ ] Rate limiting / backpressure
- [ ] Data pruning (keep only recent blocks)
- [ ] Transaction data retrieval from peers
- [ ] 48-byte hash (post-2.6) vs 32-byte hash handling
- [ ] Windows service / systemd integration
- [ ] CI/CD pipeline
- [ ] Unit test coverage > 60%

### 🔮 Future Ideas
- [ ] IPFS ↔ Arweave bridge (DHT + Bitswap approach)
- [ ] REST API for local queries
- [ ] Web UI dashboard
- [ ] Docker image

## Key Decisions Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-05-07 | Default data-dir = ./ar-data | User consistency |
| 2026-05-07 | Remove PoW difficulty check | VDF not compatible; trust consensus + chain continuity |
| 2026-05-07 | tx_root validation = structural only | Light node lacks data_root/offset info to fully verify |
| 2026-05-07 | Peer discovery as independent goroutine | Need continuous refresh, not tied to sync cycle |
| 2026-05-07 | Security levels (low/high) replace min-consensus | More flexible, ratio-based instead of fixed number |
