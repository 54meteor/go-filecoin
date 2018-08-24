package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gx/ipfs/QmYVNvtQkeZ6AKSwDrjQTs432QtL6umrrK41EBq3cu7iSP/go-cid"
	"gx/ipfs/QmZR2XWVVBCtbgBWnQhWk2xcQfaR3W8faQPriAiaaj7rsr/go-libp2p-peerstore"
	"gx/ipfs/QmdVrMn1LhB4ybb8hMVaMLXnA8XRSewMnK6YqXKXoTcRvN/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/mining"
	"github.com/filecoin-project/go-filecoin/repo"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	"github.com/filecoin-project/go-filecoin/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var seed = types.GenerateKeyInfoSeed()
var ki = types.MustGenerateKeyInfo(10, seed)
var mockSigner = types.NewMockSigner(ki)
var newSignedMessage = types.NewSignedMessageForTestGetter(mockSigner)

func TestNodeConstruct(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	nd := MakeNodesUnstarted(t, 1, false, true)[0]
	assert.NotNil(nd.Host)

	nd.Stop(context.Background())
}

func TestNodeNetworking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert := assert.New(t)

	nds := MakeNodesUnstarted(t, 2, false, true)
	nd1, nd2 := nds[0], nds[1]

	pinfo := peerstore.PeerInfo{
		ID:    nd2.Host.ID(),
		Addrs: nd2.Host.Addrs(),
	}

	err := nd1.Host.Connect(ctx, pinfo)
	assert.NoError(err)

	nd1.Stop(ctx)
	nd2.Stop(ctx)
}

func TestConnectsToBootstrapNodes(t *testing.T) {
	t.Parallel()

	t.Run("no bootstrap nodes no problem", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		ctx := context.Background()

		r := repo.NewInMemoryRepo()
		r.Config().Swarm.Address = "/ip4/0.0.0.0/tcp/0"

		require.NoError(Init(ctx, r, core.InitGenesis))
		r.Config().Bootstrap.Addresses = []string{}
		opts, err := OptionsFromRepo(r)
		require.NoError(err)

		nd, err := New(ctx, opts...)
		require.NoError(err)
		assert.NoError(nd.Start(ctx))
		defer nd.Stop(ctx)
	})

	t.Run("connects to bootstrap nodes", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		ctx := context.Background()

		// These are two bootstrap nodes we'll connect to.
		nds := MakeNodesStarted(t, 2, false, true)
		nd1, nd2 := nds[0], nds[1]

		// Gotta be a better way to do this?
		peer1 := fmt.Sprintf("%s/ipfs/%s", nd1.Host.Addrs()[0].String(), nd1.Host.ID().Pretty())
		peer2 := fmt.Sprintf("%s/ipfs/%s", nd2.Host.Addrs()[0].String(), nd2.Host.ID().Pretty())

		// Create a node with the nodes above as bootstrap nodes.
		r := repo.NewInMemoryRepo()
		r.Config().Swarm.Address = "/ip4/0.0.0.0/tcp/0"

		require.NoError(Init(ctx, r, core.InitGenesis))
		r.Config().Bootstrap.Addresses = []string{peer1, peer2}
		opts, err := OptionsFromRepo(r)
		require.NoError(err)
		nd, err := New(ctx, opts...)
		require.NoError(err)
		nd.Bootstrapper.MinPeerThreshold = 2
		nd.Bootstrapper.Period = 10 * time.Millisecond
		assert.NoError(nd.Start(ctx))
		defer nd.Stop(ctx)

		// Ensure they're connected.
		connected := false
		// poll until we are connected, to avoid flaky tests
		for i := 0; i <= 30; i++ {
			l1 := len(nd.Host.Network().ConnsToPeer(nd1.Host.ID()))
			l2 := len(nd.Host.Network().ConnsToPeer(nd2.Host.ID()))

			connected = l1 == 1 && l2 == 1
			if connected {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		assert.True(connected, "failed to connect")
	})
}

