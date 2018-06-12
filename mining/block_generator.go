package mining

import (
	"context"
	"fmt"

	logging "gx/ipfs/QmPuosXfnE2Xrdiw95D78AhW41GYwGqpstKMf4TEsE4f33/go-log"
	xerrors "gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	cid "gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"
)

var log = logging.Logger("mining")

// GetStateTree is a function that gets a state tree by cid. It's
// its own function to facilitate testing.
type GetStateTree func(context.Context, *cid.Cid) (state.Tree, error)

// BlockGenerator is the primary interface for blockGenerator.
type BlockGenerator interface {
	Generate(_ context.Context, _ *types.Block, _ types.Signature, nullBlockCount uint64, _ types.Address) (*types.Block, error)
}

// NewBlockGenerator returns a new BlockGenerator.
func NewBlockGenerator(messagePool *core.MessagePool, getStateTree GetStateTree, applyMessages miningApplier) BlockGenerator {
	return &blockGenerator{
		messagePool:   messagePool,
		getStateTree:  getStateTree,
		applyMessages: applyMessages,
	}
}

type miningApplier func(ctx context.Context, messages []*types.Message, st state.Tree, bh *types.BlockHeight) (results []*core.ApplicationResult, permanentFailures []*types.Message,
	successfulMessages []*types.Message, temporaryFailures []*types.Message, err error)

// blockGenerator generates new blocks for inclusion in the chain.
type blockGenerator struct {
	messagePool   *core.MessagePool
	getStateTree  GetStateTree
	applyMessages miningApplier
}

// ApplyMessages applies messages to state tree and returns results,
// messages with permanent and temporary failures, and any error.
func ApplyMessages(ctx context.Context, messages []*types.Message, st state.Tree, bh *types.BlockHeight) (
	results []*core.ApplicationResult, permanentFailures []*types.Message, temporaryFailures []*types.Message,
	successfulMessages []*types.Message, err error) {
	var emptyResults []*core.ApplicationResult
	for _, msg := range messages {
		r, err := core.ApplyMessage(ctx, st, msg, bh)
		// If the message should not have been in the block, bail somehow.
		switch {
		case errors.IsFault(err):
			return emptyResults, permanentFailures, temporaryFailures, successfulMessages, err
		case errors.IsApplyErrorPermanent(err):
			permanentFailures = append(permanentFailures, msg)
			continue
		case errors.IsApplyErrorTemporary(err):
			temporaryFailures = append(temporaryFailures, msg)
			continue
		case err != nil:
			err = fmt.Errorf("someone is a bad programmer: must be a fault, perm, or temp error: %s", err.Error())
			return emptyResults, permanentFailures, temporaryFailures, successfulMessages, err
		default:
			// TODO fritz check caller assumptions about receipts.
			successfulMessages = append(successfulMessages, msg)
			results = append(results, r)
		}
	}
	return results, permanentFailures, temporaryFailures, successfulMessages, nil
}

// Generate returns a new block created from the messages in the pool.
func (b blockGenerator) Generate(ctx context.Context, baseBlock *types.Block, ticket types.Signature, nullBlockCount uint64, rewardAddress types.Address) (blk *types.Block, err error) {
	ctx = log.Start(ctx, "Generate")
	defer func() {
		log.SetTags(ctx, map[string]interface{}{
			"base-block":       baseBlock.Cid().String(),
			"reward-address":   rewardAddress.String(),
			"ticket":           string(ticket),
			"null-block-count": nullBlockCount,
			"new-block":        blk.Cid().String(),
		})
	}()
	stateTree, err := b.getStateTree(ctx, baseBlock.StateRoot)
	if err != nil {
		return nil, err
	}

	nonce, err := core.NextNonce(ctx, stateTree, b.messagePool, address.NetworkAddress)
	if err != nil {
		return nil, err
	}

	blockHeight := baseBlock.Height + nullBlockCount + 1
	rewardMsg := types.NewMessage(address.NetworkAddress, rewardAddress, nonce, types.NewTokenAmount(1000), "", nil)
	pending := b.messagePool.Pending()
	messages := make([]*types.Message, len(pending)+1)
	messages[0] = rewardMsg // Reward message must come first since this is a part of the consensus rules.
	copy(messages[1:], core.OrderMessagesByNonce(b.messagePool.Pending()))

	results, permanentFailures, temporaryFailures, successfulMessages, err := b.applyMessages(ctx, messages, stateTree, types.NewBlockHeight(blockHeight))
	if err != nil {
		return nil, err
	}

	newStateTreeCid, err := stateTree.Flush(ctx)
	if err != nil {
		return nil, err
	}

	var receipts []*types.MessageReceipt
	for _, r := range results {
		receipts = append(receipts, r.Receipt)
	}

	next := &types.Block{
		Miner:           rewardAddress,
		Height:          blockHeight,
		Messages:        successfulMessages,
		MessageReceipts: receipts,
		Parents:         types.NewSortedCidSet(baseBlock.Cid()),
		StateRoot:       newStateTreeCid,
		Ticket:          ticket,
	}

	var rewardSuccessful bool
	for _, msg := range successfulMessages {
		if msg == rewardMsg {
			rewardSuccessful = true
		}
	}
	if !rewardSuccessful {
		return nil, xerrors.New("mining reward message failed")
	}
	// Mining reward message succeeded -- side effects okay below this point.

	for _, msg := range successfulMessages {
		mc, err := msg.Cid()
		if err == nil {
			b.messagePool.Remove(mc)
		}
	}

	// TODO: Should we really be pruning the message pool here at all? Maybe this should happen elsewhere.
	for _, msg := range permanentFailures {
		// We will not be able to apply this message in the future because the error was permanent.
		// Therefore, we will remove it from the MessagePool now.
		mc, err := msg.Cid()
		log.Infof("permanent ApplyMessage failure, [%S]", mc.String())
		// Intentionally not handling error case, since it just means we won't be able to remove from pool.
		if err == nil {
			b.messagePool.Remove(mc)
		}
	}

	for _, msg := range temporaryFailures {
		// We might be able to apply this message in the future because the error was temporary.
		// Therefore, we will leave it in the MessagePool for now.
		mc, _ := msg.Cid()
		log.Infof("temporary ApplyMessage failure, [%S]", mc.String())
	}

	return next, nil
}
