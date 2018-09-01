package vm

import (
	"context"
	"testing"

	"gx/ipfs/QmQZadYTDF4ud9DdK85PH2vReJRzUM9YfVW4ReB1q2m51p/go-hamt-ipld"
	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"
	"gx/ipfs/QmVG5gxteQNEMhrS8prJSmU2C9rebtFuTd3SYZ5kE3YZ5k/go-datastore"
	xerrors "gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmcmpX42gtDv1fz24kau4wjS9hfwWj5VexWBKgGnWzsyag/go-ipfs-blockstore"

	"github.com/stretchr/testify/assert"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/actor/builtin/account"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/exec"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"
	"github.com/stretchr/testify/require"
)

func TestVMContextStorage(t *testing.T) {
	assert := assert.New(t)
	addrGetter := address.NewForTestGetter()
	ctx := context.Background()

	cst := hamt.NewCborStore()
	st := state.NewEmptyStateTree(cst)
	cstate := state.NewCachedStateTree(st)

	bs := blockstore.NewBlockstore(datastore.NewMapDatastore())
	vms := NewStorageMap(bs)

	toActor, err := account.NewActor(nil)
	assert.NoError(err)
	toAddr := addrGetter()

	assert.NoError(st.SetActor(ctx, toAddr, toActor))
	msg := chain.NewMessage(addrGetter(), toAddr, 0, nil, "hello", nil)

	to, err := cstate.GetActor(ctx, toAddr)
	assert.NoError(err)
	vmCtx := NewVMContext(nil, to, msg, cstate, vms, chain.NewBlockHeight(0))

	node, err := cbor.WrapObject([]byte("hello"), types.DefaultHashFunction, -1)
	assert.NoError(err)

	assert.NoError(vmCtx.WriteStorage(node.RawData()))
	assert.NoError(cstate.Commit(ctx))

	// make sure we can read it back
	toActorBack, err := st.GetActor(ctx, toAddr)
	assert.NoError(err)

	storage, err := NewVMContext(nil, toActorBack, msg, cstate, vms, chain.NewBlockHeight(0)).ReadStorage()
	assert.NoError(err)
	assert.Equal(storage, node.RawData())
}

