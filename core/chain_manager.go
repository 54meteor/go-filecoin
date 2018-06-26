package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	logging "gx/ipfs/QmQCqiR5F3NeJRr7LuWq8i8FgtT65ypZw5v9V6Es6nwFBD/go-log"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmXRKBQA4wXP7xWbFiZsR1GP4HV6wMDQ1aWFxZZ4uBcPX9/go-datastore"
	"gx/ipfs/QmZoWKhxUmZ2seW4BzX6fJkNR8hh9PsGModr7q171yq2SS/go-libp2p-peer"
	"gx/ipfs/QmcYBp5EDnJKfVN63F71rDTksvEf1cfijwCTWtw6bPG58T/go-hamt-ipld"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	"gx/ipfs/QmdbxjQWogRCHRaxhhGnYdT1oQJzL9GdqSKzCdqWr85AP2/pubsub"

	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
)

var log = logging.Logger("chain")

var (
	// ErrStateRootMismatch is returned when the computed state root doesn't match the expected result.
	ErrStateRootMismatch = errors.New("blocks state root does not match computed result")
	// ErrInvalidBase is returned when the chain doesn't connect back to a known good block.
	ErrInvalidBase = errors.New("block does not connect to a known good chain")
	// ErrDifferentGenesis is returned when processing a chain with a different genesis block.
	ErrDifferentGenesis = fmt.Errorf("chain had different genesis")
	// ErrBadTipSet is returned when processing a tipset containing blocks of different heights or different parent sets
	ErrBadTipSet = errors.New("tipset contains blocks of different heights or different parent sets")
)

var heaviestTipSetKey = datastore.NewKey("/chain/heaviestTipSet")

// HeaviestTipSetTopic is the topic used to publish new best tipsets.
const HeaviestTipSetTopic = "heaviest-tipset"

// BlockProcessResult signifies the outcome of processing a given block.
type BlockProcessResult int

const (
	// Unknown implies there was an error that made it impossible to process the block.
	Unknown = BlockProcessResult(iota)

	// ChainAccepted implies the chain was valid, and is now our current best
	// chain.
	ChainAccepted

	// ChainValid implies the chain was valid, but not better than our current
	// best chain.
	ChainValid

	// InvalidBase implies the chain does not connect back to any previously
	// known good block.
	InvalidBase
)

func (bpr BlockProcessResult) String() string {
	switch bpr {
	case ChainAccepted:
		return "accepted"
	case ChainValid:
		return "valid"
	case Unknown:
		return "unknown"
	case InvalidBase:
		return "invalid"
	}
	return "" // never hit
}

// ChainManager manages the current state of the chain and handles validating
// and applying updates.
// Safe for concurrent access
type ChainManager struct {
	// heaviestTipSet is the set of blocks at the head of the best known chain
	heaviestTipSet struct {
		sync.Mutex
		ts TipSet
	}

	blockProcessor  Processor
	tipSetProcessor TipSetProcessor

	// genesisCid holds the cid of the chains genesis block for later access
	genesisCid *cid.Cid

	// Protects knownGoodBlocks and tipsIndex.
	mu sync.Mutex

	// knownGoodBlocks is a cache of 'good blocks'. It is a cache to prevent us
	// from having to rescan parts of the blockchain when determining the
	// validity of a given chain.
	// In the future we will need a more sophisticated mechanism here.
	// TODO: this should probably be an LRU, needs more consideration.
	// For example, the genesis block should always be considered a "good" block.
	knownGoodBlocks *cid.Set

	// Tracks tipsets by height/parentset for use by expected consensus.
	tips tipIndex

	// Tracks state by tipset identifier
	stateCache map[string]*cid.Cid

	cstore *hamt.CborIpldStore

	ds datastore.Datastore

	// HeaviestTipSetPubSub is a pubsub channel that publishes all best tipsets.
	// We operate under the assumption that tipsets published to this channel
	// will always be queued and delivered to subscribers in the order discovered.
	// Successive published tipsets may be supersets of previously published tipsets.
	HeaviestTipSetPubSub *pubsub.PubSub

	FetchBlock        func(context.Context, *cid.Cid) (*types.Block, error)
	GetBestBlock      func() *types.Block
	GetHeaviestTipSet func() TipSet
}

