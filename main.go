package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arweave-light/client"
	"github.com/arweave-light/node"
	"github.com/arweave-light/types"
	"github.com/arweave-light/verifier"
)

var (
	version   = "0.3.0"
	buildTime = "unknown"
)

func main() {
	cfg := node.DefaultConfig()

	dataDir := flag.String("data-dir", cfg.DataDir, "Data directory for storage")
	peerURL := flag.String("peer", cfg.PeerURL, "Fallback peer URL (arweave.net, height-only)")
	timeout := flag.Int("timeout", int(cfg.HTTPTimeout.Seconds()), "HTTP timeout in seconds")
	noSync := flag.Bool("no-sync", false, "Disable automatic synchronization")
	noValidate := flag.Bool("no-validate", false, "Disable block validation")

	consensus := flag.Bool("consensus", cfg.Consensus, "Enable multi-peer consensus voting (default true)")
	bootstrap := flag.String("bootstrap", "", "Bootstrap peer URL (required on first run)")
	addPeer := flag.String("add-peer", "", "Manually add a peer URL")
	listPeers := flag.Bool("list-peers", false, "List all known peers and exit")
	minConsensus := flag.Int("min-consensus", cfg.MinConsensus, "Minimum agreeing peers for consensus (default 3)")

	queryBlock := flag.Uint64("block", 0, "Query a specific block by height")
	queryTx := flag.String("tx", "", "Query a transaction by ID (base64)")
	queryTxData := flag.String("tx-data", "", "Fetch transaction data by ID (base64)")
	queryInfo := flag.Bool("info", false, "Show network info and exit")
	queryStatus := flag.Bool("status", false, "Show sync status")
	queryStats := flag.Bool("stats", false, "Show node statistics")
	showVersion := flag.Bool("version", false, "Show version")

	genesisVerify := flag.Bool("genesis-verify", false, "Run full chain verification from genesis/checkpoint")
	genesisFrom := flag.Uint64("genesis-from", 0, "Starting height for genesis verification")
	genesisTo := flag.Uint64("genesis-to", 0, "Ending height (default: network tip)")
	genesisWorkers := flag.Int("genesis-workers", 4, "Number of concurrent fetch workers")
	genesisForce := flag.Bool("genesis-force", false, "Ignore existing checkpoint, re-verify from --genesis-from")

	showCheckpoint := flag.Bool("checkpoint", false, "Show current trusted checkpoint")
	clearCheckpoint := flag.Bool("checkpoint-clear", false, "Remove trusted checkpoint")

	flag.Parse()

	if *showVersion {
		fmt.Printf("arweave-light v%s (built %s)\n", version, buildTime)
		os.Exit(0)
	}

	cfg.DataDir = *dataDir
	cfg.PeerURL = client.NormalizePeerURL(*peerURL)
	cfg.HTTPTimeout = time.Duration(*timeout) * time.Second
	cfg.ValidateBlocks = !*noValidate
	cfg.Consensus = *consensus
	cfg.MinConsensus = *minConsensus

	if *noSync {
		cfg.SyncEnabled = false
	}

	cfg.Bootstrap = client.NormalizePeerURL(*bootstrap)
	cfg.AddPeer = client.NormalizePeerURL(*addPeer)
	cfg.ListPeers = *listPeers

	n, err := node.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create node: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received interrupt signal")
		cancel()
		n.Stop()
		os.Exit(0)
	}()

	if cfg.ListPeers {
		fmt.Println("\n==== Known Peers ====")
		allPeers := n.PeerStore().GetAll()
		if len(allPeers) == 0 {
			fmt.Println("(no peers)")
		} else {
			for i, p := range allPeers {
				fmt.Printf("%2d. %-50s score=%4d  success=%d  fail=%d\n",
					i+1, p.URL, p.Score, p.SuccessCount, p.FailCount)
			}
		}
		fmt.Printf("Total: %d peers\n\n", len(allPeers))
	}

	if *showCheckpoint || *clearCheckpoint {
		cpStore := verifier.NewCheckpointStore(*dataDir)
		if *clearCheckpoint {
			if cpStore.Exists() {
				cpStore.Remove()
				fmt.Println("Checkpoint cleared.")
			} else {
				fmt.Println("No checkpoint to clear.")
			}
		} else {
			if !cpStore.Exists() {
				fmt.Println("No trusted checkpoint found. Run --genesis-verify first.")
			} else {
				cp, err := cpStore.Load()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error loading checkpoint: %v\n", err)
				} else {
					fmt.Printf("Trusted Checkpoint:\n")
					fmt.Printf("  Height:        %d\n", cp.Height)
					fmt.Printf("  Indep Hash:    %s\n", cp.IndepHash)
					fmt.Printf("  Verified:      %d blocks\n", cp.VerifiedCount)
					fmt.Printf("  Timestamp:     %s\n", time.Unix(cp.Timestamp, 0).Format(time.RFC3339))
				}
			}
		}
		n.Stop()
		return
	}

	if *genesisVerify {
		cpStore := verifier.NewCheckpointStore(*dataDir)
		val := n.GetValidator()
		gv := verifier.NewGenesisVerifier(n.MultiClient(), cpStore, val, *genesisWorkers)
		fmt.Printf("Starting genesis chain verification...\n")
		fmt.Printf("  From:    height %d\n", *genesisFrom)
		fmt.Printf("  To:      height %d (0 = network tip)\n", *genesisTo)
		fmt.Printf("  Force:   %v\n", *genesisForce)
		fmt.Printf("  Workers: %d\n", *genesisWorkers)
		result, err := gv.Verify(ctx, *genesisFrom, *genesisTo, *genesisForce)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Genesis verification failed: %v\n", err)
		} else if result.FailedReason != "" {
			fmt.Fprintf(os.Stderr, "\nVERIFICATION FAILED at height %d: %s\n",
				result.FailedHeight, result.FailedReason)
		} else {
			fmt.Printf("\nGenesis verification complete!\n")
			fmt.Printf("   Verified:  %d blocks (%d -> %d)\n", result.VerifiedCount, result.StartHeight, result.EndHeight)
			fmt.Printf("   Duration:  %s\n", result.Duration.Round(time.Second))
			if result.Checkpoint != nil {
				fmt.Printf("   Checkpoint saved at height %d\n", result.Checkpoint.Height)
			}
		}
		n.Stop()
		return
	}

	if *queryInfo || *queryBlock > 0 || *queryTx != "" || *queryTxData != "" || *queryStatus || *queryStats {
		handleQueries(ctx, n, *queryInfo, *queryBlock, *queryTx, *queryTxData, *queryStatus, *queryStats)
		n.Stop()
		return
	}

	if cfg.ListPeers && !cfg.SyncEnabled {
		n.Stop()
		return
	}

	if err := n.Start(ctx); err != nil {
		log.Fatalf("Failed to start node: %v", err)
	}

	log.Println("arweave-light is running. Press Ctrl+C to stop.")
	for evt := range n.Events() {
		switch evt.Type {
		case node.EventBlock:
			if block, ok := evt.Data.(*types.Block); ok {
				log.Printf("[event] New block: height=%d hash=%s txs=%d",
					block.Height, block.Hash.String()[:16], len(block.Txs))
			}
		case node.EventSyncComplete:
			log.Printf("[event] Sync complete at height %v", evt.Data)
		case node.EventError:
			log.Printf("[event] Error: %v", evt.Data)
		case node.EventFork:
			log.Printf("[event] Fork detected at height %v", evt.Data)
		}
	}

	n.Stop()
}

