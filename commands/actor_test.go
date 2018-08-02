package commands

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"

	"github.com/filecoin-project/go-filecoin/actor/builtin/account"
	"github.com/filecoin-project/go-filecoin/actor/builtin/miner"
	"github.com/filecoin-project/go-filecoin/actor/builtin/storagemarket"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/core/node"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
)

func TestActorLs(t *testing.T) {
	t.Parallel()
	getActorsNoOp := func(st state.Tree) ([]string, []*types.Actor) {
		return nil, nil
	}

	t.Run("returns an error if no best block", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)
		ctx := context.Background()
		emitter := NewMockEmitter(func(v interface{}) error {
			return nil
		})
		nd := node.MakeNodesUnstarted(t, 1, true, true)[0]
		tcm := (*core.ChainManagerForTest)(nd.ChainMgr)
		nd.ChainMgr = tcm

		err := runActorLs(ctx, emitter.emit, nd, getActorsNoOp)
		require.Error(err)
	})

	t.Run("returns an error if heaviest tipset is nil", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)
		ctx := context.Background()
		emitter := NewMockEmitter(func(v interface{}) error {
			return nil
		})
		nd := node.MakeNodesUnstarted(t, 1, true, true)[0]
		// TODO fix #543: Improve UX for multiblock tipset
		nd.ChainMgr.GetHeaviestTipSet = func() core.TipSet {
			return nil
		}

		err := runActorLs(ctx, emitter.emit, nd, nil)
		require.Error(err)
	})

	t.Run("returns an error if LoadStateTree returns an error", func(t *testing.T) {
		// TOO HARD TO TEST WITHOUT SPECIFIC DEPENDENCY INJECTION
	})

	t.Run("emits json object for each actor in state", func(t *testing.T) {
		t.Parallel()
		assert := assert.New(t)
		require := require.New(t)
		ctx := context.Background()
		var actorViews []*actorView

		emitter := NewMockEmitter(func(v interface{}) error {
			actorViews = append(actorViews, v.(*actorView))
			return nil
		})
		nd := node.MakeNodesUnstarted(t, 1, true, true)[0]
		st := state.NewEmptyStateTree(nd.CborStore)
		root, err := st.Flush(ctx)
		require.NoError(err)
		b1 := &types.Block{StateRoot: root}
		var chainMgrForTest *core.ChainManagerForTest // nolint: gosimple, megacheck
		chainMgrForTest = nd.ChainMgr
		chainMgrForTest.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b1))
		assert.NoError(nd.Start())
		tokenAmount := types.NewAttoFILFromFIL(100)

		getActors := func(state.Tree) ([]string, []*types.Actor) {
			actor1, _ := account.NewActor(tokenAmount)
			actor2, _ := storagemarket.NewActor()
			address, _ := types.NewAddressFromString("address")
			actor3, _ := miner.NewActor(address, []byte{}, types.NewBytesAmount(23), core.RequireRandomPeerID(), types.NewAttoFILFromFIL(43))
			actor4 := types.NewActorWithMemory(types.NewCidForTestGetter()(), types.NewAttoFILFromFIL(21), nil)
			return []string{"address1", "address2", "address3", "address4"}, []*types.Actor{actor1, actor2, actor3, actor4}
		}

		err = runActorLs(ctx, emitter.emit, nd, getActors)
		require.NoError(err)

		assert.Equal(4, len(actorViews))
		assert.Equal("AccountActor", actorViews[0].ActorType)
		assert.True(tokenAmount.Equal(actorViews[0].Balance))
		assert.Equal("StoragemarketActor", actorViews[1].ActorType)
		assert.Equal("MinerActor", actorViews[2].ActorType)
		assert.Equal("UnknownActor", actorViews[3].ActorType)
	})

	validateActorView := func(a *actorView, sl gojsonschema.JSONLoader) (*gojsonschema.Result, error) {
		jsonBytes, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		jsonLoader := gojsonschema.NewBytesLoader(jsonBytes)

		return gojsonschema.Validate(sl, jsonLoader)
	}

	assertSchemaValid := func(t *testing.T, a *actorView, sl gojsonschema.JSONLoader) {
		assert := assert.New(t)
		require := require.New(t)

		result, err := validateActorView(a, sl)
		require.NoError(err)

		assert.True(result.Valid())
		for _, desc := range result.Errors() {
			t.Errorf("- %s\n", desc)
		}
	}

	t.Run("Emitted AccountActor JSON conforms to schema", func(t *testing.T) {
		t.Parallel()

		wd, _ := os.Getwd()
		schemaLoader := gojsonschema.NewReferenceLoader("file://" + wd + "/schema/actor_ls.schema.json")

		actor, _ := account.NewActor(types.NewAttoFILFromFIL(100))
		a := makeActorView(actor, "address", &account.Actor{})

		assertSchemaValid(t, a, schemaLoader)

		actor, _ = storagemarket.NewActor()
		a = makeActorView(actor, "address", &storagemarket.Actor{})

		assertSchemaValid(t, a, schemaLoader)

		addr, _ := types.NewAddressFromString("minerAddress")
		actor, _ = miner.NewActor(addr, []byte{}, types.NewBytesAmount(50000), core.RequireRandomPeerID(), types.NewAttoFILFromFIL(200))
		a = makeActorView(actor, "address", &miner.Actor{})

		assertSchemaValid(t, a, schemaLoader)
	})
}

func TestPresentExports(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	e := (&storagemarket.Actor{}).Exports()
	r := presentExports(e)

	for name, sig := range r {
		s, ok := e[name]
		assert.True(ok)

		for i, x := range sig.Params {
			assert.Equal(s.Params[i].String(), x)
		}
		for i, x := range sig.Return {
			assert.Equal(s.Return[i].String(), x)
		}
	}
}