// NewChainManager creates a new filecoin chain manager.
func NewChainManager(ds datastore.Datastore, cs *hamt.CborIpldStore) *ChainManager {
	cm := &ChainManager{
		cstore:               cs,
		ds:                   ds,
		blockProcessor:       ProcessBlock,
		tipSetProcessor:      ProcessTipSet,
		HeaviestTipSetPubSub: pubsub.New(128),
		knownGoodBlocks:      cid.NewSet(),
		tips:                 tipIndex{},
		stateCache:           make(map[string]*cid.Cid),
	}
	cm.FetchBlock = cm.fetchBlock
	cm.GetBestBlock = cm.getBestBlock
	cm.GetHeaviestTipSet = cm.getHeaviestTipSet

	return cm
}

// Genesis creates a new genesis block and sets it as the the best known block.
func (s *ChainManager) Genesis(ctx context.Context, gen GenesisInitFunc) (err error) {
	ctx = log.Start(ctx, "ChainManager.Genesis")
	defer func() {
		log.FinishWithErr(ctx, err)
	}()
	genesis, err := gen(s.cstore)
	if err != nil {
		return err
	}

	s.genesisCid = genesis.Cid()

	s.heaviestTipSet.Lock()
	defer s.heaviestTipSet.Unlock()
	s.addBlock(genesis, s.genesisCid)
	genTipSet, err := NewTipSet(genesis)
	if err != nil {
		return err
	}
	return s.setHeaviestTipSet(ctx, genTipSet)
}

// setHeaviestTipSet sets the best tipset.  CALLER MUST HOLD THE heaviestTipSet LOCK.
func (s *ChainManager) setHeaviestTipSet(ctx context.Context, ts TipSet) error {
	log.LogKV(ctx, "setHeaviestTipSet", ts.String())
	if err := putCidSet(ctx, s.ds, heaviestTipSetKey, ts.ToSortedCidSet()); err != nil {
		return errors.Wrap(err, "failed to write TipSet cids to datastore")
	}
	s.HeaviestTipSetPubSub.Pub(ts, HeaviestTipSetTopic)
	// The heaviest tipset should not pick up changes from adding new blocks to the index.
	// It only changes explicitly when set through this function.
	s.heaviestTipSet.ts = ts.Clone()

	return nil
}

func putCidSet(ctx context.Context, ds datastore.Datastore, k datastore.Key, cids types.SortedCidSet) error {
	log.LogKV(ctx, "PutCidSet", cids.String())
	val, err := json.Marshal(cids)
	if err != nil {
		return err
	}

	return ds.Put(k, val)
}

// Load reads the cids of the best tipset from disk and reparses the chain backwards from there.
func (s *ChainManager) Load() error {
	tipCids, err := s.readHeaviestTipSetCids()
	if err != nil {
		return err
	}
	ts := TipSet{}
	// traverse starting from one TipSet to begin loading the chain
	for it := (*tipCids).Iter(); !it.Complete(); it.Next() {
		// TODO: 'read only from local disk' method here.
		// actually, i think that the chainmanager should only ever fetch from
		// the local disk unless we're syncing. Its something that needs more
		// thought at least.
		blk, err := s.FetchBlock(context.TODO(), it.Value())
		if err != nil {
			return errors.Wrap(err, "failed to load block in head TipSet")
		}
		err = ts.AddBlock(blk)
		if err != nil {
			return errors.Wrap(err, "failed to add validated block to TipSet")
		}
	}

	var genesii []*types.Block
	err = s.walkChain(ts.ToSlice(), func(tips []*types.Block) (cont bool, err error) {
		for _, t := range tips {
			id := t.Cid()
			s.addBlock(t, id)
		}
		genesii = tips
		return true, nil
	})
	if err != nil {
		return err
	}
	switch len(genesii) {
	case 1:
		// TODO: probably want to load the expected genesis block and assert it here?
		s.genesisCid = genesii[0].Cid()
		s.heaviestTipSet.ts = ts
	case 0:
		panic("unreached")
	default:
		panic("invalid chain - more than one genesis block found")
	}

	return nil
}

