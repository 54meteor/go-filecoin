package chain

import (
	"context"
	"testing"

	"gx/ipfs/QmSkuaNgyGmV8c1L3cZNWcUxRJV6J3nsD96JVQPcWcwtyW/go-hamt-ipld"
	bstore "gx/ipfs/QmcD7SqfyQyA91TZUQ7VPRYbGarxmY7EsQewVYMuN5LNSv/go-ipfs-blockstore"

	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/state"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTotal(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	power := uint64(987654321)
	ctx, bs, _, st := requireMinerWithPower(t, power)

	actual, err := (&consensus.MarketView{}).Total(ctx, st, bs)
	require.NoError(err)

	assert.Equal(power, actual)
}

func TestMiner(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	power := uint64(1234567890)
	ctx, bs, addr, st := requireMinerWithPower(t, power)

	actual, err := (&consensus.MarketView{}).Miner(ctx, st, bs, addr)
	require.NoError(err)

	assert.Equal(power, actual)
}

func requireMinerWithPower(t *testing.T, power uint64) (context.Context, bstore.Blockstore, types.Address, state.Tree) {

	// set up genesis block with power
	ctx := context.Background()
	bootstrapPowerTable := &consensus.TestView{}
	require := require.New(t)

	pwr := types.NewBytesAmount(power)

	r := repo.NewInMemoryRepo()
	bs := bstore.NewBlockstore(r.Datastore())
	cst := hamt.NewCborStore()
	ki := types.MustGenerateKeyInfo(1, types.GenerateKeyInfoSeed())
	mockSigner := types.NewMockSigner(ki)
	testAddress := mockSigner.Addresses[0]

	testGen := th.MakeGenesisFunc(
		th.ActorAccount(testAddress, types.NewAttoFILFromFIL(10000)),
	)

	genBlk, err := testGen(cst, bs)
	require.NoError(err)
	genCid := genBlk.Cid()
	genesisTS := consensus.RequireNewTipSet(require, genBlk)
	genRoot := genBlk.StateRoot

	con := consensus.NewExpected(cst, bs, bootstrapPowerTable, genCid)
	syncer, chain, cst := initSyncTest(require, con, testGen, cst, bs, r)

	genTsas := &TipSetAndState{
		TipSet:          genesisTS,
		TipSetStateRoot: genRoot,
	}
	RequirePutTsas(ctx, require, chain, genTsas)
	err = chain.SetHead(ctx, genesisTS) // Initialize chain store with correct genesis
	require.NoError(err)
	requireHead(require, chain, genesisTS)
	requireTsAdded(require, chain, genesisTS)
	addrMine, _, _, err := CreateMinerWithPower(ctx, t, syncer, genBlk, mockSigner, uint64(0), mockSigner.Addresses[0], pwr, cst, bs, genCid)
	require.NoError(err)

	st, err := chain.LatestState(ctx)
	require.NoError(err)
	return ctx, bs, addrMine, st
}
