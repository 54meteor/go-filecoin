package node_test

import (
	"context"
	"sync"
	"testing"
	"time"

	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	unixfs "gx/ipfs/Qmdg2crJzNUF1mLPnLPSCCaDdLDqE4Qrh9QEiDooSYkvuB/go-unixfs"
	dag "gx/ipfs/QmeLG6jF1xvEmHca5Vy4q4EdQWp8Xq9S6EPyZrN9wvSRLC/go-merkledag"

	"github.com/filecoin-project/go-filecoin/api/impl"
	. "github.com/filecoin-project/go-filecoin/node"
	"github.com/filecoin-project/go-filecoin/types"

	"github.com/stretchr/testify/assert"
)

func TestSerializeProposal(t *testing.T) {
	p := &StorageDealProposal{}
	p.Size = types.NewBytesAmount(5)
	v, _ := cid.Decode("QmcrriCMhjb5ZWzmPNxmP53px47tSPcXBNaMtLdgcKFJYk")
	p.PieceRef = v
	_, err := cbor.DumpObject(p)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorageProtocolBasic(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	ctx := context.Background()

	seed := MakeChainSeed(t, TestGenCfg)

	// make two nodes, one of which is the miner (and gets the miner peer key)
	miner := NodeWithChainSeed(t, seed, PeerKeyOpt(PeerKeys[0]))
	client := NodeWithChainSeed(t, seed)
	minerAPI := impl.New(miner)

	// Give the miner node the right private key, and set them up with
	// the miner actor
	seed.GiveKey(t, miner, "foo")
	mineraddr, minerOwnerAddr := seed.GiveMiner(t, miner, 0)

	seed.GiveKey(t, client, "bar")

	c := NewStorageMinerClient(client)
	m, err := NewStorageMiner(ctx, miner, mineraddr, minerOwnerAddr)
	assert.NoError(err)
	_ = m

	assert.NoError(miner.Start(ctx))
	assert.NoError(client.Start(ctx))

	ConnectNodes(t, miner, client)
	err = minerAPI.Mining().Start(ctx)
	assert.NoError(err)
	defer minerAPI.Mining().Stop(ctx)

	sectorSize := uint64(miner.SectorBuilder.BinSize())

	data := unixfs.NewFSNode(unixfs.TFile)
	bytes := make([]byte, sectorSize)
	for i := 0; uint64(i) < sectorSize; i++ {
		bytes[i] = byte(i)
	}
	data.SetData(bytes)

	raw, err := data.GetBytes()
	assert.NoError(err)
	protonode := dag.NodeWithData(raw)

	assert.NoError(client.Blockservice.AddBlock(protonode))

	var foundSeal bool
	var foundPoSt bool

	var wg sync.WaitGroup
	wg.Add(2)

	// TODO: remove this hack to get new blocks
	old := miner.AddNewlyMinedBlock
	miner.AddNewlyMinedBlock = func(ctx context.Context, blk *types.Block) {
		old(ctx, blk)

		if !foundSeal {
			for i, msg := range blk.Messages {
				if msg.Message.Method == "commitSector" {
					assert.False(foundSeal, "multiple commitSector submissions must not happen")
					assert.Equal(uint8(0), blk.MessageReceipts[i].ExitCode, "seal submission failed")
					foundSeal = true
					wg.Done()
				}
			}
		}
		if !foundPoSt {
			for i, msg := range blk.Messages {
				if msg.Message.Method == "submitPoSt" {
					assert.False(foundPoSt, "multiple post submissions must not happen")
					assert.Equal(uint8(0), blk.MessageReceipts[i].ExitCode, "post submission failed")
					foundPoSt = true
					wg.Done()
				}
			}
		}
	}

	ref, err := c.TryToStoreData(ctx, mineraddr, protonode.Cid(), 10, types.NewAttoFILFromFIL(60))
	assert.NoError(err)

	time.Sleep(time.Millisecond * 100) // Bad whyrusleeping, bad!

	resp, err := c.Query(ctx, ref)
	assert.NoError(err)
	assert.Equal(Staged, resp.State, resp.Message)

	assert.False(waitTimeout(&wg, 20*time.Second), "waiting for submission timed out")

	// Now all things should be ready
	resp, err = c.Query(ctx, ref)
	assert.NoError(err)
	assert.Equal(Posted, resp.State, resp.Message)
	assert.Equal(uint64(1), resp.ProofInfo.SectorID)
}

// waitTimeout waits for the waitgroup for the specified max timeout.
// Returns true if waiting timed out.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}