func (s *ChainManager) readHeaviestTipSetCids() (*types.SortedCidSet, error) {
	bbi, err := s.ds.Get(heaviestTipSetKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read heaviestTipSetKey")
	}
	bb, ok := bbi.([]byte)
	if !ok {
		return nil, fmt.Errorf("stored heaviestTipSetCids not []byte")
	}

	var cids types.SortedCidSet
	err = json.Unmarshal(bb, &cids)
	if err != nil {
		return nil, errors.Wrap(err, "casting stored heaviestTipSetCids failed")
	}

	return &cids, nil
}

// GetGenesisCid returns the cid of the current genesis block.
func (s *ChainManager) GetGenesisCid() *cid.Cid {
	return s.genesisCid
}

// BestBlockGetter is the signature for a function used to get the current best block.
// TODO: this is only being used by callers that haven't been properly updated to use
// HeaviestTipSetGetters.  These callers should be updated and this type removed
type BestBlockGetter func() *types.Block

// HeaviestTipSetGetter is the signature for a functin used to get the current best tipset.
type HeaviestTipSetGetter func() TipSet

// getBestBlock returns a random member of the tipset at the head of our
// currently selected 'best' chain.  TODO: this is only being used by callers that
// haven't been updated to use getHeaviestTipSet.  Update and remove this
func (s *ChainManager) getBestBlock() *types.Block {
	s.heaviestTipSet.Lock()
	defer s.heaviestTipSet.Unlock()
	return s.heaviestTipSet.ts.ToSlice()[0]
}

// GetHeaviestTipSet returns the tipset at the head of our current 'best' chain.
func (s *ChainManager) getHeaviestTipSet() TipSet {
	s.heaviestTipSet.Lock()
	defer s.heaviestTipSet.Unlock()
	return s.heaviestTipSet.ts
}

// maybeAcceptBlock attempts to accept blk if its score is greater than the current best block,
// otherwise returning ChainValid.
func (s *ChainManager) maybeAcceptBlock(ctx context.Context, blk *types.Block) (BlockProcessResult, error) {
	// We have to hold the lock at this level to avoid TOCTOU problems
	// with the new heaviest tipset.
	log.LogKV(ctx, "maybeAcceptBlock", blk.Cid().String())
	s.heaviestTipSet.Lock()
	defer s.heaviestTipSet.Unlock()
	ts, err := s.GetTipSetByBlock(blk)
	if err != nil {
		return Unknown, err
	}
	// Calculate weights of TipSets for comparison.
	pSt, err := s.LoadParentStateTree(ctx, s.heaviestTipSet.ts)
	if err != nil {
		return Unknown, err
	}
	heaviestWeight, err := s.heaviestTipSet.ts.Weight(pSt)
	if err != nil {
		return Unknown, err
	}
	pSt, err = s.LoadParentStateTree(ctx, ts)
	if err != nil {
		return Unknown, err
	}
	newWeight, err := ts.Weight(pSt)
	if err != nil {
		return Unknown, err
	}
	if newWeight < heaviestWeight ||
		(newWeight == heaviestWeight &&
			// break ties by choosing tipset with smaller ticket
			bytes.Compare(ts.MinTicket(), s.heaviestTipSet.ts.MinTicket()) >= 0) {
		return ChainValid, nil
	}

	// set the given tipset as our current heaviest tipset
	if err := s.setHeaviestTipSet(ctx, ts); err != nil {
		return Unknown, err
	}
	log.Infof("new heaviest tipset, [s=%f, hs=%s]", newWeight, ts.String())
	log.LogKV(ctx, "maybeAcceptBlock", ts.String())
	return ChainAccepted, nil
}

