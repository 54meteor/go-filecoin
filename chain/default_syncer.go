package chain

import (
	"context"
	"sync"
	"time"

	"gx/ipfs/QmSkuaNgyGmV8c1L3cZNWcUxRJV6J3nsD96JVQPcWcwtyW/go-hamt-ipld"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmYVNvtQkeZ6AKSwDrjQTs432QtL6umrrK41EBq3cu7iSP/go-cid"

	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
)

// The maximum number of tipsets not yet in the store that the syncer will
// traverse during chain collection before ignoring the chain.
const maxNewChainLen = 500 // TODO set this parameter in an informed way
// The amount of time the syncer will wait while fetching the blocks of a
// tipset over the network.
var blkWaitTime = time.Second * 5 // TODO set this parameter in an informed way too
var (
	// ErrChainHasBadTipSet is returned when the syncer traverses a chain with a cached bad tipset.
	ErrChainHasBadTipSet = errors.New("input chain contains a cached bad tipset")
	// ErrNewChainTooLong is returned when processing a fork that split off from the main chain too many blocks ago.
	ErrNewChainTooLong = errors.New("input chain forked from best chain too far in the past")
	// ErrUnexpectedStoreState indicates that the syncer's chain store is violating expected invariants.
	ErrUnexpectedStoreState = errors.New("the chain store is in an unexpected state")
)

// DefaultSyncer updates its chain.Store according to the methods of its
// consensus.Protocol.  It uses a bad tipset cache and a limit on new
// blocks to traverse during chain collection.  The DefaultSyncer can query the
// network for blocks.  The DefaultSyncer maintains the following invariant on
// its store: all tipsets that pass the syncer's validity checks are added to the
// chain store, and their state is added to cstOffline.
//
// Currently the DefaultSyncer is more tightly coupled to details of Exepcted
// Consenus than is  desirable.  This dependence can be seen in the widen
// function, the fact that widen is called on only one tipset in the incoming
// chain, and assumptions regarding the existence of grandparent state in the
// store.
type DefaultSyncer struct {
	// The HandleNewBlocks function is NOT threadsafe and the syncer must
	// hold this lock for each such call.  In particular at least two
	// sections of code have races without this lock
	// 1. syncOne assumes that chainStore.Head() does not change when
	// comparing tipset weights and updating the store
	// 2. HandleNewBlocks assumes that calls to widen and then syncOne
	// are not run concurrently with other calls to widen to ensure
	// that the syncer always finds the heaviest existing tipset.
	mu sync.Mutex
	// cstOnline is the online storage for fetching blocks.  It should be connected to the network with bitswap.
	cstOnline *hamt.CborIpldStore
	// cstOffline is the node's shared offline storage.
	cstOffline *hamt.CborIpldStore
	// badTipSetCache is used to filter out collections of invalid blocks.
	badTipSets *badTipSetCache
	// c is the consensus component that decides validation rules
	consensus consensus.Protocol
	// s is the Store storing the blockchain
	chainStore Store
}

var _ Syncer = (*DefaultSyncer)(nil)

// NewDefaultSyncer constructs a DefaultSyncer ready for use.
func NewDefaultSyncer(online, offline *hamt.CborIpldStore, c consensus.Protocol, s Store) Syncer {
	return &DefaultSyncer{
		cstOnline:  online,
		cstOffline: offline,
		badTipSets: &badTipSetCache{
			bad: make(map[string]struct{}),
		},
		consensus:  c,
		chainStore: s,
	}
}

// getBlksMaybeFromNet resolves cids of blocks.  It gets blocks from local
// storage if they are available there, and otherwise resolves blocks over
// the network.  This function will timeout if blocks are unavailable.
// This method is all or nothing, it will error if any of the blocks cannot be
// resolved.
// TODO the timeout factor blkWaitTime and maybe the whole timeout mechanism
// could use some actual thought, this was just a simple first pass.
func (syncer *DefaultSyncer) getBlksMaybeFromNet(ctx context.Context, blkCids []*cid.Cid) ([]*types.Block, error) {
	var blks []*types.Block
	ctx, cancel := context.WithTimeout(ctx, blkWaitTime)
	defer cancel()
	for _, blkCid := range blkCids {
		// try the chain store
		blk, err := syncer.chainStore.GetBlock(ctx, blkCid)
		if err == nil {
			blks = append(blks, blk)
			continue
		}
		// try the node's local offline storage
		err = syncer.cstOffline.Get(ctx, blkCid, &blk)
		if err == nil {
			blks = append(blks, blk)
			continue
		}
		// try the network
		if err = syncer.cstOnline.Get(ctx, blkCid, &blk); err != nil {
			return nil, err
		}
		blks = append(blks, blk)
	}
	return blks, nil
}

