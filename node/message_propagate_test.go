package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/types"
)

func TestMessagePropagation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require := require.New(t)

	nodes := MakeNodesUnstarted(t, 3, false, true)
	startNodes(t, nodes)
	defer stopNodes(nodes)
	connect(t, nodes[0], nodes[1])
	connect(t, nodes[1], nodes[2])

	// wait for heartbeats to build mesh (gossipsub)
	// and network notifications to propagate
	time.Sleep(time.Millisecond * 1000)

	require.Equal(0, len(nodes[0].MsgPool.Pending()))
	require.Equal(0, len(nodes[1].MsgPool.Pending()))
	require.Equal(0, len(nodes[2].MsgPool.Pending()))

	nd0Addr, err := nodes[0].NewAddress()
	require.NoError(err)

	msg := types.NewMessage(nd0Addr, address.NetworkAddress, 0, types.NewAttoFILFromFIL(123), "", nil)
	smsg, err := types.NewSignedMessage(*msg, nodes[0].Wallet)
	require.NoError(err)
	require.NoError(nodes[0].AddNewMessage(ctx, smsg))

	// Wait for message to propagate across network
	synced := false
	for i := 0; i < 30; i++ {
		l1 := len(nodes[0].MsgPool.Pending())
		l2 := len(nodes[1].MsgPool.Pending())
		l3 := len(nodes[2].MsgPool.Pending())
		synced = l1 == 1 && l2 == 1 && l3 == 1
		if synced {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.True(synced, "failed to propagate messages")
}