func handleQueries(ctx context.Context, n *node.Node, info bool, blockHeight uint64, txID string, txData string, status bool, stats bool) {
	if info {
		netInfo, err := n.GetNetworkInfo(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching network info: %v\n", err)
		} else {
			fmt.Printf("Network height: %d\n", netInfo.Height)
			fmt.Printf("Current hash: %s\n", netInfo.CurrentHash)
		}
	}

	if blockHeight > 0 {
		block, err := n.FetchBlockByHeight(ctx, blockHeight)
		if err != nil {
			block, err = n.GetBlockByHeight(blockHeight)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Block %d not found\n", blockHeight)
				return
			}
		}
		data, _ := json.MarshalIndent(block, "", "  ")
		fmt.Println(string(data))
	}

	if txID != "" {
		var h types.Hash
		if err := h.UnmarshalJSON([]byte(`"` + txID + `"`)); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid transaction ID: %v\n", err)
		} else {
			tx, err := n.GetTransaction(ctx, h)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			} else {
				data, _ := json.MarshalIndent(tx, "", "  ")
				fmt.Println(string(data))
			}
		}
	}

	if txData != "" {
		var h types.Hash
		if err := h.UnmarshalJSON([]byte(`"` + txData + `"`)); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid transaction ID: %v\n", err)
		} else {
			data, err := n.GetTransactionData(ctx, h)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			} else {
				fmt.Print(string(data))
			}
		}
	}

	if status {
		s := n.GetSyncStatus()
		// Also fetch network height for context
		netH, netErr := n.FetchNetworkHeight(ctx)
		if netErr == nil {
			s.TargetHeight = netH
			if netH > s.CurrentHeight {
				s.BlocksBehind = netH - s.CurrentHeight
			}
		}
		fmt.Printf("Sync Status:\n")
		fmt.Printf("  Syncing:        %v\n", s.Syncing)
		fmt.Printf("  Current Height: %d\n", s.CurrentHeight)
		fmt.Printf("  Network Height: %d\n", s.TargetHeight)
		fmt.Printf("  Blocks Behind:  %d\n", s.BlocksBehind)
	}

	if stats {
		s := n.Stats()
		data, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(data))
	}
}
