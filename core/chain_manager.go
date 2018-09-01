package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"gx/ipfs/QmQZadYTDF4ud9DdK85PH2vReJRzUM9YfVW4ReB1q2m51p/go-hamt-ipld"
	"gx/ipfs/QmQsErDt8Qgw1XrsXf2BpEzDgGWtB1YLsTAARBup5b6B9W/go-libp2p-peer"
	logging "gx/ipfs/QmRREK2CAZ5Re2Bd9zZFG6FeYDppUWt5cMgsoUEp3ktgSr/go-log"
	"gx/ipfs/QmVG5gxteQNEMhrS8prJSmU2C9rebtFuTd3SYZ5kE3YZ5k/go-datastore"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	"gx/ipfs/QmcmpX42gtDv1fz24kau4wjS9hfwWj5VexWBKgGnWzsyag/go-ipfs-blockstore"
	"gx/ipfs/QmdbxjQWogRCHRaxhhGnYdT1oQJzL9GdqSKzCdqWr85AP2/pubsub"

	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/chain"
	statetree "github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	pp "github.com/filecoin-project/go-filecoin/util/prettyprint"
	"github.com/filecoin-project/go-filecoin/vm"
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
	// ErrUninit is returned when the chain manager is called to process a block but does not have a genesis block
	ErrUninit = errors.New("the chain manager cannot process blocks without a genesis block")
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

	// Uninit implies that the chain manager does not have a genesis block
	// and therefore cannot process new blocks.
	Uninit
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
		sync.RWMutex
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
	// stateCacheLk protects the stateCache.
	stateCacheLk sync.RWMutex

	cstore *hamt.CborIpldStore

	// for mutable data
	ds datastore.Datastore

	// for ipld objects
	Blockstore blockstore.Blockstore

	// PwrTableView provides miner and total power for the EC chain weight
	// computation.
	PwrTableView PowerTableView

	// HeaviestTipSetPubSub is a pubsub channel that publishes all best tipsets.
	// We operate under the assumption that tipsets published to this channel
	// will always be queued and delivered to subscribers in the order discovered.
	// Successive published tipsets may be supersets of previously published tipsets.
	HeaviestTipSetPubSub *pubsub.PubSub

	FetchBlock        func(context.Context, *cid.Cid) (*chain.Block, error)
	GetHeaviestTipSet func() TipSet
}

// NewChainManager creates a new filecoin chain manager.
// TODO: taking three data things feels a bit weird. Two makes sense, mutable and immutable.
//       figure out how to coalesce the blockstore and ipldstore into a single
//       object (theyre the same under the hood)
func NewChainManager(ds datastore.Datastore, bs blockstore.Blockstore, cs *hamt.CborIpldStore) *ChainManager {
	cm := &ChainManager{
		cstore:          cs,
		ds:              ds,
		Blockstore:      bs,
		blockProcessor:  ProcessBlock,
		tipSetProcessor: ProcessTipSet,
		knownGoodBlocks: cid.NewSet(),
		tips:            tipIndex{},
		stateCache:      make(map[string]*cid.Cid),

		PwrTableView:         &marketView{},
		HeaviestTipSetPubSub: pubsub.New(128),
	}
	cm.FetchBlock = cm.fetchBlock
	cm.GetHeaviestTipSet = cm.getHeaviestTipSet

	return cm
}

// Genesis creates a new genesis block and sets it as the the best known block.
func (cm *ChainManager) Genesis(ctx context.Context, gen GenesisInitFunc) (err error) {
	ctx = log.Start(ctx, "ChainManager.Genesis")
	defer func() {
		log.FinishWithErr(ctx, err)
	}()
	genesis, err := gen(cm.cstore, cm.Blockstore)
	if err != nil {
		return err
	}

	cm.genesisCid = genesis.Cid()

	cm.addBlock(genesis, cm.genesisCid)
	genTipSet, err := NewTipSet(genesis)
	if err != nil {
		return err
	}

	cm.heaviestTipSet.Lock()
	defer cm.heaviestTipSet.Unlock()

	return cm.setHeaviestTipSet(ctx, genTipSet)
}

