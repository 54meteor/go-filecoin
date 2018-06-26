package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"

	"gx/ipfs/QmWM5HhdG5ZQNyHQ5XhMdGmV9CvLpFynQfGpTxN2MEM7Lc/go-ipfs-exchange-offline"
	"gx/ipfs/QmaG4DZ4JaqEfvPWt5nPPgoTzhc1tr1T3f4Nu9Jpdm8ymY/go-ipfs-blockstore"
	"gx/ipfs/QmcYBp5EDnJKfVN63F71rDTksvEf1cfijwCTWtw6bPG58T/go-hamt-ipld"

	bserv "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/blockservice"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/mining"
	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
)

var length int
var repodir string

func init() {
	flag.IntVar(&length, "length", 5, "length of fake chain to create")

	// Default repodir is different than Filecoin to avoid accidental clobbering of real data.
	flag.StringVar(&repodir, "repodir", "~/.fakecoin", "repo directory to use")
}

func main() {
	ctx := context.Background()

	var cmd string

	if len(os.Args) > 1 {
		cmd = os.Args[1]
		if len(os.Args) > 2 {
			// Remove the cmd argument if there are options, to satisfy flag.Parse() while still allowing a command-first syntax.
			os.Args = append(os.Args[1:], os.Args[0])
		}
	}
	flag.Parse()

	switch cmd {
	default:
		flag.Usage()
	case "fake":
		r, err := repo.OpenFSRepo(repodir)
		if err != nil {
			log.Fatal(err)
		}
		defer closeRepo(r)

		cm, _ := getChainManager(r.Datastore())
		err = cm.Load()
		if err != nil {
			log.Fatal(err)
		}

		err = fake(ctx, length, cm.GetHeaviestTipSet, cm.ProcessNewBlock, cm.LoadStateTreeTS)
		if err != nil {
			log.Fatal(err)
		}
	// TODO: Make usage message reflect the command argument.

	case "actors":
		r, err := repo.OpenFSRepo(repodir)
		if err != nil {
			log.Fatal(err)
		}
		defer closeRepo(r)

		_, cst, cm, bts, err := getStateTree(ctx, r.Datastore())
		if err != nil {
			log.Fatal(err)
		}
		err = fakeActors(ctx, cst, cm, bts)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func closeRepo(r *repo.FSRepo) {
	err := r.Close()
	if err != nil {
		log.Fatal(err)
	}
}

func getChainManager(d repo.Datastore) (*core.ChainManager, *hamt.CborIpldStore) {
	bs := blockstore.NewBlockstore(d)
	cst := &hamt.CborIpldStore{Blocks: bserv.New(bs, offline.Exchange(bs))}
	cm := core.NewChainManager(d, cst)
	return cm, cst
}

func getBlockGenerator(msgPool *core.MessagePool, cm *core.ChainManager, cst *hamt.CborIpldStore) mining.BlockGenerator {
	return mining.NewBlockGenerator(msgPool, func(ctx context.Context, ts core.TipSet) (state.Tree, error) {
		return cm.LoadStateTreeTS(ctx, ts)
	}, func(ctx context.Context, ts core.TipSet) (state.Tree, error) {
		return cm.LoadParentStateTree(ctx, ts)
	}, core.ApplyMessages)
}

func getStateTree(ctx context.Context, d repo.Datastore) (state.Tree, *hamt.CborIpldStore, *core.ChainManager, core.TipSet, error) {
	cm, cst := getChainManager(d)
	err := cm.Load()
	if err != nil {
		log.Fatal(err)
	}

	bts := cm.GetHeaviestTipSet()
	st, err := cm.LoadStateTreeTS(ctx, bts)
	return st, cst, cm, bts, err
}

func fake(ctx context.Context, length int, getHeaviestTipSet core.HeaviestTipSetGetter, processNewBlock core.NewBlockProcessor, stateFromTS core.AggregateStateTreeComputer) error {
	ts := getHeaviestTipSet()
	_, err := core.AddChain(ctx, processNewBlock, stateFromTS, ts.ToSlice(), length)
	if err != nil {
		return err
	}
	fmt.Printf("Added chain of %d empty blocks.\n", length)

	return err
}

// fakeActors adds a block ensuring that the StateTree contains at least one of each extant Actor type, along with
// well-formed data in its memory. For now, this exists primarily to exercise the Filecoin Explorer, though it may
// be used for testing in the future.
func fakeActors(ctx context.Context, cst *hamt.CborIpldStore, cm *core.ChainManager, bts core.TipSet) (err error) {
	msgPool := core.NewMessagePool()

	//// Have the storage market actor create a new miner
	params, err := abi.ToEncodedValues(types.NewBytesAmount(100000), []byte{}, core.RequireRandomPeerID())
	if err != nil {
		return err
	}

	newMinerMessage := types.NewMessage(address.TestAddress, address.StorageMarketAddress, 0, types.NewAttoFILFromFIL(400), "createMiner", params)
	_, err = msgPool.Add(newMinerMessage)
	if err != nil {
		return err
	}

	blk, err := mineBlock(ctx, msgPool, cst, cm, bts.ToSlice())
	if err != nil {
		return err
	}
	msgPool = core.NewMessagePool()

	cid, err := newMinerMessage.Cid()
	if err != nil {
		return err
	}

	var createMinerReceipt *types.MessageReceipt
	err = cm.WaitForMessage(ctx, cid, func(b *types.Block, msg *types.Message, rcp *types.MessageReceipt) error {
		createMinerReceipt = rcp
		return nil
	})
	if err != nil {
		return err
	}

	minerAddress, err := types.NewAddressFromBytes(createMinerReceipt.Return[0])
	if err != nil {
		return err
	}

	// Add a new ask to the storage market
	params, err = abi.ToEncodedValues(types.NewAttoFILFromFIL(10), types.NewBytesAmount(1000))
	if err != nil {
		return err
	}
	askMsg := types.NewMessage(address.TestAddress, minerAddress, 1, types.NewAttoFILFromFIL(100), "addAsk", params)
	_, err = msgPool.Add(askMsg)
	if err != nil {
		return err
	}

	// Add a new bid to the storage market
	params, err = abi.ToEncodedValues(types.NewAttoFILFromFIL(9), types.NewBytesAmount(10))
	if err != nil {
		return err
	}
	bidMsg := types.NewMessage(address.TestAddress2, address.StorageMarketAddress, 0, types.NewAttoFILFromFIL(90), "addBid", params)
	_, err = msgPool.Add(bidMsg)
	if err != nil {
		return err
	}

	// mine again
	blk, err = mineBlock(ctx, msgPool, cst, cm, []*types.Block{blk})
	if err != nil {
		return err
	}
	msgPool = core.NewMessagePool()

	// Create deal
	params, err = abi.ToEncodedValues(big.NewInt(0), big.NewInt(0), address.TestAddress2, types.NewCidForTestGetter()().Bytes())
	if err != nil {
		return err
	}
	newDealMessage := types.NewMessage(address.TestAddress, address.StorageMarketAddress, 2, types.NewAttoFILFromFIL(400), "addDeal", params)
	_, err = msgPool.Add(newDealMessage)
	if err != nil {
		return err
	}

	_, err = mineBlock(ctx, msgPool, cst, cm, []*types.Block{blk})
	return err
}

func mineBlock(ctx context.Context, mp *core.MessagePool, cst *hamt.CborIpldStore, cm *core.ChainManager, blks []*types.Block) (*types.Block, error) {
	bg := getBlockGenerator(mp, cm, cst)
	ra := types.MakeTestAddress("rewardaddress")

	const nullBlockCount = 0
	blk, err := bg.Generate(ctx, core.NewTipSet(blks...), nil, nullBlockCount, ra)
	if err != nil {
		return nil, err
	}

	_, err = cm.ProcessNewBlock(ctx, blk)
	if err != nil {
		return nil, err
	}

	return blk, nil
}