// NewBlockProcessor is the signature for a function which processes a new block.
type NewBlockProcessor func(context.Context, *types.Block) (BlockProcessResult, error)

// ProcessNewBlock sends a new block to the chain manager. If the block is in a
// tipset heavier than our current heaviest, this tipset is accepted as our
// heaviest tipset. Otherwise an error is returned explaining why it was not accepted.
func (s *ChainManager) ProcessNewBlock(ctx context.Context, blk *types.Block) (bpr BlockProcessResult, err error) {
	ctx = log.Start(ctx, "ChainManager.ProcessNewBlock")
	defer func() {
		log.SetTag(ctx, "result", bpr.String())
		log.FinishWithErr(ctx, err)
	}()
	log.Infof("processing block [s=%d, cid=%s]", blk.Score(), blk.Cid())

	switch err := s.validateBlock(ctx, blk); err {
	default:
		return Unknown, errors.Wrap(err, "validate block failed")
	case ErrInvalidBase:
		return InvalidBase, ErrInvalidBase
	case nil:
		return s.maybeAcceptBlock(ctx, blk)
	}
}

// fetchBlock gets the requested block, either from disk or from the network.
func (s *ChainManager) fetchBlock(ctx context.Context, c *cid.Cid) (*types.Block, error) {
	log.Infof("fetching block, [%s]", c.String())

	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	var blk types.Block
	if err := s.cstore.Get(ctx, c, &blk); err != nil {
		return nil, err
	}

	return &blk, nil
}

// validateTipSetStructure verifies that the input blocks form a valid tipset.
// validating each block structurally and making sure that this tipset contains
// only blocks with the same height and same parent set
func (s *ChainManager) validateTipSetStructure(ctx context.Context, blks []*types.Block) error {
	var h uint64
	var p types.SortedCidSet
	if len(blks) > 0 {
		h = blks[0].Height
		p = blks[0].Parents
	}
	for _, blk := range blks {
		if err := s.validateBlockStructure(ctx, blk); err != nil {
			return err
		}
		if blk.Height != h {
			return ErrBadTipSet
		}
		if !p.Equals(blk.Parents) {
			return ErrBadTipSet
		}
	}

	return nil
}

// validateBlockStructure verifies that this block, on its own, is structurally and
// cryptographically valid. This means checking that all of its fields are
// properly filled out and its signatures are correct. Checking the validity of
// state changes must be done separately and only once the state of the
// previous block has been validated. TODO: not yet signature checking
func (s *ChainManager) validateBlockStructure(ctx context.Context, b *types.Block) error {
	// TODO: validate signatures on messages
	log.LogKV(ctx, "validateBlockStructure", b.Cid().String())
	if b.StateRoot == nil {
		return fmt.Errorf("block has nil StateRoot")
	}

	return nil
}

// TODO: this method really needs to be thought through carefully. Probably one
// of the most complicated bits of the system
// TODO: We don't currently validate that
//   a) there is a mining reward; and b) the reward is the first message in the block.
//  We need to do so since this is a part of the consensus rules.
func (s *ChainManager) validateBlock(ctx context.Context, b *types.Block) error {
	log.LogKV(ctx, "validateBlock", b.Cid().String())
	if err := s.validateBlockStructure(ctx, b); err != nil {
		return errors.Wrap(err, "check block valid failed")
	}

	if _, err := s.cstore.Put(ctx, b); err != nil {
		return errors.Wrap(err, "failed to store block")
	}

	baseTipSet, chain, err := s.findKnownAncestor(ctx, b)
	if err != nil {
		return err
	}

	st, err := s.LoadStateTreeTS(ctx, baseTipSet)
	if err != nil {
		return err
	}

	for i := len(chain) - 1; i >= 0; i-- {
		curTipSet := chain[i]
		var cpySt state.Tree
		// validate each block within tipset
		for _, blk := range curTipSet {
			// state copied so changes don't propagate between block validations
			cpyCid, err := st.Flush(ctx)
			if err != nil {
				return err
			}
			cpySt, err = state.LoadStateTree(ctx, s.cstore, cpyCid, builtin.Actors)
			if err != nil {
				return err
			}

			receipts, err := s.blockProcessor(ctx, blk, cpySt)
			if err != nil {
				return err
			}

			// TODO: check that the receipts actually match
			if len(receipts) != len(blk.MessageReceipts) {
				return fmt.Errorf("found invalid message receipts: %v %v", receipts, blk.MessageReceipts)
			}
			s.addBlock(blk, blk.Cid())
		}
		if len(curTipSet) == 1 { // block validation state == aggregate parent state
			st = cpySt
			continue
		}
		// Multiblock tipset, reapply messages to get aggregate parent state
		_, err = s.tipSetProcessor(ctx, curTipSet, st)
		if err != nil {
			return err
		}
	}

	outCid, err := st.Flush(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to flush tree after applying state transitions")
	}
	if !outCid.Equals(b.StateRoot) {
		return ErrStateRootMismatch
	}

	return nil
}

