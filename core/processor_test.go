package core

import (
	"context"
	"testing"

	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	hamt "gx/ipfs/QmdtiofXbibTe6Day9ii5zjBZpSRm8vhfoerrNuY3sAQ7e/go-hamt-ipld"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireMakeStateTree(require *require.Assertions, cst *hamt.CborIpldStore, acts map[types.Address]*types.Actor) (*cid.Cid, types.StateTree) {
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

func TestProcessBlockSuccess(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	addr1, addr2 := newAddress(), newAddress()
	act1 := RequireNewAccountActor(require, types.NewTokenAmount(10000))
	stCid, st := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
	})
	msg := types.NewMessage(addr1, addr2, 0, types.NewTokenAmount(550), "", nil)
	blk := &types.Block{
		Height:    20,
		StateRoot: stCid,
		Messages:  []*types.Message{msg},
	}
	receipts, err := ProcessBlock(ctx, blk, st)
	assert.NoError(err)
	assert.Len(receipts, 1)

	gotStCid, err := st.Flush(ctx)
	assert.NoError(err)
	expAct1, expAct2 := RequireNewAccountActor(require, types.NewTokenAmount(10000-550)), RequireNewAccountActor(require, types.NewTokenAmount(550))
	expAct1.IncNonce()
	expStCid, _ := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: expAct1,
		addr2: expAct2,
	})
	assert.True(expStCid.Equals(gotStCid))
}

func TestProcessBlockVMErrors(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	// Install the fake actor so we can execute it.
	fakeActorCodeCid := types.NewCidForTestGetter()()
	BuiltinActors[fakeActorCodeCid.KeyString()] = &FakeActor{}
	defer func() {
		delete(BuiltinActors, fakeActorCodeCid.KeyString())
	}()

	// Stick two fake actors in the state tree so they can talk.
	addr1, addr2 := newAddress(), newAddress()
	act1, act2 := RequireNewFakeActor(require, fakeActorCodeCid), RequireNewFakeActor(require, fakeActorCodeCid)
	stCid, st := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
		addr2: act2,
	})
	msg := types.NewMessage(addr1, addr2, 0, nil, "returnRevertError", nil)
	blk := &types.Block{
		Height:    20,
		StateRoot: stCid,
		Messages:  []*types.Message{msg},
	}

	// The "foo" message will cause a vm error and
	// we're going to check four things...
	receipts, err := ProcessBlock(ctx, blk, st)

	// 1. That a VM error is not a message failure (err).
	assert.NoError(err)

	// 2. That the VM error is faithfully recorded.
	assert.Len(receipts, 1)
	assert.Contains(string(receipts[0].Return[:]), "boom")

	// 3 & 4. That on VM error the state is rolled back and nonce is inc'd.
	expectedAct1, expectedAct2 := RequireNewFakeActor(require, fakeActorCodeCid), RequireNewFakeActor(require, fakeActorCodeCid)
	expectedAct1.IncNonce()
	expectedStCid, _ := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: expectedAct1,
		addr2: expectedAct2,
	})
	gotStCid, err := st.Flush(ctx)
	assert.NoError(err)
	assert.True(expectedStCid.Equals(gotStCid))
}

func TestProcessBlockParamsLengthError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	addr2, addr1 := newAddress(), newAddress()
	act1 := RequireNewAccountActor(require, types.NewTokenAmount(1000))
	act2 := RequireNewMinerActor(require, addr1, types.NewBytesAmount(10000), types.NewTokenAmount(10000))
	_, st := requireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
		addr2: act2,
	})
	params, err := abi.ToValues([]interface{}{"param"})
	assert.NoError(err)
	badParams, err := abi.EncodeValues(params)
	assert.NoError(err)
	msg := types.NewMessage(addr1, addr2, 0, types.NewTokenAmount(550), "addAsk", badParams)

	r, err := ApplyMessage(ctx, st, msg)
	assert.NoError(err) // No error means definitely no fault error, which is what we're especially testing here.

	assert.Contains(string(r.Return[:]), "invalid params: expected 2 parameters, but got 1")
}

