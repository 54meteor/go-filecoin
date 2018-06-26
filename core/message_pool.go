package core

import (
	"context"
	"sort"
	"sync"

	errors "gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	hamt "gx/ipfs/QmcYBp5EDnJKfVN63F71rDTksvEf1cfijwCTWtw6bPG58T/go-hamt-ipld"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	"github.com/filecoin-project/go-filecoin/types"
)

// MessagePool keeps an unordered, de-duplicated set of Messages and supports removal by CID.
// By 'de-duplicated' we mean that insertion of a message by cid that already
// exists is a nop. We use a MessagePool to store all messages received by this node
// via network or directly created via user command that have yet to be included
// in a block. Messages are removed as they are processed.
//
// MessagePool is safe for concurrent access.
type MessagePool struct {
	lk sync.RWMutex

	pending map[string]*types.Message // all pending messages
}

// Add adds a message to the pool.
func (pool *MessagePool) Add(msg *types.Message) (*cid.Cid, error) {
	pool.lk.Lock()
	defer pool.lk.Unlock()

	c, err := msg.Cid()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create CID")
	}

	pool.pending[c.KeyString()] = msg
	return c, nil
}

// Pending returns all pending messages.
func (pool *MessagePool) Pending() []*types.Message {
	out := make([]*types.Message, 0, len(pool.pending))
	for _, msg := range pool.pending {
		out = append(out, msg)
	}

	return out
}

// Remove removes the message by CID from the pending pool.
func (pool *MessagePool) Remove(c *cid.Cid) {
	delete(pool.pending, c.KeyString())
}

// NewMessagePool constructs a new MessagePool.
func NewMessagePool() *MessagePool {
	return &MessagePool{
		pending: make(map[string]*types.Message),
	}
}

// getParentTips returns the parent tipset of the provided tipset
func getParentTipSet(store *hamt.CborIpldStore, ts TipSet) (TipSet, error) {
	newTipSet := TipSet{}
	for it := ts.Parents().Iter(); !it.Complete(); it.Next() {
		var newBlk types.Block
		if err := store.Get(context.TODO(), it.Value(), &newBlk); err != nil {
			return nil, err
		}
		if err := newTipSet.AddBlock(&newBlk); err != nil {
			return nil, err
		}
	}
	return newTipSet, nil
}

// collectChainsMessagesToHeight is a helper that collects all the messages
// from block `b` down the chain to but not including its ancestor of
// height `height`.  This function returns the messages collected along with
// the tipset at the final height.
// TODO ripe for optimizing away lots of allocations
func collectChainsMessagesToHeight(store *hamt.CborIpldStore, curTipSet TipSet, height uint64) ([]*types.Message, TipSet, error) {
	var msgs []*types.Message
	h := curTipSet.Height()
	for h > height {
		for _, blk := range curTipSet {
			msgs = append(msgs, blk.Messages...)
		}
		switch curTipSet.Parents().Len() {
		case 0:
			return msgs, curTipSet, nil
		default:
			nextTipSet, err := getParentTipSet(store, curTipSet)
			if err != nil {
				return []*types.Message{}, TipSet{}, err
			}
			curTipSet = nextTipSet
			h = curTipSet.Height()
		}
	}
	return msgs, curTipSet, nil
}

// UpdateMessagePool brings the message pool into the correct state after
// we accept a new block. It removes messages from the pool that are
// found in the newly adopted chain and adds back those from the removed
// chain (if any) that do not appear in the new chain. We think
// that the right model for keeping the message pool up to date is
// to think about it like a garbage collector.
//
// TODO there is considerable functionality missing here: don't add
//      messages that have expired, respect nonce, do this efficiently,
//      etc.
func UpdateMessagePool(ctx context.Context, pool *MessagePool, store *hamt.CborIpldStore, old, new TipSet) error {
	// Strategy: walk head-of-chain pointers old and new back until they are at the same
	// height, then walk back in lockstep to find the common ancesetor.

	// If old is higher/longer than new, collect all the messages
	// from old's chain down to the height of new.
	addToPool, old, err := collectChainsMessagesToHeight(store, old, new.Height())
	if err != nil {
		return err
	}
	// If new is higher/longer than old, collect all the messages
	// from new's chain down to the height of old.
	removeFromPool, new, err := collectChainsMessagesToHeight(store, new, old.Height())
	if err != nil {
		return err
	}
	// Old and new are now at the same height. Keep walking them down a
	// tipset at a time in lockstep until they are pointing to the same
	// tipset, the common ancestor. Collect their messages to add/remove
	// along the way.
	//
	// TODO probably should limit depth here.
	for !old.Equals(new) {
		for _, blk := range old {
			addToPool = append(addToPool, blk.Messages...)
		}
		for _, blk := range new {
			removeFromPool = append(removeFromPool, blk.Messages...)
		}
		if old.Parents().Empty() || new.Parents().Empty() {
			break
		}
		old, err = getParentTipSet(store, old)
		if err != nil {
			return err
		}
		new, err = getParentTipSet(store, new)
		if err != nil {
			return err
		}
	}

	// Now actually update the pool.
	for _, m := range addToPool {
		_, err := pool.Add(m)
		if err != nil {
			return err
		}
	}
	// m.Cid() can error, so collect all the Cids before
	removeCids := make([]*cid.Cid, len(removeFromPool))
	for i, m := range removeFromPool {
		cid, err := m.Cid()
		if err != nil {
			return err
		}
		removeCids[i] = cid
	}
	for _, cid := range removeCids {
		pool.Remove(cid)
	}

	return nil
}

// OrderMessagesByNonce returns the pending messages in the
// pool ordered such that all messages with the same msg.From
// occur in Nonce order in the slice.
// TODO can be smarter here by skipping messages with gaps; see
//      ethereum's abstraction for example
// TODO order by time of receipt
func OrderMessagesByNonce(messages []*types.Message) []*types.Message {
	// TODO this could all be more efficient.
	byAddress := make(map[types.Address][]*types.Message)
	for _, m := range messages {
		byAddress[m.From] = append(byAddress[m.From], m)
	}
	messages = messages[:0]
	for _, msgs := range byAddress {
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].Nonce < msgs[j].Nonce })
		messages = append(messages, msgs...)
	}
	return messages
}

// LargestNonce returns the largest nonce used by a message from address in the pool.
// If no messages from address are found, found will be false.
func LargestNonce(pool *MessagePool, address types.Address) (largest uint64, found bool) {
	for _, m := range pool.Pending() {
		if m.From == address {
			found = true
			if m.Nonce > largest {
				largest = m.Nonce
			}
		}
	}
	return
}