func TestNodeInit(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	ctx := context.Background()

	nd := MakeNodesUnstarted(t, 1, true, true)[0]

	assert.NoError(nd.Start(ctx))

	assert.NotNil(nd.ChainMgr.GetHeaviestTipSet())
	nd.Stop(ctx)
}

func TestNodeMining(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	newCid := types.NewCidForTestGetter()
	ctx := context.Background()

	node := MakeNodesUnstarted(t, 1, true, true)[0]

	mockScheduler := &mining.MockScheduler{}
	inCh, outCh, doneWg := make(chan mining.Input), make(chan mining.Output), new(sync.WaitGroup)
	// Apparently you have to have exact types for testify.mock, so
	// we use iCh and oCh for the specific return type of Start().
	var iCh chan<- mining.Input = inCh
	var oCh <-chan mining.Output = outCh

	mockScheduler.On("Start", mock.Anything).Return(iCh, oCh, doneWg)
	node.MiningScheduler = mockScheduler
	// TODO: this is horrible, this setup needs to be a lot less dependent of the inner workings of the node!!
	node.miningCtx, node.cancelMining = context.WithCancel(ctx)
	node.miningInCh = inCh
	node.miningDoneWg = doneWg
	go node.handleNewMiningOutput(oCh)

	// Ensure that the initial input (the best tipset) is wired up properly.
	b1 := &types.Block{StateRoot: newCid()}
	var chainMgrForTest *core.ChainManagerForTest // nolint: gosimple, megacheck
	chainMgrForTest = node.ChainMgr
	chainMgrForTest.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b1))
	require.NoError(node.Start(ctx))
	require.NoError(node.StartMining(ctx))
	gotInput := <-inCh
	require.Equal(1, len(gotInput.TipSet))
	assert.True(b1.Cid().Equals(gotInput.TipSet.ToSlice()[0].Cid()))

	// Ensure that the successive inputs (new best tipsets) are wired up properly.
	b2 := core.MkChild([]*types.Block{b1}, newCid(), 0)
	node.ChainMgr.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b2))
	gotInput = <-inCh
	require.Equal(1, len(gotInput.TipSet))
	assert.True(b2.Cid().Equals(gotInput.TipSet.ToSlice()[0].Cid()))

	// Ensure we don't mine when stopped.
	assert.Equal(mining.ChannelEmpty, mining.ReceiveInCh(inCh))
	node.StopMining(ctx)
	node.ChainMgr.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b2))
	time.Sleep(20 * time.Millisecond)
	assert.Equal(mining.ChannelEmpty, mining.ReceiveInCh(inCh))

	// Ensure we're tearing down cleanly.
	// Part of stopping cleanly is waiting for the worker to be done.
	// Kinda lame to test this way, but better than not testing.
	node = MakeNodesUnstarted(t, 1, true, true)[0]

	chainMgrForTest = node.ChainMgr
	chainMgrForTest.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b1))
	assert.NoError(node.Start(ctx))
	assert.NoError(node.StartMining(ctx))
	workerDone := false
	node.miningDoneWg.Add(1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		workerDone = true
		node.miningDoneWg.Done()
	}()
	node.Stop(ctx)
	assert.True(workerDone)

	// Ensure that the output is wired up correctly.
	node = MakeNodesUnstarted(t, 1, true, true)[0]

	mockScheduler = &mining.MockScheduler{}
	inCh, outCh, doneWg = make(chan mining.Input), make(chan mining.Output), new(sync.WaitGroup)
	iCh = inCh
	oCh = outCh
	mockScheduler.On("Start", mock.Anything).Return(iCh, oCh, doneWg)
	node.MiningScheduler = mockScheduler
	node.miningCtx, node.cancelMining = context.WithCancel(ctx)
	node.miningInCh = inCh
	node.miningDoneWg = doneWg
	go node.handleNewMiningOutput(oCh)
	assert.NoError(node.Start(ctx))

	var gotBlock *types.Block
	gotBlockCh := make(chan struct{})
	node.AddNewlyMinedBlock = func(ctx context.Context, b *types.Block) {
		gotBlock = b
		go func() { gotBlockCh <- struct{}{} }()
	}
	assert.NoError(node.StartMining(ctx))
	go func() { outCh <- mining.NewOutput(b1, nil) }()
	<-gotBlockCh
	assert.True(b1.Cid().Equals(gotBlock.Cid()))
}