// findKnownAncestor walks backwards from the given block until it finds a tipset
// that we know to be good. It then returns that known tipset, and the tipsets
// that form the chain back to it.
func (s *ChainManager) findKnownAncestor(ctx context.Context, tip *types.Block) (TipSet, []TipSet, error) {
	log.LogKV(ctx, "findKnownAncestor", tip.Cid().String())

	var baseTipSet TipSet
	var path []TipSet

	// TODO: should be some sort of limit here
	// Some implementations limit the length of a chain that can be swapped.
	// Historically, bitcoin does not, this is purely for religious and
	// idealogical reasons. In reality, if a weeks worth of blocks is about to
	// be reverted, the system should opt to halt, not just happily switch over
	// to an entirely different chain.

	err := s.walkChain([]*types.Block{tip}, func(tips []*types.Block) (cont bool, err error) {
		// The tipset is known if all tips are known.
		known := true
		for _, blk := range tips {
			if !s.isKnownGoodBlock(blk.Cid()) {
				known = false
			}
		}
		// Even if the tipset is known its structure must be validated.
		// For example the tipset could contain all known blocks of
		// different heights.  TipSet validation includes validating each
		// block
		if err := s.validateTipSetStructure(ctx, tips); err != nil {
			return false, errors.Wrap(err, "validate tipset failed")
		}

		next, err := NewTipSet(tips...)
		if err != nil {
			return false, err
		}
		if known {
			baseTipSet = next
			return false, nil
		}

		path = append(path, next)

		return true, nil
	})
	if err != nil {
		return nil, nil, err
	}

	if len(baseTipSet) == 0 {
		return nil, nil, ErrInvalidBase
	}

	log.LogKV(ctx, "foundAncestorTipSet", baseTipSet.String())
	return baseTipSet, path, nil
}

