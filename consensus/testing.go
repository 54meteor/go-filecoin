package consensus

import (
	"context"

	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"gx/ipfs/QmcD7SqfyQyA91TZUQ7VPRYbGarxmY7EsQewVYMuN5LNSv/go-ipfs-blockstore"

	"github.com/stretchr/testify/require"
)

// TestView is an implementation of stateView used for testing the chain
// manager.  It provides a consistent view that the storage market
// stores 1 byte and all miners store 0 bytes regardless of inputs.
type TestView struct{}

var _ PowerTableView = &TestView{}

// Total always returns 1.
func (tv *TestView) Total(ctx context.Context, st state.Tree, bstore blockstore.Blockstore) (uint64, error) {
	return uint64(1), nil
}

// Miner always returns 0.
func (tv *TestView) Miner(ctx context.Context, st state.Tree, bstore blockstore.Blockstore, mAddr types.Address) (uint64, error) {
	return uint64(0), nil
}

// HasPower always returns true.
func (tv *TestView) HasPower(ctx context.Context, st state.Tree, bstore blockstore.Blockstore, mAddr types.Address) bool {
	return true
}

// RequireNewTipSet instantiates and returns a new tipset of the given blocks
// and requires that the setup validation succeed.
func RequireNewTipSet(require *require.Assertions, blks ...*types.Block) TipSet {
	ts, err := NewTipSet(blks...)
	require.NoError(err)
	return ts
}

// RequireTipSetAdd adds a block to the provided tipset and requires that this
// does not error.
func RequireTipSetAdd(require *require.Assertions, blk *types.Block, ts TipSet) {
	err := ts.AddBlock(blk)
	require.NoError(err)
}
