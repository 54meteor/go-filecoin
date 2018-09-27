package consensus

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"

	"gx/ipfs/QmSkuaNgyGmV8c1L3cZNWcUxRJV6J3nsD96JVQPcWcwtyW/go-hamt-ipld"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmYVNvtQkeZ6AKSwDrjQTs432QtL6umrrK41EBq3cu7iSP/go-cid"
	"gx/ipfs/QmcD7SqfyQyA91TZUQ7VPRYbGarxmY7EsQewVYMuN5LNSv/go-ipfs-blockstore"
	logging "gx/ipfs/QmcVVHfdyv15GVPk7NrxdWjh2hLVccXnoD8j2tyQShiXJb/go-log"

	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm"
)

var log = logging.Logger("consensus.expected")

var (
	// ErrStateRootMismatch is returned when the computed state root doesn't match the expected result.
	ErrStateRootMismatch = errors.New("blocks state root does not match computed result")
	// ErrInvalidBase is returned when the chain doesn't connect back to a known good block.
	ErrInvalidBase = errors.New("block does not connect to a known good chain")
	// ErrUnorderedTipSets is returned when weight and minticket are the same between two tipsets.
	ErrUnorderedTipSets = errors.New("two tipsets exist with the same weight and min ticket")
)

// ECV is the constant V defined in the EC spec.
// TODO: the value of V needs motivation at the protocol design level.
const ECV uint64 = 10

// ECPrM is the power ratio magnitude defined in the EC spec.
// TODO: the value of this constant needs motivation at the protocol level.
const ECPrM uint64 = 100

// Expected implements expected consensus.
type Expected struct {
	// PwrTableView provides miner and total power for the EC chain weight
	// computation.
	PwrTableView PowerTableView

	// cstore is used for loading state trees during message running.
	cstore *hamt.CborIpldStore
	// bstore contains data referenced by actors within the state
	// during message running.  Additionally bstore is used for
	// accessing the power table.
	bstore blockstore.Blockstore

	genesisCid *cid.Cid
}

// Ensure Expected satisfies the Protocol interface at compile time.
var _ Protocol = (*Expected)(nil)

// NewExpected is the constructor for the Expected consenus.Protocol module.
func NewExpected(cs *hamt.CborIpldStore, bs blockstore.Blockstore, pt PowerTableView, gCid *cid.Cid) Protocol {
	return &Expected{
		cstore:       cs,
		bstore:       bs,
		PwrTableView: pt,
		genesisCid:   gCid,
	}
}

// NewValidTipSet creates a new tipset from the input blocks that is guaranteed
// to be valid. It operates by validating each block and further checking that
// this tipset contains only blocks with the same heights, parent weights,
// and parent sets.
func (c *Expected) NewValidTipSet(ctx context.Context, blks []*types.Block) (TipSet, error) {
	for _, blk := range blks {
		if err := c.validateBlockStructure(ctx, blk); err != nil {
			return nil, err
		}
	}
	return NewTipSet(blks...)
}

// ValidateBlockStructure verifies that this block, on its own, is structurally and
// cryptographically valid. This means checking that all of its fields are
// properly filled out and its signatures are correct. Checking the validity of
// state changes must be done separately and only once the state of the
// previous block has been validated. TODO: not yet signature checking
func (c *Expected) validateBlockStructure(ctx context.Context, b *types.Block) error {
	// TODO: validate signature on block
	log.LogKV(ctx, "ValidateBlockStructure", b.Cid().String())
	if b.StateRoot == nil {
		return fmt.Errorf("block has nil StateRoot")
	}

	return nil
}

// weight returns the EC weight of the given tipset in a format (big Rational)
// suitable for internal use in the consensus package.
// TODO: this implementation needs to handle precision of long chains correctly,
// see issue #655.
func (c *Expected) weight(ctx context.Context, ts TipSet, pSt state.Tree) (*big.Rat, error) {
	log.LogKV(ctx, "Weight", ts.String())
	if len(ts) == 1 && ts.ToSlice()[0].Cid().Equals(c.genesisCid) {
		return big.NewRat(int64(0), int64(1)), nil
	}
	// Compute parent weight.
	wNum, wDenom, err := ts.ParentWeight()
	if err != nil {
		return nil, err
	}
	if wDenom == uint64(0) {
		return nil, errors.New("storage market with 0 bytes stored not handled")
	}
	w := big.NewRat(int64(wNum), int64(wDenom))
	fmt.Printf("parent w: %v\n", w)

	// Each block in the tipset adds ECV + ECPrm * miner_power to parent weight.
	totalBytes, err := c.PwrTableView.Total(ctx, pSt, c.bstore)
	if err != nil {
		return nil, err
	}
	ratECV := big.NewRat(int64(ECV), int64(1))
	fmt.Printf("Adding in the constant 10: %v\n", w)
	for _, blk := range ts {

		minerBytes, err := c.PwrTableView.Miner(ctx, pSt, c.bstore, blk.Miner)
		if err != nil {
			return nil, err
		}
		wNumBlk := int64(ECPrM * minerBytes)
		wBlk := big.NewRat(wNumBlk, int64(totalBytes)) // power added for each block
		wBlk.Add(wBlk, ratECV)                         // constant added for each block
		w.Add(w, wBlk)
		fmt.Printf("Adding power of one block: %v\n", w)
	}
	return w, nil
}