func TestProcessBlockParamsError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	addr2, addr1 := newAddress(), newAddress()
	act1 := RequireNewAccountActor(require, types.NewTokenAmount(1000))
	act2 := RequireNewMinerActor(require, addr1, types.NewBytesAmount(10000), types.NewTokenAmount(10000))
	_, st := requireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
		addr2: act2,
	})
	badParams := []byte{1, 2, 3, 4, 5}
	msg := types.NewMessage(addr1, addr2, 0, types.NewTokenAmount(550), "addAsk", badParams)

	r, err := ApplyMessage(ctx, st, msg)
	assert.NoError(err) // No error means definitely no fault error, which is what we're especially testing here.

	assert.Contains(string(r.Return[:]), "invalid params: malformed stream")
}

func TestProcessBlockNonceTooLow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	addr2, addr1 := newAddress(), newAddress()
	act1 := RequireNewAccountActor(require, types.NewTokenAmount(1000))
	act1.Nonce = 5
	act2 := RequireNewMinerActor(require, addr1, types.NewBytesAmount(10000), types.NewTokenAmount(10000))
	_, st := requireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
		addr2: act2,
	})
	msg := types.NewMessage(addr1, addr2, 0, types.NewTokenAmount(550), "", []byte{})

	_, err := ApplyMessage(ctx, st, msg)
	assert.Error(err)
	assert.Equal(err.(*applyErrorPermanent).err, errNonceTooLow)
}

func TestProcessBlockNonceTooHigh(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	addr2, addr1 := newAddress(), newAddress()
	act1 := RequireNewAccountActor(require, types.NewTokenAmount(1000))
	act2 := RequireNewMinerActor(require, addr1, types.NewBytesAmount(10000), types.NewTokenAmount(10000))
	_, st := requireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr1: act1,
		addr2: act2,
	})
	msg := types.NewMessage(addr1, addr2, 5, types.NewTokenAmount(550), "", []byte{})

	_, err := ApplyMessage(ctx, st, msg)
	assert.Error(err)
	assert.Equal(err.(*applyErrorTemporary).err, errNonceTooHigh)
}

// TODO fritz add more test cases that cover the intent expressed
// in ApplyMessage's comments.

func TestNestedSendBalance(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	newAddress := types.NewAddressForTestGetter()
	ctx := context.Background()
	cst := hamt.NewCborStore()

	// Install the fake actor so we can execute it.
	fakeActorCodeCid := types.NewCidForTestGetter()()
	BuiltinActors[fakeActorCodeCid.KeyString()] = &FakeActor{}
	defer func() {
		delete(BuiltinActors, fakeActorCodeCid.KeyString())
	}()

	addr0, addr1, addr2 := newAddress(), newAddress(), newAddress()
	act0 := RequireNewAccountActor(require, types.NewTokenAmount(101))
	act1 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(102))
	act2 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(0))

	_, st := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr0: act0,
		addr1: act1,
		addr2: act2,
	})

	// send 100 from addr1 -> addr2, by sending a message from addr0 to addr1
	params1, err := abi.ToEncodedValues(addr2)
	assert.NoError(err)
	msg1 := types.NewMessage(addr0, addr1, 0, nil, "nestedBalance", params1)

	_, err = attemptApplyMessage(ctx, st, msg1)
	assert.NoError(err)

	gotStCid, err := st.Flush(ctx)
	assert.NoError(err)

	expAct0 := RequireNewAccountActor(require, types.NewTokenAmount(101))
	expAct1 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(2))
	expAct2 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(100))

	expStCid, _ := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr0: expAct0,
		addr1: expAct1,
		addr2: expAct2,
	})

	assert.True(expStCid.Equals(gotStCid))
}
