package impl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/node"
	"github.com/filecoin-project/go-filecoin/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChainHead(t *testing.T) {
	t.Parallel()
	t.Run("returns an error if no best block", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)

		n := node.MakeNodesUnstarted(t, 1, true, true)[0]
		api := New(n)

		_, err := api.Chain().Head()

		require.Error(err)
		require.EqualError(err, ErrHeaviestTipSetNotFound.Error())
	})

	t.Run("emits the blockchain head", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		require := require.New(t)
		assert := assert.New(t)

		blk := chain.NewBlockForTest(nil, 1)
		n := node.MakeNodesUnstarted(t, 1, true, true)[0]

		n.ChainMgr.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, blk))

		api := New(n)
		out, err := api.Chain().Head()

		require.NoError(err)
		assert.Len(out, 1)
		types.AssertCidsEqual(assert, out[0], blk.Cid())
	})
}

func TestChainLsRun(t *testing.T) {
	t.Parallel()
	t.Run("chain of height two", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)
		assert := assert.New(t)

		ctx := context.Background()
		n := node.MakeNodesUnstarted(t, 1, true, true)[0]

		err := n.ChainMgr.Genesis(ctx, core.InitGenesis)
		require.NoError(err)
		genBlock := core.RequireBestBlock(n.ChainMgr, t)
		chlBlock := chain.NewBlockForTest(genBlock, 1)

		err = n.ChainMgr.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, chlBlock))
		require.NoError(err)

		api := New(n)

		var bs [][]*chain.Block
		for raw := range api.Chain().Ls(ctx) {
			switch v := raw.(type) {
			case core.TipSet:
				bs = append(bs, v.ToSlice())
			default:
				assert.FailNow("invalid element in ls", v)
			}
		}

		assert.Equal(2, len(bs))
		types.AssertHaveSameCid(assert, chlBlock, bs[0][0])
		types.AssertHaveSameCid(assert, genBlock, bs[1][0])
	})

	t.Run("emit best block and then time out getting parent", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)

		ctx := context.Background()
		n := node.MakeNodesUnstarted(t, 1, true, true)[0]

		parBlock := chain.NewBlockForTest(nil, 0)
		chlBlock := chain.NewBlockForTest(parBlock, 1)

		err := n.ChainMgr.SetHeaviestTipSetForTest(ctx, core.RequireNewTipSet(require, chlBlock))
		require.NoError(err)

		api := New(n)
		// parBlock is not known to the chain, which causes the timeout
		var innerErr error
		for raw := range api.Chain().Ls(ctx) {
			switch v := raw.(type) {
			case error:
				innerErr = v
			case core.TipSet:
				// ignore
			default:
				require.FailNow("invalid element in ls", v)
			}
		}

		require.NotNil(innerErr)
		require.EqualError(innerErr, "error fetching block: context deadline exceeded")
	})

	t.Run("JSON marshaling", func(t *testing.T) {
		t.Parallel()
		assert := assert.New(t)

		parent := chain.NewBlockForTest(nil, 0)
		child := chain.NewBlockForTest(parent, 1)

		// Generate a single private/public key pair
		ki := crypto.MustGenerateKeyInfo(1, crypto.GenerateKeyInfoSeed())
		// Create a mockSigner (bad name) that can sign using the previously generated key
		mockSigner := crypto.NewMockSigner(ki)
		// Generate SignedMessages
		newSignedMessage := chain.NewSignedMessageForTestGetter(mockSigner)
		message := newSignedMessage()

		retVal := []byte{1, 2, 3}
		receipt := &chain.MessageReceipt{
			ExitCode: 123,
			Return:   []types.Bytes{retVal},
		}
		child.Messages = []*chain.SignedMessage{message}
		child.MessageReceipts = []*chain.MessageReceipt{receipt}

		marshaled, e1 := json.Marshal(child)
		assert.NoError(e1)
		str := string(marshaled)

		assert.Contains(str, parent.Cid().String())
		assert.Contains(str, message.From.String())
		assert.Contains(str, message.To.String())

		// marshal/unmarshal symmetry
		var unmarshalled chain.Block
		e2 := json.Unmarshal(marshaled, &unmarshalled)
		assert.NoError(e2)

		assert.Equal(uint8(123), unmarshalled.MessageReceipts[0].ExitCode)
		assert.Equal([]types.Bytes{[]byte{1, 2, 3}}, unmarshalled.MessageReceipts[0].Return)

		types.AssertHaveSameCid(assert, child, &unmarshalled)
	})
}