// setHeaviestTipSet sets the best tipset.  CALLER MUST HOLD THE heaviestTipSet LOCK.
func (cm *ChainManager) setHeaviestTipSet(ctx context.Context, ts TipSet) error {
	log.LogKV(ctx, "setHeaviestTipSet", ts.String())
	if err := putCidSet(ctx, cm.ds, heaviestTipSetKey, ts.ToSortedCidSet()); err != nil {
		return errors.Wrap(err, "failed to write TipSet cids to datastore")
	}
	cm.HeaviestTipSetPubSub.Pub(ts, HeaviestTipSetTopic)
	// The heaviest tipset should not pick up changes from adding new blocks to the index.
	// It only changes explicitly when set through this function.
	cm.heaviestTipSet.ts = ts.Clone()

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
func (cm *ChainManager) Load() error {
	tipCids, err := cm.readHeaviestTipSetCids()
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
		blk, err := cm.FetchBlock(context.TODO(), it.Value())
		if err != nil {
			return errors.Wrap(err, "failed to load block in head TipSet")
		}
		err = ts.AddBlock(blk)
		if err != nil {
			return errors.Wrap(err, "failed to add validated block to TipSet")
		}
	}

	var genesii []*chain.Block
	err = cm.walkChain(ts.ToSlice(), func(tips []*chain.Block) (cont bool, err error) {
		for _, t := range tips {
			id := t.Cid()
			cm.addBlock(t, id)
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
		cm.genesisCid = genesii[0].Cid()

		cm.heaviestTipSet.Lock()
		cm.heaviestTipSet.ts = ts
		cm.heaviestTipSet.Unlock()
	case 0:
		panic("unreached")
	default:
		panic("invalid chain - more than one genesis block found")
	}

	return nil
}

func (cm *ChainManager) readHeaviestTipSetCids() (*types.SortedCidSet, error) {
	bb, err := cm.ds.Get(heaviestTipSetKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read heaviestTipSetKey")
	}

	var cids types.SortedCidSet
	err = json.Unmarshal(bb, &cids)
	if err != nil {
		return nil, errors.Wrap(err, "casting stored heaviestTipSetCids failed")
	}

	return &cids, nil
}

// GetGenesisCid returns the cid of the current genesis block.
func (cm *ChainManager) GetGenesisCid() *cid.Cid {
	return cm.genesisCid
}

// HeaviestTipSetGetter is the signature for a functin used to get the current best tipset.
type HeaviestTipSetGetter func() TipSet

// GetHeaviestTipSet returns the tipset at the head of our current 'best' chain.
func (cm *ChainManager) getHeaviestTipSet() TipSet {
	cm.heaviestTipSet.RLock()
	defer cm.heaviestTipSet.RUnlock()

	return cm.heaviestTipSet.ts
}

// maybeAcceptBlock attempts to accept blk if its score is greater than the current best block,
// otherwise returning ChainValid.
func (cm *ChainManager) maybeAcceptBlock(ctx context.Context, blk *chain.Block) (BlockProcessResult, error) {
	// We have to hold the lock at this level to avoid TOCTOU problems
	// with the new heaviest tipset.
	log.LogKV(ctx, "maybeAcceptBlock", blk.Cid().String())
	cm.heaviestTipSet.Lock()
	defer cm.heaviestTipSet.Unlock()

	ts, err := cm.GetTipSetByBlock(blk)
	if err != nil {
		return Unknown, err
	}
	// Calculate weights of TipSets for comparison.
	heaviestWeight, err := cm.weight(ctx, cm.heaviestTipSet.ts)
	if err != nil {
		return Unknown, err
	}
	newWeight, err := cm.weight(ctx, ts)
	if err != nil {
		return Unknown, err
	}
	heaviestTicket, err := cm.heaviestTipSet.ts.MinTicket()
	if err != nil {
		return Unknown, err
	}
	newTicket, err := ts.MinTicket()
	if err != nil {
		return Unknown, err
	}
	if newWeight.Cmp(heaviestWeight) == -1 ||
		(newWeight.Cmp(heaviestWeight) == 0 &&
			// break ties by choosing tipset with smaller ticket
			bytes.Compare(newTicket, heaviestTicket) >= 0) {
		return ChainValid, nil
	}

	// set the given tipset as our current heaviest tipset
	if err := cm.setHeaviestTipSet(ctx, ts); err != nil {
		return Unknown, err
	}
	log.Infof("new heaviest tipset, [s=%s, hs=%s]", newWeight.RatString(), ts.String())
	log.LogKV(ctx, "maybeAcceptBlock", ts.String())
	return ChainAccepted, nil
}

// NewBlockProcessor is the signature for a function which processes a new block.
type NewBlockProcessor func(context.Context, *chain.Block) (BlockProcessResult, error)

// ProcessNewBlock sends a new block to the chain manager. If the block is in a
// tipset heavier than our current heaviest, this tipset is accepted as our
// heaviest tipset. Otherwise an error is returned explaining why it was not accepted.
func (cm *ChainManager) ProcessNewBlock(ctx context.Context, blk *chain.Block) (bpr BlockProcessResult, err error) {
	ctx = log.Start(ctx, "ChainManager.ProcessNewBlock")
	defer func() {
		log.SetTag(ctx, "result", bpr.String())
		log.FinishWithErr(ctx, err)
	}()
	log.Infof("processing block [s=%d, cid=%s]", blk.Score(), blk.Cid())
	if cm.genesisCid == nil {
		return Uninit, ErrUninit
	}

	// TODO: this is really confusing. This function needs a better name than 'state'
	// and it should be much more clear about *why* its doing these things
	switch _, err := cm.state(ctx, []*chain.Block{blk}); err {
	default:
		return Unknown, errors.Wrap(err, "validate block failed")
	case ErrInvalidBase:
		return InvalidBase, ErrInvalidBase
	case nil:
		return cm.maybeAcceptBlock(ctx, blk)
	}
}

// fetchBlock gets the requested block, either from disk or from the network.
func (cm *ChainManager) fetchBlock(ctx context.Context, c *cid.Cid) (*chain.Block, error) {
	log.Infof("fetching block, [%s]", c.String())

	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	var blk chain.Block
	if err := cm.cstore.Get(ctx, c, &blk); err != nil {
		return nil, err
	}

	return &blk, nil
}

// newValidTipSet creates a new tipset from the input blocks that is guaranteed
// to be valid.  It operates by validating each block and further checking that
// this tipset contains only blocks with the same heights, parent weights,
// and parent sets.
func (cm *ChainManager) newValidTipSet(ctx context.Context, blks []*chain.Block) (TipSet, error) {
	for _, blk := range blks {
		if err := cm.validateBlockStructure(ctx, blk); err != nil {
			return nil, err
		}
	}
	return NewTipSet(blks...)
}

// validateBlockStructure verifies that this block, on its own, is structurally and
// cryptographically valid. This means checking that all of its fields are
// properly filled out and its signatures are correct. Checking the validity of
// state changes must be done separately and only once the state of the
// previous block has been validated. TODO: not yet signature checking
func (cm *ChainManager) validateBlockStructure(ctx context.Context, b *chain.Block) error {
	// TODO: validate signatures on messages
	log.LogKV(ctx, "validateBlockStructure", b.Cid().String())
	if b.StateRoot == nil {
		return fmt.Errorf("block has nil StateRoot")
	}

	// TODO: validate that this miner had a winning ticket last block.
	// In general this may depend on block farther back in the chain (lookback param).

	return nil
}

// State is a wrapper for state that logs a trace. before returning the
// validated state of the input blocks.  initializing a trace can't happen
// within state because it is a recursive function and would log a new
// trace for each invocation.
func (cm *ChainManager) State(ctx context.Context, blks []*chain.Block) (statetree.Tree, error) {
	ctx = log.Start(ctx, "State")
	log.Info("Calling State")
	return cm.state(ctx, blks)
}

// state returns the aggregate state tree for the blocks or an error if the
// blocks are not a valid tipset or are not part of a valid chain.
func (cm *ChainManager) state(ctx context.Context, blks []*chain.Block) (statetree.Tree, error) {
	ts, err := cm.newValidTipSet(ctx, blks)
	if err != nil {
		return nil, errors.Wrapf(err, "blks do not form a valid tipset: %s", pp.StringFromBlocks(blks))
	}

	// Return cache hit
	cm.stateCacheLk.RLock()
	root, ok := cm.stateCache[ts.String()]
	cm.stateCacheLk.RUnlock()

	if ok { // tipset in cache
		st, err := statetree.LoadStateTree(ctx, cm.cstore, root, builtin.Actors)
		return st, err
	}
	// Base case is the genesis block
	if len(ts) == 1 && blks[0].Cid().Equals(cm.genesisCid) { // genesis tipset
		st, err := statetree.LoadStateTree(ctx, cm.cstore, blks[0].StateRoot, builtin.Actors)
		return st, err
	}

	// Recursive case: construct valid tipset from valid parent
	pBlks, err := cm.fetchParentBlks(ctx, ts)
	if err != nil {
		return nil, err
	}
	if len(pBlks) == 0 { // invalid genesis tipset
		return nil, ErrInvalidBase
	}
	st, err := cm.state(ctx, pBlks)
	if err != nil {
		return nil, err
	}
	err = cm.validateMining(ctx, st, cm.Blockstore, ts)
	if err != nil {
		return nil, err
	}
	vms := vm.NewStorageMap(cm.Blockstore)
	st, err = cm.runMessages(ctx, st, vms, ts)
	if err != nil {
		return nil, err
	}
	if err = cm.flushAndCache(ctx, st, vms, ts); err != nil {
		return nil, err
	}
	return st, nil
}

// fetchParentBlks returns the blocks in the parent set of the input tipset.
func (cm *ChainManager) fetchParentBlks(ctx context.Context, ts TipSet) ([]*chain.Block, error) {
	ids, err := ts.Parents()
	if err != nil {
		return nil, err
	}
	return cm.fetchBlksForIDs(ctx, ids)
}

// fetchBlks returns the blocks in the input cid set.
func (cm *ChainManager) fetchBlksForIDs(ctx context.Context, ids types.SortedCidSet) ([]*chain.Block, error) {
	var pBlks []*chain.Block
	for it := ids.Iter(); !it.Complete(); it.Next() {
		pid := it.Value()
		p, err := cm.FetchBlock(ctx, pid)
		if err != nil {
			return nil, errors.Wrap(err, "error fetching block")
		}
		pBlks = append(pBlks, p)
	}
	return pBlks, nil
}

// validateMining throws an error if any tipset's block was mined by an invalid
// miner address.
func (cm *ChainManager) validateMining(ctx context.Context, st statetree.Tree, bstore blockstore.Blockstore, ts TipSet) error {
	for _, blk := range ts {
		if !cm.PwrTableView.HasPower(ctx, st, bstore, blk.Miner) {
			return errors.New("invalid miner address without network power")
		}
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
func (cm *ChainManager) runMessages(ctx context.Context, st statetree.Tree, vms vm.StorageMap, ts TipSet) (statetree.Tree, error) {
	var cpySt statetree.Tree
	for _, blk := range ts {
		cpyCid, err := st.Flush(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}
		// state copied so changes don't propagate between block validations
		cpySt, err = statetree.LoadStateTree(ctx, cm.cstore, cpyCid, builtin.Actors)
		if err != nil {
			return nil, errors.Wrap(err, "error validating block state")
		}

		receipts, err := cm.blockProcessor(ctx, blk, cpySt, vms)
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
	_, err := cm.tipSetProcessor(ctx, ts, st, vms)
	if err != nil {
		return nil, errors.Wrap(err, "error validating tipset")
	}
	return st, nil
}

// flushAndCache flushes and caches the input tipset's state.  It also persists
// the tipset's blocks in the ChainManager's data store.
func (cm *ChainManager) flushAndCache(ctx context.Context, st statetree.Tree, vms vm.StorageMap, ts TipSet) error {
	for _, blk := range ts {
		if _, err := cm.cstore.Put(ctx, blk); err != nil {
			return errors.Wrap(err, "failed to store block")
		}
		cm.addBlock(blk, blk.Cid())
	}
	root, err := st.Flush(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to flush state")
	}
	err = vms.Flush()
	if err != nil {
		return errors.Wrap(err, "failed to flush actor state")
	}
	cm.stateCacheLk.Lock()
	cm.stateCache[ts.String()] = root
	cm.stateCacheLk.Unlock()

	return nil
}

func (cm *ChainManager) addBlock(b *chain.Block, id *cid.Cid) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.knownGoodBlocks.Add(id)
	if err := cm.tips.addBlock(b); err != nil {
		panic("Invalid block added to tipset.  Validation should have caught earlier")
	}
}

// AggregateStateTreeComputer is the signature for a function used to get the state of a tipset.
type AggregateStateTreeComputer func(context.Context, TipSet) (statetree.Tree, error)

// stateForBlockIDs returns the state of the tipset consisting of the input
// blockIDs.
func (cm *ChainManager) stateForBlockIDs(ctx context.Context, ids types.SortedCidSet) (statetree.Tree, error) {
	blks, err := cm.fetchBlksForIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(blks) == 0 { // no ids
		return nil, errors.New("cannot get state of tipset with no members")
	}
	return cm.state(ctx, blks)
}

// InformNewTipSet informs the chainmanager that we learned about a potentially
// new tipset from the given peer. It fetches that tipset's blocks and
// passes them to the block processor (which fetches the rest of the chain on
// demand). In the (near) future we will want a better protocol for
// synchronizing the blockchain and downloading it efficiently.
// TODO: sync logic should be decoupled and off in a separate worker. This
// method should not block
func (cm *ChainManager) InformNewTipSet(from peer.ID, cids []*cid.Cid, h uint64) {
	// Naive sync.
	// TODO: more dedicated sync protocols, like "getBlockHashes(range)"
	ctx := context.TODO()

	for _, c := range cids {
		blk, err := cm.FetchBlock(ctx, c)
		if err != nil {
			log.Error("failed to fetch block: ", err)
			return
		}
		_, err = cm.ProcessNewBlock(ctx, blk)
		if err != nil {
			log.Error("processing new block: ", err)
			return
		}
	}
}

// Stop stops all activities and cleans up.
func (cm *ChainManager) Stop() {
	cm.HeaviestTipSetPubSub.Shutdown()
}

// ChainManagerForTest provides backdoor access to internal fields to make
// testing easier. You are a bad person if you use this outside of a test.
type ChainManagerForTest = ChainManager

// SetHeaviestTipSetForTest enables setting the best tipset directly. Don't use this
// outside of a testing context.
func (cm *ChainManagerForTest) SetHeaviestTipSetForTest(ctx context.Context, ts TipSet) error {
	// added to make `LogKV` call in `setHeaviestTipSet` happy (else it logs an error message)
	ctx = log.Start(ctx, "SetHeaviestTipSetForTest")
	for _, b := range ts {
		_, err := cm.cstore.Put(ctx, b)
		if err != nil {
			return errors.Wrap(err, "failed to put block to disk")
		}
		id := b.Cid()
		cm.addBlock(b, id)
	}
	defer log.Finish(ctx)
	cm.heaviestTipSet.Lock()
	defer cm.heaviestTipSet.Unlock()
	return cm.setHeaviestTipSet(ctx, ts)
}

// BlockHistory returns a channel of block pointers (or errors), starting with the current best tipset's blocks
// followed by each subsequent parent and ending with the genesis block, after which the channel
// is closed. If an error is encountered while fetching a block, the error is sent, and the channel is closed.
func (cm *ChainManager) BlockHistory(ctx context.Context) <-chan interface{} {
	ctx = log.Start(ctx, "ChainManager.BlockHistory")
	out := make(chan interface{})
	tips := cm.GetHeaviestTipSet().ToSlice()

	go func() {
		defer close(out)
		defer log.Finish(ctx)
		err := cm.walkChain(tips, func(tips []*chain.Block) (cont bool, err error) {
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

// msgIndexOfTipSet returns the order in which  msgCid apperas in the canonical
// message ordering of the given tipset, or an error if it is not in the
// tipset.
func msgIndexOfTipSet(msgCid *cid.Cid, ts TipSet, fails types.SortedCidSet) (int, error) {
	blks := ts.ToSlice()
	chain.SortBlocks(blks)
	var duplicates types.SortedCidSet
	var msgCnt int
	for _, b := range blks {
		for _, msg := range b.Messages {
			c, err := msg.Cid()
			if err != nil {
				return -1, err
			}
			if fails.Has(c) {
				continue
			}
			if duplicates.Has(c) {
				continue
			}
			(&duplicates).Add(c)
			if c.Equals(msgCid) {
				return msgCnt, nil
			}
			msgCnt++
		}
	}

	return -1, fmt.Errorf("message cid %s not in tipset", msgCid.String())
}

// receiptFromTipSet finds the receipt for the message with msgCid in the input
// input tipset.  This can differ from the message's receipt as stored in its
// parent block in the case that the message is in conflict with another
// message of the tipset.
func (cm *ChainManager) receiptFromTipSet(ctx context.Context, msgCid *cid.Cid, ts TipSet) (*chain.MessageReceipt, error) {
	// Receipts always match block if tipset has only 1 member.
	var rcpt *chain.MessageReceipt
	blks := ts.ToSlice()
	if len(ts) == 1 {
		b := blks[0]
		// TODO: this should return an error if a receipt doesn't exist.
		// Right now doing so breaks tests because our test helpers
		// don't correctly apply messages when making test chains.
		j, err := msgIndexOfTipSet(msgCid, ts, types.SortedCidSet{})
		if err != nil {
			return nil, err
		}
		if j < len(b.MessageReceipts) {
			rcpt = b.MessageReceipts[j]
		}
		return rcpt, nil
	}

	// Apply all the tipset's messages to determine the correct receipts.
	ids, err := ts.Parents()
	if err != nil {
		return nil, err
	}
	st, err := cm.stateForBlockIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	res, err := cm.tipSetProcessor(ctx, ts, st, vm.NewStorageMap(cm.Blockstore))
	if err != nil {
		return nil, err
	}

	// If this is a failing conflict message there is no application receipt.
	if res.Failures.Has(msgCid) {
		return nil, nil
	}

	j, err := msgIndexOfTipSet(msgCid, ts, res.Failures)
	if err != nil {
		return nil, err
	}
	// TODO: and of bounds receipt index should return an error.
	if j < len(res.Results) {
		rcpt = res.Results[j].Receipt
	}
	return rcpt, nil
}

// ECV is the constant V defined in the EC spec.  TODO: the value of V needs
//  motivation at the protocol design level
const ECV uint64 = 10

// ECPrM is the power ratio magnitude defined in the EC spec.  TODO: the value
// of this constant needs motivation at the protocol level
const ECPrM uint64 = 100

// weight returns the EC weight of this TipSet
// TODO: this implementation needs to handle precision correctly, see issue #655.
func (cm *ChainManager) weight(ctx context.Context, ts TipSet) (*big.Rat, error) {
	if len(ts) == 1 && ts.ToSlice()[0].Cid().Equals(cm.genesisCid) {
		return big.NewRat(int64(0), int64(1)), nil
	}
	// Gather parent and state.
	parentIDs, err := ts.Parents()
	if err != nil {
		return nil, err
	}
	st, err := cm.stateForBlockIDs(ctx, parentIDs)
	if err != nil {
		return nil, errors.Wrap(err, "get weight, stateForParents failed")
	}

	wNum, wDenom, err := ts.ParentWeight()
	if err != nil {
		return nil, errors.Wrap(err, "computing parent weight")
	}
	if wDenom == uint64(0) {
		return nil, errors.New("storage market with 0 bytes stored not handled")
	}
	w := big.NewRat(int64(wNum), int64(wDenom))

	// Each block in the tipset adds ECV + ECPrm * miner_power
	totalBytes, err := cm.PwrTableView.Total(ctx, st, cm.Blockstore)
	if err != nil {
		return nil, errors.Wrap(err, "getting total power")
	}
	ratECV := big.NewRat(int64(ECV), int64(1))
	for _, blk := range ts {
		minerBytes, err := cm.PwrTableView.Miner(ctx, st, cm.Blockstore, blk.Miner)
		if err != nil {
			return nil, errors.Wrap(err, "getting miner power")
		}
		wNumBlk := int64(ECPrM * minerBytes)
		wBlk := big.NewRat(wNumBlk, int64(totalBytes))
		wBlk.Add(wBlk, ratECV)
		w.Add(w, wBlk)
	}
	return w, nil
}

// Weight returns the numerator and denominator of the weight of the input tipset.
func (cm *ChainManager) Weight(ctx context.Context, ts TipSet) (numer uint64, denom uint64, err error) {
	ctx = log.Start(ctx, "ChainManager.Weight")
	log.SetTag(ctx, "tipSet", ts)
	defer func() {
		log.SetTags(ctx, map[string]interface{}{
			"numerator":   numer,
			"denominator": denom,
		})
		log.FinishWithErr(ctx, err)
	}()
	w, err := cm.weight(ctx, ts)
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

// WaitForMessage searches for a message with Cid, msgCid, then passes it, along with the containing Block and any
// MessageRecipt, to the supplied callback, cb. If an error is encountered, it is returned. Note that it is logically
// possible that an error is returned and the success callback is called. In that case, the error can be safely ignored.
// TODO: This implementation will become prohibitively expensive since it involves traversing the entire blockchain.
//       We should replace with an index later.
func (cm *ChainManager) WaitForMessage(ctx context.Context, msgCid *cid.Cid, cb func(*chain.Block, *chain.SignedMessage,
	*chain.MessageReceipt) error) (retErr error) {
	ctx = log.Start(ctx, "WaitForMessage")
	log.SetTag(ctx, "messageCid", msgCid.String())
	defer log.Finish(ctx)
	log.Info("Calling WaitForMessage")
	// Ch will contain a stream of blocks to check for message (or errors).
	// Blocks are either in new heaviest tipsets, or next oldest historical blocks.
	ch := make(chan (interface{}))

	// New blocks
	newTipSetCh := cm.HeaviestTipSetPubSub.Sub(HeaviestTipSetTopic)
	defer cm.HeaviestTipSetPubSub.Unsub(newTipSetCh, HeaviestTipSetTopic)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Historical blocks
	historyCh := cm.BlockHistory(ctx)

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
			for _, blk := range ts {
				for _, msg := range blk.Messages {
					c, err := msg.Cid()
					if err != nil {
						log.Errorf("chainManager.WaitForMessage: %s", err)
						return err
					}
					if c.Equals(msgCid) {
						recpt, err := cm.receiptFromTipSet(ctx, msgCid, ts)
						if err != nil {
							return errors.Wrap(err, "error retrieving receipt from tipset")
						}
						return cb(blk, msg, recpt)
					}
				}
			}
		}
	}

	return retErr
}

// Called for each step in the walk for walkChain(). The path contains all nodes traversed,
// including all tips at each height. Return true to continue walking, false to stop.
type walkChainCallback func(tips []*chain.Block) (cont bool, err error)

// walkChain walks backward through the chain, starting at tips, invoking cb() at each height.
func (cm *ChainManager) walkChain(tips []*chain.Block, cb walkChainCallback) error {
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
			p, err := cm.FetchBlock(context.TODO(), pid)
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
func (cm *ChainManager) GetTipSetByBlock(blk *chain.Block) (TipSet, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	ts, ok := cm.tips[uint64(blk.Height)][keyForParentSet(blk.Parents)]
	if !ok {
		return TipSet{}, errors.New("block's tipset not indexed by chain_mgr")
	}
	return ts.Clone(), nil
}

// GetTipSetsByHeight returns all tipsets at the given height. Neither the returned
// slice nor its members will be mutated by the ChainManager once returned.
func (cm *ChainManager) GetTipSetsByHeight(height uint64) (tips []TipSet) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	tsbp, ok := cm.tips[height]
	if ok {
		for _, ts := range tsbp {
			// Assumption here that the blocks contained in `ts` are never mutated.
			tips = append(tips, ts.Clone())
		}
	}
	return tips
}
