package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/arweave-light/node"
	"github.com/arweave-light/types"
)

var (
	version   = "0.2.0"
	buildTime = "unknown"
)

func main() {
	cfg := node.DefaultConfig()

	dataDir := flag.String("data-dir", cfg.DataDir, "Data directory for storage")
	peerURL := flag.String("peer", cfg.PeerURL, "Arweave node URL (fallback / legacy)")
	timeout := flag.Int("timeout", int(cfg.HTTPTimeout.Seconds()), "HTTP timeout in seconds")
	noSync := flag.Bool("no-sync", false, "Disable automatic synchronization")
	noValidate := flag.Bool("no-validate", false, "Disable block validation")

	// New peer-discovery flags
	bootstrap := flag.String("bootstrap", "", "Bootstrap peer URL (required on first run)")
	addPeer := flag.String("add-peer", "", "Manually add a peer URL")
	listPeers := flag.Bool("list-peers", false, "List all known peers and exit")
	minConsensus := flag.Int("min-consensus", cfg.MinConsensus, "Minimum agreeing peers for consensus (default 3)")

	// Query flags
	queryBlock := flag.Uint64("block", 0, "Query a specific block by height")
	queryTx := flag.String("tx", "", "Query a transaction by ID (base64)")
	queryTxData := flag.String("tx-data", "", "Fetch transaction data by ID (base64)")
	queryInfo := flag.Bool("info", false, "Show network info and exit")
	queryStatus := flag.Bool("status", false, "Show sync status")
	queryStats := flag.Bool("stats", false, "Show node statistics")
	showVersion := flag.Bool("version", false, "Show version")

	flag.Parse()

	if *showVersion {
		fmt.Printf("arweave-light v%s (built %s)\n", version, buildTime)
		os.Exit(0)
	}

	cfg.DataDir = *dataDir
	cfg.PeerURL = strings.TrimRight(*peerURL, "/")
	cfg.HTTPTimeout = time.Duration(*timeout) * time.Second
	cfg.ValidateBlocks = !*noValidate

	if *noSync {
		cfg.SyncEnabled = false
	}

	// New config fields
	cfg.Bootstrap = strings.TrimRight(*bootstrap, "/")
	cfg.AddPeer = strings.TrimRight(*addPeer, "/")
	cfg.ListPeers = *listPeers
	cfg.MinConsensus = *minConsensus

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

	// Handle --list-peers (can be combined with other queries or standalone)
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
		fmt.Printf("Total: %d peers (max %d)\n\n", len(allPeers), 50)
	}

	if *queryInfo || *queryBlock > 0 || *queryTx != "" || *queryTxData != "" || *queryStatus || *queryStats {
		handleQueries(ctx, n, *queryInfo, *queryBlock, *queryTx, *queryTxData, *queryStatus, *queryStats)
		n.Stop()
		return
	}

	// If only --list-peers was requested and nothing else, exit
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
					block.Height, block.Hash, len(block.Txs))
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
		block, err := n.GetBlockByHeight(blockHeight)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Block %d not found\n", blockHeight)
		} else {
			data, _ := json.MarshalIndent(block, "", "  ")
			fmt.Println(string(data))
		}
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
		data, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(data))
	}

	if stats {
		s := n.Stats()
		data, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(data))
	}
}
