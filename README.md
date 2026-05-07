# arweave-light

A lightweight, pure decentralized Arweave light node client.

## Architecture

```
arweave-light/
├── main.go          # CLI entry point
├── node/            # Node lifecycle & orchestration
├── client/          # HTTP & multi-peer consensus client
├── peers/           # Peer discovery & credibility scoring
├── syncer/          # Block synchronization logic
├── store/           # LevelDB block storage
├── validator/       # Block validation (chain continuity, tx_root)
├── verifier/        # Full chain verification & checkpoint
├── types/           # Shared data types
├── logger/          # Logging system
└── merkle/          # Merkle tree utilities
```

## Design Principles

1. **Pure decentralized** - No hardcoded dependency on arweave.net after initial bootstrap
2. **Consensus only for bootstrap** - Multi-peer voting used once to find chain tip; after that, sequential block verification via `previous_block` chain
3. **Chain continuity** - Security comes from `previous_block` chain and `hash_list_merkle`, not PoW/VDF verification
4. **Credibility scoring** - Peers are rated by reliability; weighted voting prevents Sybil attacks

## Quick Start

```bash
# Run daemon (auto syncs from network)
./arweave-light

# Query network info
./arweave-light --info

# Query a block
./arweave-light --block 1912614

# Query a transaction
./arweave-light --tx <base64-tx-id>
```

See [docs/usage.md](docs/usage.md) for complete documentation.

## Security Model

```
Bootstrap Phase (one-time):
  Layer 1: Consensus Voting     → Find chain tip (weighted by peer score)

Post-Bootstrap (every block):
  Layer 2: Chain Continuity     → Verify previous_block links (每块验证)
  Layer 3: hash_list_merkle     → Cross-check historical block inclusion (随机抽查)
  Layer 4: VDF Timelock         → Physical impossibility of fake chain
```

**核心原则**：多节点共识只用于第一次启动时的链头发现。之后每个块必须通过 `previous_block` 链逐块验证，不再依赖共识投票做安全决策。每个 Arweave 块都包含 `hash_list_merkle`——所有历史块 `indep_hash` 的 Merkle 根，可用做随机抽查验证。

See [docs/security.md](docs/security.md) for details.

## Build

```bash
# Linux
go build -o arweave-light .

# Windows (cross-compile)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o arweave-light.exe .
```
