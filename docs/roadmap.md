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

### ✅ Done (v0.3.6)
- [x] Weighted consensus voting (by credibility score)
- [x] Hardcoded trusted seed peers (compiled-in, elevated initial score)
- [x] Checkpoint-based trust (no consensus override on fork)
- [x] Independent peer discovery loop (separate goroutine, 10s interval, random peers)
- [x] Security level system (--security low|high, ratio-based consensus)

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
| 2026-05-07 | Weighted consensus voting | Credit-score-based weighting prevents Sybil attacks; >51% ratio required |
| 2026-05-07 | Trusted seed nodes compiled in | Reduces bootstrap reliance on single arweave.net, seeds get elevated score |
| 2026-05-07 | Checkpoint trust is one-way ratchet | Once established, fork detection logs warning but doesn't auto-rollback |
| 2026-05-07 | **Heaviest chain rule** (最高累计难度) | 官方源码确认：Arweave 分叉选择规则是 **heaviest chain**（`cumulative_diff` 最高），不是 "最小 indep_hash"。bootstrap 后用多节点拉数据 + `cumulative_diff` 比较选出 canonical 块，不走投票 |
| 2026-05-07 | **共识只用于首次 bootstrap** | 每个块含 `hash_list_merkle`（所有历史块 hash 的 Merkle 根），首次共识找到链头后，后续**逐块验证** `previous_block` 链连续性，不再需要共识投票做安全决策 |
| 2026-05-07 | `hash_list_merkle` 验证 | 利用每个块里的历史 Merkle 根做随机抽查验证，增强链连续性之外的第二层安全保障 |