func TestUpdateMessagePool(t *testing.T) {
	t.Parallel()
	// Note: majority of tests are in message_pool_test. This test
	// just makes sure it looks like it is hooked up correctly.
	assert := assert.New(t)
	ctx := context.Background()
	node := MakeNodesUnstarted(t, 1, true, true)[0]

	var chainMgrForTest *core.ChainManagerForTest = node.ChainMgr // nolint: gosimple, megacheck, golint

	// Msg pool: [m0, m1],   Chain: b[m2, m3]
	// to
	// Msg pool: [m0, m3],   Chain: b[] -> b[m1, m2]
	m := types.NewSignedMsgs(4, mockSigner)
	core.MustAdd(node.MsgPool, m[0], m[1])
	oldChain := core.NewChainWithMessages(node.CborStore, nil, [][]*types.SignedMessage{{m[2], m[3]}})
	newChain := core.NewChainWithMessages(node.CborStore, nil, [][]*types.SignedMessage{{}}, [][]*types.SignedMessage{{m[1], m[2]}})
	chainMgrForTest.SetHeaviestTipSetForTest(ctx, oldChain[len(oldChain)-1])
	assert.NoError(node.Start(ctx))
	updateMsgPoolDoneCh := make(chan struct{})
	node.HeaviestTipSetHandled = func() { updateMsgPoolDoneCh <- struct{}{} }
	// Triggers a notification, node should update the message pool as a result.
	chainMgrForTest.SetHeaviestTipSetForTest(ctx, newChain[len(newChain)-1])
	<-updateMsgPoolDoneCh
	assert.Equal(2, len(node.MsgPool.Pending()))
	pending := node.MsgPool.Pending()
	assert.True(types.SmsgCidsEqual(m[0], pending[0]) || types.SmsgCidsEqual(m[0], pending[1]))
	assert.True(types.SmsgCidsEqual(m[3], pending[0]) || types.SmsgCidsEqual(m[3], pending[1]))
	node.Stop(ctx)
}

func testWaitHelp(wg *sync.WaitGroup, assert *assert.Assertions, cm *core.ChainManager, expectMsg *types.SignedMessage, expectError bool, cb func(*types.Block, *types.SignedMessage, *types.MessageReceipt) error) {
	expectCid, err := expectMsg.Cid()
	if cb == nil {
		cb = func(b *types.Block, msg *types.SignedMessage,
			rcp *types.MessageReceipt) error {
			assert.True(types.SmsgCidsEqual(expectMsg, msg))
			if wg != nil {
				wg.Done()
			}

			return nil
		}
	}
	assert.NoError(err)

	err = cm.WaitForMessage(context.Background(), expectCid, cb)
	assert.Equal(expectError, err != nil)
}

type smsgs []*types.SignedMessage
type smsgsSet [][]*types.SignedMessage

func TestWaitForMessage(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	ctx := context.Background()

	node := MakeNodesUnstarted(t, 1, true, true)[0]

	err := node.Start(ctx)
	assert.NoError(err)

	stm := (*core.ChainManagerForTest)(node.ChainMgr)

	testWaitExisting(ctx, assert, node, stm)
	testWaitNew(ctx, assert, node, stm)
}