func TestVMContextSendFailures(t *testing.T) {
	actor1 := actor.NewActor(nil, types.NewAttoFILFromFIL(100))
	actor2 := actor.NewActor(nil, types.NewAttoFILFromFIL(50))
	newMsg := chain.NewMessageForTestGetter()
	newAddress := address.NewForTestGetter()

	mockStateTree := state.MockStateTree{
		BuiltinActors: map[string]exec.ExecutableActor{},
	}
	fakeActorCid := chain.NewCidForTestGetter()()
	mockStateTree.BuiltinActors[fakeActorCid.KeyString()] = &actor.FakeActor{}
	tree := state.NewCachedStateTree(&mockStateTree)
	bs := blockstore.NewBlockstore(datastore.NewMapDatastore())
	vms := NewStorageMap(bs)

	t.Run("failure to convert to ABI values results in fault error", func(t *testing.T) {
		assert := assert.New(t)

		var calls []string
		deps := &deps{
			ToValues: func(_ []interface{}) ([]*abi.Value, error) {
				calls = append(calls, "ToValues")
				return nil, xerrors.New("error")
			},
		}

		ctx := NewVMContext(actor1, actor2, newMsg(), tree, vms, chain.NewBlockHeight(0))
		ctx.deps = deps

		_, code, err := ctx.Send(newAddress(), "foo", nil, []interface{}{})

		assert.Error(err)
		assert.Equal(1, int(code))
		assert.True(errors.IsFault(err))
		assert.Equal([]string{"ToValues"}, calls)
	})

	t.Run("failure to encode ABI values to byte slice results in revert error", func(t *testing.T) {
		assert := assert.New(t)

		var calls []string
		deps := &deps{
			EncodeValues: func(_ []*abi.Value) ([]byte, error) {
				calls = append(calls, "EncodeValues")
				return nil, xerrors.New("error")
			},
			ToValues: func(_ []interface{}) ([]*abi.Value, error) {
				calls = append(calls, "ToValues")
				return nil, nil
			},
		}

		ctx := NewVMContext(actor1, actor2, newMsg(), tree, vms, chain.NewBlockHeight(0))
		ctx.deps = deps

		_, code, err := ctx.Send(newAddress(), "foo", nil, []interface{}{})

		assert.Error(err)
		assert.Equal(1, int(code))
		assert.True(errors.ShouldRevert(err))
		assert.Equal([]string{"ToValues", "EncodeValues"}, calls)
	})

	t.Run("refuse to send a message with identical from/to", func(t *testing.T) {
		assert := assert.New(t)

		to := newAddress()

		msg := newMsg()
		msg.To = to

		var calls []string
		deps := &deps{
			EncodeValues: func(_ []*abi.Value) ([]byte, error) {
				calls = append(calls, "EncodeValues")
				return nil, nil
			},
			ToValues: func(_ []interface{}) ([]*abi.Value, error) {
				calls = append(calls, "ToValues")
				return nil, nil
			},
		}

		ctx := NewVMContext(actor1, actor2, msg, tree, vms, chain.NewBlockHeight(0))
		ctx.deps = deps

		_, code, err := ctx.Send(to, "foo", nil, []interface{}{})

		assert.Error(err)
		assert.Equal(1, int(code))
		assert.True(errors.IsFault(err))
		assert.Equal([]string{"ToValues", "EncodeValues"}, calls)
	})

	t.Run("returns a fault error if unable to create or find a recipient actor", func(t *testing.T) {
		assert := assert.New(t)

		var calls []string
		deps := &deps{
			EncodeValues: func(_ []*abi.Value) ([]byte, error) {
				calls = append(calls, "EncodeValues")
				return nil, nil
			},
			GetOrCreateActor: func(_ context.Context, _ address.Address, _ func() (*actor.Actor, error)) (*actor.Actor, error) {

				calls = append(calls, "GetOrCreateActor")
				return nil, xerrors.New("error")
			},
			ToValues: func(_ []interface{}) ([]*abi.Value, error) {
				calls = append(calls, "ToValues")
				return nil, nil
			},
		}

		ctx := NewVMContext(actor1, actor2, newMsg(), tree, vms, chain.NewBlockHeight(0))
		ctx.deps = deps

		_, code, err := ctx.Send(newAddress(), "foo", nil, []interface{}{})

		assert.Error(err)
		assert.Equal(1, int(code))
		assert.True(errors.IsFault(err))
		assert.Equal([]string{"ToValues", "EncodeValues", "GetOrCreateActor"}, calls)
	})

	t.Run("propagates any error returned from Send", func(t *testing.T) {
		assert := assert.New(t)

		expectedVMSendErr := xerrors.New("error")

		var calls []string
		deps := &deps{
			EncodeValues: func(_ []*abi.Value) ([]byte, error) {
				calls = append(calls, "EncodeValues")
				return nil, nil
			},
			GetOrCreateActor: func(_ context.Context, _ address.Address, f func() (*actor.Actor, error)) (*actor.Actor, error) {
				calls = append(calls, "GetOrCreateActor")
				return f()
			},
			Send: func(ctx context.Context, vmCtx *Context) ([][]byte, uint8, error) {
				calls = append(calls, "Send")
				return nil, 123, expectedVMSendErr
			},
			ToValues: func(_ []interface{}) ([]*abi.Value, error) {
				calls = append(calls, "ToValues")
				return nil, nil
			},
		}

		ctx := NewVMContext(actor1, actor2, newMsg(), tree, vms, chain.NewBlockHeight(0))
		ctx.deps = deps

		_, code, err := ctx.Send(newAddress(), "foo", nil, []interface{}{})

		assert.Error(err)
		assert.Equal(123, int(code))
		assert.Equal(expectedVMSendErr, err)
		assert.Equal([]string{"ToValues", "EncodeValues", "GetOrCreateActor", "Send"}, calls)
	})

	t.Run("creates new actor from cid", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		ctx := context.Background()
		vmctx := NewVMContext(actor1, actor2, newMsg(), tree, vms, chain.NewBlockHeight(0))
		addr, err := vmctx.AddressForNewActor()

		require.NoError(err)

		params := &actor.FakeActorStorage{}
		err = vmctx.CreateNewActor(addr, fakeActorCid, params)
		require.NoError(err)

		act, err := tree.GetActor(ctx, addr)
		require.NoError(err)

		assert.Equal(fakeActorCid, act.Code)
		actorStorage := vms.NewStorage(addr, act)
		chunk, err := actorStorage.Get(act.Head)
		require.NoError(err)

		assert.True(len(chunk) > 0)
	})

}

func TestVMContextIsAccountActor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	bs := blockstore.NewBlockstore(datastore.NewMapDatastore())
	vms := NewStorageMap(bs)

	accountActor, err := account.NewActor(types.NewAttoFILFromFIL(1000))
	require.NoError(err)
	ctx := NewVMContext(accountActor, nil, nil, nil, vms, nil)
	assert.True(ctx.IsFromAccountActor())

	nonAccountActor := actor.NewActor(chain.NewCidForTestGetter()(), types.NewAttoFILFromFIL(1000))
	ctx = NewVMContext(nonAccountActor, nil, nil, nil, vms, nil)
	assert.False(ctx.IsFromAccountActor())
}
