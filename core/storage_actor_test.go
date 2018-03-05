package core

import (
	"context"
	"math/big"
	"testing"

	cbor "gx/ipfs/QmRVSCwQtW1rjHCay9NqKXDwbtKTgDcN4iY7PrpSqfKM5D/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmZhoiN2zi5SBBBKb181dQm4QdvWAvEwbppZvKpp4gRyNY/go-hamt-ipld"

	"github.com/stretchr/testify/assert"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/types"
)

func mustConvertParams(params ...interface{}) []byte {
	vals, err := abi.ToValues(params)
	if err != nil {
		panic(err)
	}

	out, err := abi.EncodeValues(vals)
	if err != nil {
		panic(err)
	}
	return out
}

func TestStorageMarketCreateMiner(t *testing.T) {
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cst := hamt.NewCborStore()
	blk, err := InitGenesis(cst)
	assert.NoError(err)

	st, err := types.LoadStateTree(ctx, cst, blk.StateRoot)
	assert.NoError(err)

	pdata := mustConvertParams(big.NewInt(10000))
	msg := types.NewMessage(TestAccount, StorageMarketAddress, big.NewInt(100), "createMiner", pdata)
	receipt, err := ApplyMessage(ctx, st, msg)
	assert.NoError(err)

	outAddr := types.Address(receipt.Return)
	miner, err := st.GetActor(ctx, outAddr)
	assert.NoError(err)

	storageMkt, err := st.GetActor(ctx, StorageMarketAddress)
	assert.NoError(err)

	assert.Equal(big.NewInt(0), storageMkt.Balance)
	assert.Equal(big.NewInt(100), miner.Balance)

	var mstor MinerStorage
	assert.NoError(cbor.DecodeInto(miner.ReadStorage(), &mstor))

	assert.Equal(mstor.Collateral, big.NewInt(100))
	assert.Equal(mstor.PledgeBytes, big.NewInt(10000))
}

func TestStorageMarketCreateMinerPledgeTooLow(t *testing.T) {
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cst := hamt.NewCborStore()
	blk, err := InitGenesis(cst)
	assert.NoError(err)

	st, err := types.LoadStateTree(ctx, cst, blk.StateRoot)
	assert.NoError(err)

	pdata := mustConvertParams(big.NewInt(50))
	msg := types.NewMessage(TestAccount, StorageMarketAddress, big.NewInt(100), "createMiner", pdata)
	_, err = ApplyMessage(ctx, st, msg)
	assert.Equal(ErrPledgeTooLow, errors.Cause(err))
}

func TestStorageMarketAddBid(t *testing.T) {
	assert := assert.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cst := hamt.NewCborStore()
	blk, err := InitGenesis(cst)
	assert.NoError(err)

	st, err := types.LoadStateTree(ctx, cst, blk.StateRoot)
	assert.NoError(err)

	// create a bid
	pdata := mustConvertParams(big.NewInt(20), big.NewInt(30))
	msg := types.NewMessage(TestAccount, StorageMarketAddress, big.NewInt(600), "addBid", pdata)
	receipt, err := ApplyMessage(ctx, st, msg)
	assert.NoError(err)

	assert.Equal(uint8(0), receipt.ExitCode)
	assert.Equal(big.NewInt(0), big.NewInt(0).SetBytes(receipt.Return))

	// create another bid
	pdata = mustConvertParams(big.NewInt(15), big.NewInt(80))
	msg = types.NewMessage(TestAccount, StorageMarketAddress, big.NewInt(1200), "addBid", pdata)
	receipt, err = ApplyMessage(ctx, st, msg)
	assert.NoError(err)

	assert.Equal(uint8(0), receipt.ExitCode)
	assert.Equal(big.NewInt(1), big.NewInt(0).SetBytes(receipt.Return))

	// try to create a bid, but send wrong value
	pdata = mustConvertParams(big.NewInt(90), big.NewInt(100))
	msg = types.NewMessage(TestAccount, StorageMarketAddress, big.NewInt(600), "addBid", pdata)
	_, err = ApplyMessage(ctx, st, msg) // TODO: apply message shouldnt error
	// here. This invocation should be valid, it should just return a non-zero
	// exit code and an error message in the receipt.
	assert.EqualError(err, "failed to send message: must send price * size funds to create bid")
}