func TestWaitForMessageError(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	ctx := context.Background()

	node := MakeNodesUnstarted(t, 1, true, true)[0]

	assert.NoError(node.Start(ctx))

	stm := (*core.ChainManagerForTest)(node.ChainMgr)

	testWaitError(ctx, assert, node, stm)
}

func testWaitExisting(ctx context.Context, assert *assert.Assertions, node *Node, stm *core.ChainManagerForTest) {
	m1, m2 := newSignedMessage(), newSignedMessage()
	chain := core.NewChainWithMessages(node.CborStore, stm.GetHeaviestTipSet(), smsgsSet{smsgs{m1, m2}})

	stm.SetHeaviestTipSetForTest(ctx, chain[len(chain)-1])

	testWaitHelp(nil, assert, stm, m1, false, nil)
	testWaitHelp(nil, assert, stm, m2, false, nil)
}

func testWaitNew(ctx context.Context, assert *assert.Assertions, node *Node,
	stm *core.ChainManagerForTest) {
	var wg sync.WaitGroup

	_, _ = newSignedMessage(), newSignedMessage() // flush out so we get distinct messages from testWaitExisting
	m3, m4 := newSignedMessage(), newSignedMessage()
	chain := core.NewChainWithMessages(node.CborStore, stm.GetHeaviestTipSet(), smsgsSet{smsgs{m3, m4}})

	wg.Add(2)
	go testWaitHelp(&wg, assert, stm, m3, false, nil)
	go testWaitHelp(&wg, assert, stm, m4, false, nil)
	time.Sleep(10 * time.Millisecond)

	stm.SetHeaviestTipSetForTest(ctx, chain[len(chain)-1])
	wg.Wait()
}

func testWaitError(ctx context.Context, assert *assert.Assertions, node *Node, stm *core.ChainManagerForTest) {
	stm.FetchBlock = func(ctx context.Context, cid *cid.Cid) (*types.Block, error) {
		return nil, fmt.Errorf("error fetching block (in test)")
	}

	m1, m2, m3, m4 := newSignedMessage(), newSignedMessage(), newSignedMessage(), newSignedMessage()
	chain := core.NewChainWithMessages(node.CborStore, stm.GetHeaviestTipSet(), smsgsSet{smsgs{m1, m2}})
	chain2 := core.NewChainWithMessages(node.CborStore, chain[len(chain)-1], smsgsSet{smsgs{m3, m4}})
	stm.SetHeaviestTipSetForTest(ctx, chain2[len(chain2)-1])

	testWaitHelp(nil, assert, stm, m2, true, nil)
}

func TestWaitConflicting(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()

	addr1, addr2, addr3 := mockSigner.Addresses[0], mockSigner.Addresses[1], mockSigner.Addresses[2]

	node := MakeNodesUnstarted(t, 1, true, true)[0]
	testGen := th.MakeGenesisFunc(
		th.ActorAccount(addr1, types.NewAttoFILFromFIL(10000)),
		th.ActorAccount(addr2, types.NewAttoFILFromFIL(0)),
		th.ActorAccount(addr3, types.NewAttoFILFromFIL(0)),
	)
	assert.NoError(node.ChainMgr.Genesis(ctx, testGen))

	assert.NoError(node.Start(ctx))
	stm := (*core.ChainManagerForTest)(node.ChainMgr)

	// Create conflicting messages
	m1 := types.NewMessage(addr1, addr3, 0, types.NewAttoFILFromFIL(6000), "", nil)
	sm1, err := types.NewSignedMessage(*m1, &mockSigner)
	require.NoError(err)

	m2 := types.NewMessage(addr1, addr2, 0, types.NewAttoFILFromFIL(6000), "", nil)
	sm2, err := types.NewSignedMessage(*m2, &mockSigner)
	require.NoError(err)

	base := stm.GetHeaviestTipSet().ToSlice()
	require.Equal(1, len(base))

	b1 := core.MkChild(base, base[0].StateRoot, 0)
	b1.Messages = []*types.SignedMessage{sm1}
	b1.Ticket = []byte{0} // block 1 comes first in message application
	core.MustPut(node.CborStore, b1)
	b2 := core.MkChild(base, base[0].StateRoot, 1)
	b2.Messages = []*types.SignedMessage{sm2}
	b2.Ticket = []byte{1}
	core.MustPut(node.CborStore, b2)

	stm.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, b1, b2))
	msgApplySucc := func(b *types.Block, msg *types.SignedMessage,
		rcp *types.MessageReceipt) error {
		assert.NotNil(rcp)
		return nil
	}
	msgApplyFail := func(b *types.Block, msg *types.SignedMessage,
		rcp *types.MessageReceipt) error {
		assert.Nil(rcp)
		return nil
	}

	testWaitHelp(nil, assert, stm, sm1, false, msgApplySucc)
	testWaitHelp(nil, assert, stm, sm2, false, msgApplyFail)
}

