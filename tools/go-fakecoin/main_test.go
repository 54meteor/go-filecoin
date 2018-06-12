package main

import (
	"context"
	"os/exec"
	"testing"

	"gx/ipfs/QmXRKBQA4wXP7xWbFiZsR1GP4HV6wMDQ1aWFxZZ4uBcPX9/go-datastore"

	"fmt"
	"os"
	"path/filepath"

	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/actor/builtin/storagemarket"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/state"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddFakeChain(t *testing.T) {
	assert := assert.New(t)

	var length = 9
	var gbbCount, pbCount int
	ctx := context.Background()

	getBestBlock := func() *types.Block {
		gbbCount++
		return new(types.Block)
	}
	processBlock := func(context context.Context, block *types.Block) (core.BlockProcessResult, error) {
		pbCount++
		return 0, nil
	}
	fake(ctx, length, getBestBlock, processBlock)
	assert.Equal(1, gbbCount)
	assert.Equal(length, pbCount)
}

func TestAddActors(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	ctx := context.Background()

	ds := datastore.NewMapDatastore()
	cm, _ := getChainManager(ds)

	err := cm.Genesis(ctx, core.InitGenesis)
	require.NoError(err)

	st, cst, cm, bb, err := getStateTree(ctx, ds)
	require.NoError(err)

	_, allActors := state.GetAllActors(st)
	initialActors := len(allActors)

	err = fakeActors(ctx, cst, cm, bb)
	assert.NoError(err)

	st, _, _, _, err = getStateTree(ctx, ds)
	require.NoError(err)

	_, allActors = state.GetAllActors(st)
	assert.Equal(initialActors+2, len(allActors), "add a account and miner actors")

	sma, err := st.GetActor(ctx, address.StorageMarketAddress)
	require.NoError(err)

	var storageMkt storagemarket.Storage
	err = actor.UnmarshalStorage(sma.ReadStorage(), &storageMkt)
	require.NoError(err)

	assert.Equal(1, len(storageMkt.Miners))
	assert.Equal(1, len(storageMkt.Orderbook.Asks))
	assert.Equal(1, len(storageMkt.Orderbook.Bids))
	assert.Equal(1, len(storageMkt.Filemap.Deals))
}

func GetFakecoinBinary() (string, error) {
	bin := filepath.FromSlash(fmt.Sprintf("%s/src/github.com/filecoin-project/go-filecoin/tools/go-fakecoin/go-fakecoin", os.Getenv("GOPATH")))
	_, err := os.Stat(bin)
	if err == nil {
		return bin, nil
	}

	if os.IsNotExist(err) {
		return "", fmt.Errorf("You are missing the fakecoin binary...try building, searched in '%s'", bin)
	}

	return "", err
}

var testRepoPath = filepath.FromSlash("/tmp/fakecoin/")

func TestCommandsSucceed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fbin, err := th.GetFilecoinBinary()
	require.NoError(err)

	os.RemoveAll(testRepoPath)       // go-filecoin init will fail if repo exists.
	defer os.RemoveAll(testRepoPath) // clean up when we're done.

	exec.Command(fbin, "init", "--repodir", testRepoPath).Run()
	require.NoError(err)

	bin, err := GetFakecoinBinary()
	require.NoError(err)

	// 'go-fakecoin fake' completes without error.
	cmdFake := exec.Command(bin, "fake", "-repodir", testRepoPath)
	err = cmdFake.Run()
	assert.NoError(err)

	// 'go-fakecoin actors' completes without error.
	cmdActors := exec.Command(bin, "actors", "-repodir", testRepoPath)
	err = cmdActors.Run()
	assert.NoError(err)
}
