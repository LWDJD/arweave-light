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
2. **Multi-peer consensus** - Cross-validate data from multiple peers before accepting
3. **Chain continuity** - Security comes from previous_block chain, not PoW/VDF verification
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
Layer 1: Consensus Voting     → Find chain tip (weighted by peer score)
Layer 2: Chain Continuity     → Verify previous_block links
Layer 3: VDF Timelock         → Physical impossibility of fake chain
```

See [docs/security.md](docs/security.md) for details.

## Build

```bash
# Linux
go build -o arweave-light .

# Windows (cross-compile)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o arweave-light.exe .
```
