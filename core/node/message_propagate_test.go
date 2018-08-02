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

	// Wait for network connection notifications to propagate
	time.Sleep(time.Millisecond * 50)

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
	time.Sleep(time.Millisecond * 50)

	require.Equal(1, len(nodes[0].MsgPool.Pending()))
	require.Equal(1, len(nodes[1].MsgPool.Pending()))
	require.Equal(1, len(nodes[2].MsgPool.Pending()))
}
