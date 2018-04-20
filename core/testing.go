package core

import (
	"context"

	cbor "gx/ipfs/QmRVSCwQtW1rjHCay9NqKXDwbtKTgDcN4iY7PrpSqfKM5D/go-ipld-cbor"
	cid "gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	hamt "gx/ipfs/QmdtiofXbibTe6Day9ii5zjBZpSRm8vhfoerrNuY3sAQ7e/go-hamt-ipld"

	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/require"
)

func init() {
	cbor.RegisterCborType(FakeActorStorage{})
}

// MustPut stores the thingy in the store or panics if it cannot.
func MustPut(store *hamt.CborIpldStore, thingy interface{}) *cid.Cid {
	cid, err := store.Put(context.Background(), thingy)
	if err != nil {
		panic(err)
	}
	return cid
}

// MustAdd adds the given messages to the messagepool or panics if it
// cannot.
func MustAdd(ctx context.Context, p *MessagePool, msgs ...*types.Message) {
	for _, m := range msgs {
		if _, err := p.Add(ctx, m); err != nil {
			panic(err)
		}
	}
}

// mkChild creates a new block with parent, blk, and supplied nonce.
func mkChild(blk *types.Block, nonce uint64) *types.Block {
	return &types.Block{
		Parent:          blk.Cid(),
		Height:          blk.Height + 1,
		Nonce:           nonce,
		StateRoot:       blk.StateRoot,
		Messages:        []*types.Message{},
		MessageReceipts: []*types.MessageReceipt{},
	}
}

// AddChain creates and processes new, empty chain of length, beginning from blk.
func AddChain(ctx context.Context, processNewBlock NewBlockProcessor, blk *types.Block, length int) (*types.Block, error) {
	l := uint64(length)
	for i := uint64(0); i < l; i++ {
		blk = mkChild(blk, i)
		_, err := processNewBlock(ctx, blk)
		if err != nil {
			return nil, err
		}
	}
	return blk, nil
}

// NewChainWithMessages creates a chain of blocks containing the given messages
// and stores them in the given store. Note the msg arguments are slices of
// messages -- each slice goes into a successive block.
func NewChainWithMessages(store *hamt.CborIpldStore, root *types.Block, msgSets ...[]*types.Message) []*types.Block {
	parent := root
	blocks := []*types.Block{}
	if parent != nil {
		MustPut(store, parent)
		blocks = append(blocks, parent)
	}

	for _, msgs := range msgSets {
		child := &types.Block{Messages: msgs}
		if parent != nil {
			child.Parent = parent.Cid()
			child.Height = parent.Height + 1
		}
		MustPut(store, child)
		blocks = append(blocks, child)
		parent = child
	}

	return blocks
}

// RequireMakeStateTree takes a map of addresses to actors and stores them on
// the state tree, requiring that all its steps succeed.
func RequireMakeStateTree(require *require.Assertions, cst *hamt.CborIpldStore, acts map[types.Address]*types.Actor) (*cid.Cid, types.StateTree) {
	ctx := context.Background()
	t := types.NewEmptyStateTree(cst)

	for addr, act := range acts {
		err := t.SetActor(ctx, addr, act)
		require.NoError(err)
	}

	c, err := t.Flush(ctx)
	require.NoError(err)

	return c, t
}

// RequireNewAccountActor creates a new account actor with the given starting
// value and requires that its steps succeed.
func RequireNewAccountActor(require *require.Assertions, value *types.TokenAmount) *types.Actor {
	act, err := NewAccountActor(value)
	require.NoError(err)
	return act
}

// RequireNewFakeActor instantiates and returns a new fake actor and requires
// that its steps succeed.
func RequireNewFakeActor(require *require.Assertions, codeCid *cid.Cid) *types.Actor {
	storageBytes, err := MarshalStorage(&FakeActorStorage{})
	require.NoError(err)
	return types.NewActorWithMemory(codeCid, types.NewTokenAmount(100), storageBytes)
}

// FakeActorStorage is storage for our fake actor. It contains a single
// bit that is set when the actor's methods are invoked.
type FakeActorStorage struct{ Changed bool }

// FakeActor is a fake actor for use in tests.
type FakeActor struct{}

var _ ExecutableActor = (*FakeActor)(nil)

var fakeActorExports = Exports{
	"returnRevertError": &FunctionSignature{
		Params: nil,
		Return: nil,
	},
	"goodCall": &FunctionSignature{
		Params: nil,
		Return: nil,
	},
}

// Exports returns the list of fake actor exported functions.
func (ma *FakeActor) Exports() Exports {
	return fakeActorExports
}

// ReturnRevertError sets a bit inside fakeActor's storage and returns a
// revert error.
func (ma *FakeActor) ReturnRevertError(ctx *VMContext) (uint8, error) {
	fastore := &FakeActorStorage{}
	_, err := WithStorage(ctx, fastore, func() (interface{}, error) {
		fastore.Changed = true
		return nil, nil
	})
	if err != nil {
		panic(err.Error())
	}
	return 1, newRevertError("boom")
}

// GoodCall sets a bit inside fakeActor's storage.
func (ma *FakeActor) GoodCall(ctx *VMContext) (uint8, error) {
	fastore := &FakeActorStorage{}
	_, err := WithStorage(ctx, fastore, func() (interface{}, error) {
		fastore.Changed = true
		return nil, nil
	})
	if err != nil {
		panic(err.Error())
	}
	return 0, nil
}

// NewStorage returns an empty FakeActorStorage struct
func (ma *FakeActor) NewStorage() interface{} {
	return &FakeActorStorage{}
}
