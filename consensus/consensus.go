// Package consensus provides checkpoint voting for the light node.
// It only concerns itself with the latest block — no history is verified.
package consensus

import (
	"context"
	"fmt"

	"github.com/arweave-light/client"
	"github.com/arweave-light/logger"
	"github.com/arweave-light/types"
)

// Voter runs multi-peer consensus exclusively on the latest block.
type Voter struct {
	mc           *client.MultiClient
	minConsensus int
	log          *logger.Logger
}

// NewVoter creates a consensus voter backed by a MultiClient.
func NewVoter(mc *client.MultiClient, minConsensus int) *Voter {
	if minConsensus < 1 {
		minConsensus = 1
	}
	return &Voter{
		mc:           mc,
		minConsensus: minConsensus,
		log:          logger.NewLogger("consensus"),
	}
}

// VoteLatestBlock queries multiple peers for the latest block and returns
// the consensus-winning block or an error. This is the sole entry point for
// establishing a trusted checkpoint on first boot (or after a long offline
// period).
func (v *Voter) VoteLatestBlock(ctx context.Context) (*types.Block, error) {
	// 1. Get network height via multi-peer consensus
	info, err := v.mc.GetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("consensus: get network info: %w", err)
	}
	height := info.Height

	v.log.Debug("Network height=%d via %d peers; voting on latest block...",
		height, v.minConsensus)

	// 2. Fetch the latest block via multi-peer consensus
	cr, err := v.mc.GetBlockByHeight(ctx, height)
	if err != nil {
		return nil, fmt.Errorf("consensus: vote on block %d: %w", height, err)
	}

	if !cr.Consensus {
		return nil, fmt.Errorf("consensus: no consensus on block %d (%d/%d agree, need %d)",
			height, cr.Agreed, cr.Total, v.minConsensus)
	}

	v.log.Info("Latest block %d: %d/%d peers agree, indep_hash=%s",
		height, cr.Agreed, cr.Total, cr.Block.IndepHash.String()[:16])

	return cr.Block, nil
}
