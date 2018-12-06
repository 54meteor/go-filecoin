package retrieval_test

import (
	"bytes"
	"context"
	"io/ioutil"
	"testing"
	"time"

	"gx/ipfs/QmR8BauakNcBa3RbE4nbQu76PDiJgoQgz8AJdhJuiU4TAw/go-cid"
	"gx/ipfs/QmcqU6QUDSXprb1518vYDGczrTJTyGwLG9eUa5iNX4xUtS/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/api"
	"github.com/filecoin-project/go-filecoin/api/impl"
	"github.com/filecoin-project/go-filecoin/node"
	"github.com/filecoin-project/go-filecoin/types"

	"github.com/stretchr/testify/require"
)

func TestRetrievalProtocolPieceNotFound(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx := context.Background()

	minerNode, clientNode, _, _ := configureMinerAndClient(t)

	require.NoError(minerNode.StartMining(ctx))
	defer minerNode.StopMining(ctx)

	someRandomCid := types.NewCidForTestGetter()()

	_, err := retrievePieceBytes(ctx, impl.New(clientNode).RetrievalClient(), minerNode.Host().ID(), someRandomCid)
	require.Error(err)
}

func TestRetrievalProtocolHappyPath(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	ctx := context.Background()

	minerNode, clientNode, _, minerOwnerAddr := configureMinerAndClient(t)

	// start mining
	require.NoError(minerNode.StartMining(ctx))
	defer minerNode.StopMining(ctx)

	response, err := minerNode.SectorStore.GetMaxUnsealedBytesPerSector()
	require.NoError(err)
	testSectorSize := uint64(response.NumBytes)

	// pretend like we've run through the storage protocol and saved user's
	// data to the miner's block store and sector builder
	pieceA, bytesA := node.CreateRandomPieceInfo(t, minerNode.BlockService(), testSectorSize/2)
	pieceB, bytesB := node.CreateRandomPieceInfo(t, minerNode.BlockService(), testSectorSize-(testSectorSize/2))

	_, err = minerNode.SectorBuilder().AddPiece(ctx, pieceA) // blocks until all piece-bytes written to sector
	require.NoError(err)
	_, err = minerNode.SectorBuilder().AddPiece(ctx, pieceB) // triggers seal
	require.NoError(err)

	// wait for commitSector to make it into the chain
	cancelA := make(chan struct{})
	cancelB := make(chan struct{})
	errCh := make(chan error)
	defer close(cancelA)
	defer close(cancelB)
	defer close(errCh)

	select {
	case <-node.FirstMatchingMsgInChain(ctx, t, minerNode.ChainReader, "commitSector", minerOwnerAddr, cancelA, errCh):
	case err = <-errCh:
		require.NoError(err)
	case <-time.After(120 * time.Second):
		cancelA <- struct{}{}
		t.Fatalf("timed out waiting for commitSector message (for sector of size=%d, from miner owner=%s) to appear in **miner** node's chain", testSectorSize, minerOwnerAddr)
	}

	select {
	case <-node.FirstMatchingMsgInChain(ctx, t, clientNode.ChainReader, "commitSector", minerOwnerAddr, cancelB, errCh):
	case err = <-errCh:
		require.NoError(err)
	case <-time.After(120 * time.Second):
		cancelB <- struct{}{}
		t.Fatalf("timed out waiting for commitSector message (for sector of size=%d, from miner owner=%s) to appear in **client** node's chain", testSectorSize, minerOwnerAddr)
	}

	// retrieve piece by CID and compare bytes with what we sent to miner
	retrievedBytesA, err := retrievePieceBytes(ctx, impl.New(clientNode).RetrievalClient(), minerNode.Host().ID(), pieceA.Ref)
	require.NoError(err)
	require.True(bytes.Equal(bytesA, retrievedBytesA))

	// retrieve/compare the second piece for good measure
	retrievedBytesB, err := retrievePieceBytes(ctx, impl.New(clientNode).RetrievalClient(), minerNode.Host().ID(), pieceB.Ref)
	require.NoError(err)
	require.True(bytes.Equal(bytesB, retrievedBytesB))

	// sanity check
	require.True(len(retrievedBytesA) > 0)
}

func retrievePieceBytes(ctx context.Context, retrievalClient api.RetrievalClient, minerPeerID peer.ID, pieceCid cid.Cid) ([]byte, error) {
	r, err := retrievalClient.RetrievePiece(ctx, minerPeerID, pieceCid)
	if err != nil {
		return nil, err
	}

	slice, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}

	return slice, nil
}

func configureMinerAndClient(t *testing.T) (minerNode *node.Node, clientNode *node.Node, minerAddr address.Address, minerOwnerAddr address.Address) {
	ctx := context.Background()

	seed := node.MakeChainSeed(t, node.TestGenCfg)

	// make two nodes, one of which is the minerNode (and gets the miner peer key)
	minerNode = node.NodeWithChainSeed(t, seed, node.PeerKeyOpt(node.PeerKeys[0]), node.AutoSealIntervalSecondsOpt(0))
	clientNode = node.NodeWithChainSeed(t, seed)

	// give the minerNode node a key and the miner associated with that key
	seed.GiveKey(t, minerNode, 0)
	minerAddr, minerOwnerAddr = seed.GiveMiner(t, minerNode, 0)

	// give the clientNode node a private key, too
	seed.GiveKey(t, clientNode, 1)

	// start 'em up
	require.NoError(t, minerNode.Start(ctx))
	require.NoError(t, clientNode.Start(ctx))

	// make sure they're swarmed together (for block propagation)
	node.ConnectNodes(t, minerNode, clientNode)

	return
}
