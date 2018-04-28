package core

import (
	"context"
	"fmt"
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
	assert.Contains(receipts[0].Error, "boom")

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

	assert.Contains(r.Error, "invalid params: expected 2 parameters, but got 1")
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

	assert.Contains(r.Error, "invalid params: malformed stream")
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
	msg1 := types.NewMessage(addr0, addr1, 0, nil, "sendTokens", params1)

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

func TestReentrantTransferDoesntAllowMultiSpending(t *testing.T) {
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

	// This checks for re-entrancy problems where an actor's state
	// isn't reloaded after calling Send(). It needs to be reloaded
	// after send because a downstream callee could've called back
	// into the actor thus changing its state (eg, its balance).
	//
	// Here's how this works:
	//  - addr0 is just a trigger because we need to be in a method
	//    to do the trick. add0 calls AttemptMultiSpend on addr1.
	//  - addr1 has 100 tokens and will triplespend to addr2
	//  - add2 has 0 tokens.
	//  - addr1 sends callSendTokens to addr2, which calls back
	//    into addr1, which calls back into addr2 this time transferring
	//    100 tokens.
	//  - we let this call unroll. When we're back in addr1 we do it
	//    again, transferring another 100 tokens. We could keep doing
	//    that.
	//  - we let it unroll and are back in addr1. We do a direct transfer
	//    of 100 more tokens.
	//  - add2 has 300 tokens :sadface:

	addr0, addr1, addr2 := newAddress(), newAddress(), newAddress()
	act0 := RequireNewAccountActor(require, types.NewTokenAmount(0))
	act1 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(100))
	act2 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(0))

	_, st := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr0: act0,
		addr1: act1,
		addr2: act2,
	})

	// addr1 will attempt to triple spend to addr2 (params are self, target)
	params, err := abi.ToEncodedValues(addr1, addr2)
	assert.NoError(err)
	msg := types.NewMessage(addr0, addr1, 0, types.ZeroToken, "attemptMultiSpend", params)
	_, err = attemptApplyMessage(ctx, st, msg)
	assert.NoError(err)
	gotStCid, err := st.Flush(ctx)
	assert.NoError(err)

	expAct0 := RequireNewAccountActor(require, types.NewTokenAmount(0))
	expAct1 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(0))
	expAct2 := RequireNewFakeActorWithTokens(require, fakeActorCodeCid, types.NewTokenAmount(100))

	expStCid, _ := RequireMakeStateTree(require, cst, map[types.Address]*types.Actor{
		addr0: expAct0,
		addr1: expAct1,
		addr2: expAct2,
	})

	// If FakeActor.AttemptDoubleSpend succeeds in double spending this will fail with
	// addr2 having 300 (instead of 100).
	assert.True(expStCid.Equals(gotStCid), "State trees differ: %s", diffStateTrees(require, cst, expStCid, gotStCid))
}

func diffStateTrees(require *require.Assertions, store *hamt.CborIpldStore, st1Cid, st2Cid *cid.Cid) []string {
	addrs1, actors1, err := types.GetAllActorsFromStore(context.Background(), store, st1Cid)
	require.NoError(err)
	addrs2, actors2, err := types.GetAllActorsFromStore(context.Background(), store, st2Cid)
	require.NoError(err)

	var diffs []string
	for i, addr := range addrs1 {
		a := actorForAddress(addr, addrs2, actors2)
		if a == nil {
			// TODO if we to print purty actor output we should use actorView from the `actor ls`
			diffs = append(diffs, fmt.Sprintf("Address %s is in tree1 but not in tree2: %+v", addr, actors1[i]))
			continue
		}
		cid1, err := actors1[i].Cid()
		require.NoError(err)
		cid2, err := actors2[i].Cid()
		require.NoError(err)
		if !cid1.Equals(cid2) {
			diffs = append(diffs, fmt.Sprintf("Address %s is different: tree1: %+v, tree2: %+v", addr, actors1[i], actors2[i]))
		}
	}

	for i, addr := range addrs2 {
		a := actorForAddress(addr, addrs1, actors1)
		if a == nil {
			diffs = append(diffs, fmt.Sprintf("Address %s is in tree2 but not in tree1: %+v", addr, actors2[i]))
		}
	}

	return diffs
}

func actorForAddress(addr string, addrs []string, actors []*types.Actor) *types.Actor {
	for i, _ := range addrs {
		if addrs[i] == addr {
			return actors[i]
		}
	}
	return nil
}