func TestGetSignature(t *testing.T) {
	t.Parallel()
	t.Run("no method", func(t *testing.T) {
		ctx := context.Background()
		assert := assert.New(t)

		nd := MakeNodesUnstarted(t, 1, true, true)[0]
		nodeAddr, err := nd.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)
		nd.ChainMgr.Genesis(ctx, tif)

		assert.NoError(nd.Start(ctx))
		defer nd.Stop(ctx)

		sig, err := nd.GetSignature(ctx, nodeAddr, "")
		assert.Equal(ErrNoMethod, err)
		assert.Nil(sig)
	})
}

func TestOptionWithError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert := assert.New(t)
	r := repo.NewInMemoryRepo()
	assert.NoError(Init(ctx, r, core.InitGenesis))

	opts, err := OptionsFromRepo(r)
	assert.NoError(err)

	scaryErr := errors.New("i am an error grrrr")
	errOpt := func(c *Config) error {
		return scaryErr
	}

	opts = append(opts, errOpt)

	_, err = New(ctx, opts...)
	assert.Error(err, scaryErr)

}

func TestMakePrivateKey(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	// should fail if less than 1024
	badKey, err := makePrivateKey(10)
	assert.Error(err, ErrLittleBits)
	assert.Nil(badKey)

	// 1024 should work
	okKey, err := makePrivateKey(1024)
	assert.NoError(err)
	assert.NotNil(okKey)

	// large values should work
	goodKey, err := makePrivateKey(4096)
	assert.NoError(err)
	assert.NotNil(goodKey)
}

func TestNextNonce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("account does not exist", func(t *testing.T) {
		assert := assert.New(t)
		node := MakeNodesUnstarted(t, 1, true, true)[0]

		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)

		err = node.ChainMgr.Genesis(ctx, tif)
		assert.NoError(err)
		assert.NoError(node.Start(ctx))

		noActorAddress, err := node.NewAddress() // Won't have an actor.
		assert.NoError(err)

		_, err = NextNonce(ctx, node, noActorAddress)
		assert.Error(err)
		assert.Contains(err.Error(), "not found")
	})

	t.Run("account exists, largest value is in message pool", func(t *testing.T) {
		assert := assert.New(t)

		node := MakeNodesUnstarted(t, 1, true, true)[0]
		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))

		assert.NoError(node.Start(ctx))

		// TODO: does sending a message to ourselves fit the spirit of the test?
		msg := types.NewMessage(nodeAddr, nodeAddr, 0, nil, "foo", []byte{})
		msg.Nonce = 42
		smsg, err := types.NewSignedMessage(*msg, node.Wallet)
		assert.NoError(err)
		core.MustAdd(node.MsgPool, smsg)

		nonce, err := NextNonce(ctx, node, nodeAddr)
		assert.NoError(err)
		assert.Equal(uint64(43), nonce)
	})
}