func (s *ChainManager) isKnownGoodBlock(bc *cid.Cid) bool {
	if bc.Equals(s.genesisCid) {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.knownGoodBlocks.Has(bc)
}

func (s *ChainManager) addBlock(b *types.Block, id *cid.Cid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownGoodBlocks.Add(id)
	if err := s.tips.addBlock(b); err != nil {
		panic("Invalid block added to tipset.  Validation should have caught earlier")
	}
}

// AggregateStateTreeComputer is the signature for a function used to get the state of a tipset.
type AggregateStateTreeComputer func(context.Context, TipSet) (state.Tree, error)

// LoadParentStateTree returns the aggregate state tree of the input tipset's parent.
// Only tipsets that are already known to be valid by the chain manager should
// be provided as arguments.  Otherwise there is no guarantee that the returned
// state is valid or that this function won't panic.
// loadStateTreeResultsTS traverses the chain backwards to reach one of two base
// cases.  The traversal ends upon reaching a tipset of size 1, as the state
// can be read directly from the block.  Alternatively the traversal ends if
// the tipset's state tree has been cached by the chain manager from a previous
// traversal.
func (s *ChainManager) LoadParentStateTree(ctx context.Context, ts TipSet) (state.Tree, error) {
	// Get base state and gather tipsets to apply.
	var path []TipSet
	var st state.Tree
	err := s.walkChain(ts.ToSlice(), func(tips []*types.Block) (cont bool, err error) {
		next, err := NewTipSet(tips...)
		if err != nil {
			return false, errors.Wrap(err, "error creating TipSet from already validated chain section")
		}
		// Skip the head tipset.
		if next.Equals(ts) {
			return true, nil
		}

		if len(tips) == 1 {
			st, err = state.LoadStateTree(ctx, s.cstore, tips[0].StateRoot, builtin.Actors)
			return false, err
		}
		tipsID := next.String()
		if stateRoot, ok := s.stateCache[tipsID]; ok {
			st, err = state.LoadStateTree(ctx, s.cstore, stateRoot, builtin.Actors)
			return false, err
		}
		path = append(path, next)
		return true, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "error loading base state")
	}

	for i := len(path) - 1; i >= 0; i-- {
		next := path[i]
		_, err = s.tipSetProcessor(ctx, next, st)
		if err != nil {
			return nil, errors.Wrap(err, "failed to process tipset")
		}
		stateRoot, err := st.Flush(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to flush tree after applying state transitions")
		}
		s.stateCache[next.String()] = stateRoot
	}
	return st, nil
}

// LoadStateTreeTS returns the aggregate state of the input tipset.  This should
// only be called on tipsets that are already validated by the chain manager
func (s *ChainManager) LoadStateTreeTS(ctx context.Context, ts TipSet) (state.Tree, error) {
	// Return immediately if this tipset's state can be computed directly or is cached
	if len(ts) == 1 {
		return state.LoadStateTree(ctx, s.cstore, ts.ToSlice()[0].StateRoot, builtin.Actors)
	}
	if stateRoot, ok := s.stateCache[ts.String()]; ok {
		return state.LoadStateTree(ctx, s.cstore, stateRoot, builtin.Actors)
	}

	// Calculate by processing tipset on parent state
	st, err := s.LoadParentStateTree(ctx, ts)
	if err != nil {
		return nil, err
	}
	_, err = s.tipSetProcessor(ctx, ts, st)
	if err != nil {
		return nil, errors.Wrap(err, "failed to process tipset")
	}
	stateRoot, err := st.Flush(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to flush tree after applying state transitions")
	}
	s.stateCache[ts.String()] = stateRoot
	return st, nil
}

// InformNewBlock informs the chainmanager that we learned about a potentially
// new block from the given peer. Currently, it just fetches that block and
// passes it to the block processor (which fetches the rest of the chain on
// demand). In the (near) future we will want a better protocol for
// synchronizing the blockchain and downloading it efficiently.
// TODO: sync logic should be decoupled and off in a separate worker. This
// method should not block
func (s *ChainManager) InformNewBlock(from peer.ID, c *cid.Cid, h uint64) {
	ts := s.GetHeaviestTipSet()
	if len(ts) == 0 {
		panic("best tip set must have at least one block")
	}
	// TODO: this method should be reworked to include non-longest heaviest
	if ts.ToSlice()[0].Height >= h {
		return
	}

	// Naive sync.
	// TODO: more dedicated sync protocols, like "getBlockHashes(range)"
	ctx := context.TODO()
	blk, err := s.FetchBlock(ctx, c)
	if err != nil {
		log.Error("failed to fetch block: ", err)
		return
	}

	_, err = s.ProcessNewBlock(ctx, blk)
	if err != nil {
		log.Error("processing new block: ", err)
		return
	}
}

// Stop stops all activities and cleans up.
func (s *ChainManager) Stop() {
	s.HeaviestTipSetPubSub.Shutdown()
}

// ChainManagerForTest provides backdoor access to internal fields to make
// testing easier. You are a bad person if you use this outside of a test.
type ChainManagerForTest = ChainManager

// SetHeaviestTipSetForTest enables setting the best tipset directly. Don't use this
// outside of a testing context.
func (s *ChainManagerForTest) SetHeaviestTipSetForTest(ctx context.Context, ts TipSet) error {
	// added to make `LogKV` call in `setHeaviestTipSet` happy (else it logs an error message)
	log.Start(ctx, "SetHeaviestTipSetForTest")
	for _, b := range ts {
		_, err := s.cstore.Put(ctx, b)
		if err != nil {
			return errors.Wrap(err, "failed to put block to disk")
		}
		id := b.Cid()
		s.addBlock(b, id)
	}
	defer log.Finish(ctx)
	return s.setHeaviestTipSet(ctx, ts)
}

// BlockHistory returns a channel of block pointers (or errors), starting with the current best tipset's blocks
// followed by each subsequent parent and ending with the genesis block, after which the channel
// is closed. If an error is encountered while fetching a block, the error is sent, and the channel is closed.
func (s *ChainManager) BlockHistory(ctx context.Context) <-chan interface{} {
	out := make(chan interface{})
	tips := s.GetHeaviestTipSet().ToSlice()

	go func() {
		defer close(out)
		err := s.walkChain(tips, func(tips []*types.Block) (cont bool, err error) {
			var raw interface{}
			raw, err = NewTipSet(tips...)
			if err != nil {
				raw = err
			}
			select {
			case <-ctx.Done():
				return false, nil
			case out <- raw:
			}
			return true, nil
		})
		if err != nil {
			select {
			case <-ctx.Done():
			case out <- err:
			}
		}
	}()
	return out
}

// receiptFromTipSet finds the receipt for the jth message of the tipset ordered by
// by ticket size.  This can differ from the jth's message receipt stored
// in the ith block in the case that the jth message is in conflict with another
// message of the tipset.
func (s *ChainManager) receiptFromTipSet(ctx context.Context, j int, ts TipSet) (*types.MessageReceipt, error) {
	// Receipts always match block if tipset has only 1 member.
	var rcpt *types.MessageReceipt
	blks := ts.ToSlice()
	if len(ts) == 1 {
		b := blks[0]
		// TODO: this should return an error if a receipt doesn't exist.
		// Right now doing so breaks tests because our test helpers
		// don't correctly apply messages when making test chains.
		if j < len(b.MessageReceipts) {
			rcpt = b.MessageReceipts[j]
		}
		return rcpt, nil
	}

	// Apply all the tipset's messages to determine the correct receipts.
	st, err := s.LoadParentStateTree(ctx, ts)
	if err != nil {
		return nil, err
	}
	res, err := s.tipSetProcessor(ctx, ts, st)
	if err != nil {
		return nil, err
	}
	// If no conflicts original index applies
	// TODO: as noted above an out of bounds receipt index should
	// eventually return an error.
	if res.Failures.Empty() {
		if j < len(res.Results) {
			rcpt = res.Results[j].Receipt
		}
		return rcpt, nil
	}

	// Calculate a new index accounting for conflicting messages
	sort.Slice(blks, func(i, j int) bool {
		return bytes.Compare(blks[i].Ticket, blks[j].Ticket) == -1
	})
	var msgCnt int
	for _, b := range blks {
		for _, msg := range b.Messages {
			mCid, err := msg.Cid()
			if err != nil {
				return nil, err
			}
			if res.Failures.Has(mCid) {
				// Selected message was a conflict-failure, return empty receipt.
				if msgCnt == j {
					return rcpt, nil
				}
				j--
			} else {
				msgCnt++
			}
			if j < msgCnt {
				j = msgCnt
				break
			}
		}
	}

	// TODO: as noted above an out of bounds receipt index should
	// eventually return an error.
	if j < len(res.Results) {
		rcpt = res.Results[j].Receipt
	}
	return rcpt, nil
}

// WaitForMessage searches for a message with Cid, msgCid, then passes it, along with the containing Block and any
// MessageRecipt, to the supplied callback, cb. If an error is encountered, it is returned. Note that it is logically
// possible that an error is returned and the success callback is called. In that case, the error can be safely ignored.
// TODO: This implementation will become prohibitively expensive since it involves traversing the entire blockchain.
//       We should replace with an index later.
func (s *ChainManager) WaitForMessage(ctx context.Context, msgCid *cid.Cid, cb func(*types.Block, *types.Message,
	*types.MessageReceipt) error) (retErr error) {
	// Ch will contain a stream of blocks to check for message (or errors).
	// Blocks are either in new heaviest tipsets, or next oldest historical blocks.
	ch := make(chan (interface{}))

	// New blocks
	newTipSetCh := s.HeaviestTipSetPubSub.Sub(HeaviestTipSetTopic)
	defer s.HeaviestTipSetPubSub.Unsub(newTipSetCh, HeaviestTipSetTopic)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Historical blocks
	historyCh := s.BlockHistory(ctx)

	// Merge historical and new block Channels.
	go func() {
		// TODO: accommodate a new chain being added, as opposed to just a single block.
		for raw := range newTipSetCh {
			ch <- raw
		}
	}()
	go func() {
		// TODO make history serve up tipsets
		for raw := range historyCh {
			ch <- raw
		}
	}()

	for raw := range ch {
		switch ts := raw.(type) {
		case error:
			log.Errorf("chainManager.WaitForMessage: %s", ts)
			return ts
		case TipSet:
			blks := ts.ToSlice()
			sort.Slice(blks, func(i, j int) bool {
				return bytes.Compare(blks[i].Ticket, blks[j].Ticket) == -1
			})
			var msgTotal int
			for _, blk := range blks {
				for j, msg := range blk.Messages {
					c, err := msg.Cid()
					if err != nil {
						log.Errorf("chainManager.WaitForMessage: %s", err)
						return err
					}
					if c.Equals(msgCid) {
						recpt, err := s.receiptFromTipSet(ctx, msgTotal+j, ts)
						if err != nil {
							return errors.Wrap(err, "error retrieving receipt from tipset")
						}
						return cb(blk, msg, recpt)
					}
				}
				msgTotal += len(blk.Messages)
			}
		}
	}

	return retErr
}

// Called for each step in the walk for walkChain(). The path contains all nodes traversed,
// including all tips at each height. Return true to continue walking, false to stop.
type walkChainCallback func(tips []*types.Block) (cont bool, err error)

// walkChain walks backward through the chain, starting at tips, invoking cb() at each height.
func (s *ChainManager) walkChain(tips []*types.Block, cb walkChainCallback) error {
	for {
		cont, err := cb(tips)
		if err != nil {
			return errors.Wrap(err, "error processing block")
		}
		if !cont {
			return nil
		}
		ids := tips[0].Parents
		if ids.Empty() {
			break
		}

		tips = tips[:0]
		for it := ids.Iter(); !it.Complete(); it.Next() {
			pid := it.Value()
			p, err := s.FetchBlock(context.TODO(), pid)
			if err != nil {
				return errors.Wrap(err, "error fetching block")
			}
			tips = append(tips, p)
		}
	}

	return nil
}

// GetTipSetByBlock returns the tipset associated with a given block by
// performing a lookup on its parent set.  The tipset returned is a
// cloned shallow copy of the version stored in the index
func (s *ChainManager) GetTipSetByBlock(blk *types.Block) (TipSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.tips[blk.Height][keyForParentSet(blk.Parents)]
	if !ok {
		return TipSet{}, errors.New("block's tipset not indexed by chain_mgr")
	}
	return ts.Clone(), nil
}

// GetTipSetsByHeight returns all tipsets at the given height. Neither the returned
// slice nor its members will be mutated by the ChainManager once returned.
func (s *ChainManager) GetTipSetsByHeight(height uint64) (tips []TipSet) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tsbp, ok := s.tips[height]
	if ok {
		for _, ts := range tsbp {
			// Assumption here that the blocks contained in `ts` are never mutated.
			tips = append(tips, ts.Clone())
		}
	}
	return tips
}