// collectChain resolves the cids of the head tipset and its ancestors to blocks
// until it resolves blocks contained in the Store. collectChain may resolve cids
// from the Store, the node's local offline cborstore, or the syncer's online
// cbor store that is networked under the hood. collectChain errors if any
// set of cids in the chain resolves to blocks that do not form a tipset, if
// the chain is too long, or if any tipset has already been recorded as the
// head of an invalid chain.
//
// collectChain is the only function call where the syncer interacts with the
// network to resolve blocks.
func (syncer *DefaultSyncer) collectChain(ctx context.Context, blkCids []*cid.Cid) ([]consensus.TipSet, consensus.TipSet, error) {
	var chain []consensus.TipSet
	for i := 0; i < maxNewChainLen; i++ {
		var blks []*types.Block
		// check the cache for bad tipsets before doing anything
		tsKey := types.NewSortedCidSet(blkCids...).String()
		if syncer.badTipSets.Has(tsKey) {
			return nil, nil, ErrChainHasBadTipSet
		}

		blks, err := syncer.getBlksMaybeFromNet(ctx, blkCids)
		if err != nil {
			return nil, nil, err
		}

		ts, err := syncer.consensus.NewValidTipSet(ctx, blks)
		if err != nil {
			syncer.badTipSets.Add(tsKey)
			syncer.badTipSets.AddChain(chain)
			return nil, nil, err
		}

		// Finish traversal if all these blocks are in the store.
		if syncer.chainStore.HasAllBlocks(ctx, blkCids) {
			return chain, ts, nil
		}

		// Update values to traverse next tipset
		chain = append([]consensus.TipSet{ts}, chain...)
		parentCidSet, err := ts.Parents()
		if err != nil {
			return nil, nil, err
		}
		blkCids = parentCidSet.ToSlice()
	}
	return nil, nil, ErrNewChainTooLong
}

// loadTipSetState retrieves the tipset state root from the chain store
// loads the state tree, and returns the state tree and root.
func (syncer *DefaultSyncer) loadTipSetState(ctx context.Context, tsKey string) (state.Tree, *cid.Cid, error) {
	tsas, err := syncer.chainStore.GetTipSetAndState(ctx, tsKey)
	if err != nil {
		return nil, nil, err
	}
	st, err := state.LoadStateTree(ctx, syncer.cstOffline, tsas.TipSetStateRoot, builtin.Actors)
	if err != nil {
		return nil, nil, err
	}
	return st, tsas.TipSetStateRoot, nil
}

// tipSetState returns the state and state cid resulting from applying the
// input tipset to the chain.  Precondition: the parent tipset must be in the
// store.
func (syncer *DefaultSyncer) tipSetState(ctx context.Context, ts consensus.TipSet) (state.Tree, *cid.Cid, error) {
	tsKey := ts.String()
	if syncer.chainStore.HasTipSetAndState(ctx, tsKey) {
		//		fmt.Printf("syncer: tipsetstate is loading state\n")
		return syncer.loadTipSetState(ctx, tsKey)
	}

	pCidSet, err := ts.Parents()
	if err != nil {
		return nil, nil, err
	}
	pKey := pCidSet.String()
	// If we have been adding aggregate tipset states into the
	// store every time we compute them it is an invariant that
	// the parents of all base tipsets must have a state value in
	// the store.  This invariant is maintained because the syncer
	// only calls tipSetState on a tipset whose parent tipset has already
	// been synced to the store.
	// TODO -- we will need to either change this invariant and move
	// to generating states on demand, or start persisting
	// the tipindex to the cborstore when we start limiting cache sizes.
	if !syncer.chainStore.HasTipSetAndState(ctx, pKey) {
		return nil, nil, errors.Wrap(ErrUnexpectedStoreState, "parent tipset must be in store")
	}
	st, _, err := syncer.loadTipSetState(ctx, pKey)
	if err != nil {
		return nil, nil, err
	}
	st, err = syncer.consensus.RunStateTransition(ctx, ts, st)
	if err != nil {
		return nil, nil, errors.Wrap(err, "unexpected error: tipset in store should always transition validly")
	}

	root, err := st.Flush(ctx)
	if err != nil {
		return nil, nil, err
	}
	return st, root, nil
}