func TestNewMessageWithNextNonce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("includes correct nonce", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		node := MakeNodesUnstarted(t, 1, true, true)[0]
		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
			th.ActorNonce(nodeAddr, 42),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))
		assert.NoError(node.Start(ctx))

		bb := types.NewBlockForTest(core.RequireBestBlock(node.ChainMgr, t), 1)
		var chainMgrForTest *core.ChainManagerForTest = node.ChainMgr // nolint: golint
		chainMgrForTest.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, bb))

		msg, err := NewMessageWithNextNonce(ctx, node, nodeAddr, types.NewAddressForTestGetter()(), nil, "foo", []byte{})
		require.NoError(err)
		assert.Equal(uint64(42), uint64(msg.Nonce))
	})
}

func TestQueryMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("can contact payment broker", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		node := MakeNodesUnstarted(t, 1, true, true)[0]
		nodeAddr, err := node.NewAddress()
		require.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))
		assert.NoError(node.Start(ctx))

		args, err := abi.ToEncodedValues(nodeAddr)
		require.NoError(err)

		returnValue, exitCode, err := node.CallQueryMethod(ctx, address.PaymentBrokerAddress, "ls", args, nil)
		require.NoError(err)
		require.Equal(uint8(0), exitCode)

		assert.NotNil(returnValue)
	})
}

func TestCreateMiner(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	t.Run("success", func(t *testing.T) {
		ctx := context.Background()

		node := MakeOfflineNode(t)
		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(1000000)),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))
		assert.NoError(node.Start(ctx))

		assert.Equal(0, len(node.SectorBuilders))

		result := <-RunCreateMiner(t, node, nodeAddr, *types.NewBytesAmount(100000), core.RequireRandomPeerID(), *types.NewAttoFILFromFIL(100))
		require.NoError(result.Err)
		assert.NotNil(result.MinerAddress)

		assert.Equal(*result.MinerAddress, node.Repo.Config().Mining.MinerAddresses[0])
	})

	t.Run("fail with pledge too low", func(t *testing.T) {
		ctx := context.Background()

		node := MakeOfflineNode(t)
		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))
		assert.NoError(node.Start(ctx))

		assert.Equal(0, len(node.SectorBuilders))

		result := <-RunCreateMiner(t, node, nodeAddr, *types.NewBytesAmount(10), core.RequireRandomPeerID(), *types.NewAttoFILFromFIL(10))
		assert.Error(result.Err)
		assert.Contains(result.Err.Error(), "pledge must be at least")
	})

	t.Run("fail with insufficient funds", func(t *testing.T) {
		ctx := context.Background()

		node := MakeOfflineNode(t)
		nodeAddr, err := node.NewAddress()
		assert.NoError(err)

		tif := th.MakeGenesisFunc(
			th.ActorAccount(nodeAddr, types.NewAttoFILFromFIL(10000)),
		)
		assert.NoError(node.ChainMgr.Genesis(ctx, tif))
		assert.NoError(node.Start(ctx))

		assert.Equal(0, len(node.SectorBuilders))

		result := <-RunCreateMiner(t, node, nodeAddr, *types.NewBytesAmount(20000), core.RequireRandomPeerID(), *types.NewAttoFILFromFIL(1000000))
		assert.Error(result.Err)
		assert.Contains(result.Err.Error(), "not enough balance")
	})
}