// Weight returns the EC weight of this TipSet
// TODO: this implementation needs to handle precision of long chains correctly,
// see issue #655.
func (c *Expected) Weight(ctx context.Context, ts TipSet, pSt state.Tree) (uint64, uint64, error) {
	w, err := c.weight(ctx, ts, pSt)
	if err != nil {
		return uint64(0), uint64(0), err
	}
	wNum := w.Num()
	if !wNum.IsUint64() {
		return uint64(0), uint64(0), errors.New("weight numerator cannot be repr by uint64")
	}
	wDenom := w.Denom()
	if !wDenom.IsUint64() {
		return uint64(0), uint64(0), errors.New("weight denominator cannot be repr by uint64")
	}
	return wNum.Uint64(), wDenom.Uint64(), nil
}

// IsHeavier returns an integer comparing two tipsets by weight.  The
// result will be -1 if W(a) < W(b), and 1 if W(a) > W(b).  In the rare
// case where two tipsets have the same weight, ties are broken by taking
// the tipset with the smallest ticket.  In the event that tickets
// are the same, IsHeavier will break ties by comparing the concatenation
// of block cids in the tipset.
// TODO BLOCK CID CONCAT TIE BREAKER IS NOT IN THE SPEC AND SHOULD BE
// EVALUATED BEFORE GETTING TO PRODUCTION.
func (c *Expected) IsHeavier(ctx context.Context, a, b TipSet, aSt, bSt state.Tree) (int, error) {
	aW, err := c.weight(ctx, a, aSt)
	if err != nil {
		return 0, err
	}
	bW, err := c.weight(ctx, b, bSt)
	if err != nil {
		return 0, err
	}

	// Without ties pass along the comparison.
	cmp := aW.Cmp(bW)
	if cmp != 0 {
		return cmp, nil
	}

	// To break ties compare the min tickets.
	aTicket, err := a.MinTicket()
	if err != nil {
		return 0, err
	}
	bTicket, err := b.MinTicket()
	if err != nil {
		return 0, err
	}

	cmp = bytes.Compare(bTicket, aTicket)
	if cmp != 0 {
		return cmp, nil
	}

	// Tie break on cid ids.
	cmp = strings.Compare(a.String(), b.String())
	if cmp == 0 {
		// Caller is mistakenly calling on two identical tipsets.
		return 0, ErrUnorderedTipSets
	}
	return cmp, nil
}

// RunStateTransition is the chain transition function that goes from a
// starting state and a tipset to a new state.  It errors if the tipset was not
// mined according to the EC rules, or if running the messages in the tipset
// results in an error.
func (c *Expected) RunStateTransition(ctx context.Context, ts TipSet, pSt state.Tree) (state.Tree, error) {
	err := c.validateMining(ctx, pSt, ts)
	if err != nil {
		return nil, err
	}
	vms := vm.NewStorageMap(c.bstore)
	st, err := c.runMessages(ctx, pSt, vms, ts)
	if err != nil {
		return nil, err
	}
	err = vms.Flush()
	if err != nil {
		return nil, err
	}
	return st, nil
}

// validateMining throws an error if any tipset's block was mined by an invalid
// miner address.
func (c *Expected) validateMining(ctx context.Context, st state.Tree, ts TipSet) error {
	for _, blk := range ts {
		if !c.PwrTableView.HasPower(ctx, st, c.bstore, blk.Miner) {
			return errors.New("invalid miner address without network power")
		}
		// TODO: check that ticket is a winner
	}
	return nil
}

// runMessages applies the messages of all blocks within the input
// tipset to the input base state.  Messages are applied block by
// block with blocks sorted by their ticket bytes.  The output state must be
// flushed after calling to guarantee that the state transitions propagate.
//
// An error is returned if individual blocks contain messages that do not
// lead to successful state transitions.  An error is also returned if the node
// faults while running aggregate state computation.
func (c *Expected) runMessages(ctx context.Context, st state.Tree, vms vm.StorageMap, ts TipSet) (state.Tree, error) {
	var cpySt state.Tree
	for _, blk := range ts {
		cpyCid, err := st.Flush(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}
		// state copied so changes don't propagate between block validations
		cpySt, err = state.LoadStateTree(ctx, c.cstore, cpyCid, builtin.Actors)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}

		receipts, err := ProcessBlock(ctx, blk, cpySt, vms)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}
		// TODO: check that receipts actually match
		if len(receipts) != len(blk.MessageReceipts) {
			return nil, fmt.Errorf("found invalid message receipts: %v %v", receipts, blk.MessageReceipts)
		}

		outCid, err := cpySt.Flush(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}
		if !outCid.Equals(blk.StateRoot) {
			return nil, ErrStateRootMismatch
		}
	}
	if len(ts) == 1 { // block validation state == aggregate parent state
		return cpySt, nil
	}
	// multiblock tipsets require reapplying messages to get aggregate state
	// NOTE: It is possible to optimize further by applying block validation
	// in sorted order to reuse first block transitions as the starting state
	// for the tipSetProcessor.
	_, err := ProcessTipSet(ctx, ts, st, vms)
	if err != nil {
		return nil, errors.Wrap(err, "error validating tipset")
	}
	return st, nil
}