// syncOne syncs a single tipset with the chain store. syncOne calculates the
// parent state of the tipset and calls into consensus to run a state transition
// in order to validate the tipset.  In the case the input tipset is valid,
// syncOne calls into consensus to check its weight, and then updates the head
// of the store if this tipset is the heaviest.
func (syncer *DefaultSyncer) syncOne(ctx context.Context, parent, next consensus.TipSet) error {
	//	fmt.Printf("syncer: within syncOne\n")
	// Lookup parent state and add to store if not yet there.  It is
	// guaranteed by the syncer that the grandparent's state is in the
	// store.
	st, pRoot, err := syncer.tipSetState(ctx, parent)
	if err != nil {
		return err
	}
	if !syncer.chainStore.HasTipSetAndState(ctx, parent.String()) {
		syncer.chainStore.PutTipSetAndState(ctx, &TipSetAndState{
			TipSet:          parent,
			TipSetStateRoot: pRoot,
		})
	}

	// TODO if using LBP for challenge sampling > 1 we should
	// use consensus.LookBackParam and extend store interface
	// to include looking up tipsets by height.

	// Run a state transition to validate the tipset and compute
	// a new state to add to the store.
	st, err = syncer.consensus.RunStateTransition(ctx, next, st)
	if err != nil {
		return err
	}
	root, err := st.Flush(ctx)
	if err != nil {
		return err
	}
	syncer.chainStore.PutTipSetAndState(ctx, &TipSetAndState{
		TipSet:          next,
		TipSetStateRoot: root,
	})

	// TipSet is validated and added to store, now check if it is the heaviest.
	//	fmt.Printf("syncer: new ts is validated and added to the store!! time to check weight\n")
	nextParent, err := next.Parents()
	if err != nil {
		return err
	}
	nextParentSt, _, err := syncer.loadTipSetState(ctx, nextParent.String())
	if err != nil {
		return err
	}
	headParent, err := syncer.chainStore.Head().Parents()
	if err != nil {
		return err
	}
	var headParentSt state.Tree
	if headParent.Len() != 0 { // head is not genesis
		headParentSt, _, err = syncer.loadTipSetState(ctx, headParent.String())
		if err != nil {
			return err
		}
	}

	cmp, err := syncer.consensus.IsHeavier(ctx, next, syncer.chainStore.Head(), nextParentSt, headParentSt)
	if err != nil {
		return err
	}
	if cmp > 0 {
		if err = syncer.chainStore.SetHead(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

// widen computes a tipset implied by the input tipset and the store that
// could potentially be the heaviest tipset. In the context of EC, widen
// returns the union of the input tipset and the biggest tipset with the same
// parents from the store.
// TODO: this leaks EC abstractions into the syncer, we should think about this.
func (syncer *DefaultSyncer) widen(ctx context.Context, ts consensus.TipSet) (consensus.TipSet, error) {
	//	fmt.Printf("syncer: within widen\n")
	// Lookup tipsets with the same parents from the store.
	parentSet, err := ts.Parents()
	if err != nil {
		return nil, err
	}
	if !syncer.chainStore.HasTipSetAndStatesWithParents(ctx, parentSet.String()) {
		return nil, nil
	}
	candidates, err := syncer.chainStore.GetTipSetAndStatesByParents(ctx, parentSet.String())
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Only take the tipset with the most blocks (this is EC specific logic)
	max := candidates[0]
	for _, candidate := range candidates[0:] {
		if len(candidate.TipSet) > len(max.TipSet) {
			max = candidate
		}
	}

	// Add blocks of the biggest tipset in the store to a copy of ts
	wts := ts.Clone()
	for _, blk := range max.TipSet {
		if err = wts.AddBlock(blk); err != nil {
			return nil, err
		}
	}

	// check that the tipset from the store actually added new blocks
	if wts.String() == ts.String() {
		return nil, nil
	}

	return wts, nil
}

// HandleNewBlocks updates the Syncer's Store according to the input, the
// Syncer's Consensus rules, and the Syncer's resource management logic.
// HandleNewBlocks is the method a filecoin node uses to safely extend its
// view of the blockchain, as contained in the Store, with the new information
// contained in the chain with "blkCids" at its head.  HandleNewBlocks does this
// "safely", because 1. it attempts to defend against resource wasting / DOS
// attacks, and 2. it only includes new blocks into the Store if these blocks
// satisfy the protocol validation rules.
func (syncer *DefaultSyncer) HandleNewBlocks(ctx context.Context, blkCids []*cid.Cid) error {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	// If the store already has all these blocks the syncer is finished.
	if syncer.chainStore.HasAllBlocks(ctx, blkCids) {
		return nil
	}

	// Walk the chain given by the input blocks back to a known tipset in
	// the store. This is the only code that may go to the network to
	// resolve cids to blocks.
	chain, parent, err := syncer.collectChain(ctx, blkCids)
	if err != nil {
		return err
	}
	//fmt.Printf("syncer: chain collected\n")

	// Try adding the tipsets of the chain to the store, checking for new
	// heaviest tipsets.
	for i, ts := range chain {
		// TODO: this "i==0" leaks EC specifics into syncer abstraction
		// for the sake of efficiency, consider plugging up this leak.
		if i == 0 {
			//			fmt.Printf("attempting to widen \n")
			wts, err := syncer.widen(ctx, ts)
			if err != nil {
				return err
			}
			if wts != nil {
				//				fmt.Printf("syncer: found something to widen\n")
				err = syncer.syncOne(ctx, parent, wts)
				if err != nil {
					return err
				}
			}
		}
		//		fmt.Printf("syncer: regular degular syncone\n")
		if err = syncer.syncOne(ctx, parent, ts); err != nil {
			return err
		}
		parent = ts
	}
	return nil
}