// TODO: this currently only tests for a single miner, as that is all we can do right now.
func TestCreateSectorBuilders(t *testing.T) {
	// TODO: enable this test once the mockmining is fixed
	t.Skip()
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	ctx := context.Background()

	node := MakeNodesUnstarted(t, 1, true, true)[0]
	minerAddr1, err := node.NewAddress()
	assert.NoError(err)

	tif := th.MakeGenesisFunc(
		th.ActorAccount(minerAddr1, types.NewAttoFILFromFIL(10000)),
	)
	assert.NoError(node.ChainMgr.Genesis(ctx, tif))
	assert.NoError(node.Start(ctx))

	assert.Equal(0, len(node.SectorBuilders))

	result := <-RunCreateMiner(t, node, minerAddr1, *types.NewBytesAmount(100000), core.RequireRandomPeerID(), *types.NewAttoFILFromFIL(100))
	require.NoError(result.Err)

	assert.Equal(0, len(node.SectorBuilders))

	node.StartMining(ctx)
	assert.Equal(1, len(node.SectorBuilders))

	// ensure that that the sector builders have been configured
	// with the mining address of each of the node's miners

	sbaddrs := make(map[types.Address]struct{})
	for _, sb := range node.SectorBuilders {
		sbaddrs[sb.MinerAddr] = struct{}{}
	}

	cfaddrs := make(map[types.Address]struct{})
	for _, addr := range node.Repo.Config().Mining.MinerAddresses {
		cfaddrs[addr] = struct{}{}
	}

	assert.Equal(cfaddrs, sbaddrs)
}

func TestLookupMinerAddress(t *testing.T) {
	t.Parallel()

	/*
		t.Run("lookup fails if provided address of non-miner actor", func(t *testing.T) {
			t.Parallel()

			require := require.New(t)
			ctx := context.Background()

			nd := MakeNodesStarted(t, 1, true, true)[0]

			_, err := nd.Lookup.GetPeerIDByMinerAddress(ctx, nd.RewardAddress())
			require.Error(err)
		})
	*/

	t.Run("lookup succeeds if provided address of a miner actor", func(t *testing.T) {
		t.Skip("FIXME: likely has problems with making assumptions about mining")
		t.Parallel()

		require := require.New(t)
		ctx := context.Background()

		nd := MakeNodesUnstarted(t, 1, true, true)[0]

		newMinerPid := core.RequireRandomPeerID()

		// Note: we should probably just have nodes make an address for themselves during init
		minerOwnerAddr, err := nd.NewAddress()
		require.NoError(err)

		// initialize genesis block
		tif := th.MakeGenesisFunc(
			th.ActorAccount(minerOwnerAddr, types.NewAttoFILFromFIL(10000)),
		)
		require.NoError(nd.ChainMgr.Genesis(ctx, tif))
		require.NoError(nd.Start(ctx))

		// create a miner, owned by the account actor
		result := <-RunCreateMiner(t, nd, minerOwnerAddr, *types.NewBytesAmount(100000), newMinerPid, *types.NewAttoFILFromFIL(100))
		require.NoError(result.Err)

		// retrieve the libp2p identity of the newly-created miner
		retPid, err := nd.Lookup.GetPeerIDByMinerAddress(ctx, *result.MinerAddress)
		require.NoError(err)
		require.Equal(peer.IDB58Encode(newMinerPid), peer.IDB58Encode(retPid))
	})
}

func TestDefaultMessageFromAddress(t *testing.T) {
	t.Run("it returns the configured wallet default if it exists", func(t *testing.T) {
		require := require.New(t)

		n := MakeOfflineNode(t)

		// generate a default address
		addrA, err := n.NewAddress()
		require.NoError(err)

		// load up the wallet with a few more addresses
		n.NewAddress()
		n.NewAddress()

		// configure a default
		n.Repo.Config().Wallet.DefaultAddress = addrA

		addrB, err := n.DefaultSenderAddress()
		require.NoError(err)
		require.Equal(addrA.String(), addrB.String())
	})

	/*
		t.Run("it returns an error if no default address was configured and more than one address in wallet", func(t *testing.T) {
			require := require.New(t)

			n := MakeOfflineNode(t)

			// generate a few addresses
			n.NewAddress()
			n.NewAddress()
			n.NewAddress()

			// remove existing wallet config
			n.Repo.Config().Wallet = &config.WalletConfig{}

			_, err := n.DefaultSenderAddress()
			require.Error(err)
			require.Equal(ErrNoDefaultMessageFromAddress, err)
		})
	*/
}
