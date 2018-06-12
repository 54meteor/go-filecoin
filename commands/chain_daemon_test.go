package commands

import (
	"bytes"
	"encoding/json"
	"testing"

	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	th "github.com/filecoin-project/go-filecoin/testhelpers"
)

func TestChainDaemon(t *testing.T) {
	t.Run("chain ls returns the whole chain", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		d := th.NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		op1 := d.RunSuccess("mining", "once", "--enc", "text")
		result1 := op1.ReadStdoutTrimNewlines()
		c, err := cid.Parse(result1)
		require.NoError(err)

		op2 := d.RunSuccess("chain", "ls", "--enc", "json")
		result2 := op2.ReadStdoutTrimNewlines()

		var bs []types.Block
		for _, line := range bytes.Split([]byte(result2), []byte{'\n'}) {
			var b types.Block
			err := json.Unmarshal(line, &b)
			require.NoError(err)
			bs = append(bs, b)

			// ensure conformance with JSON schema
			requireSchemaConformance(t, line, "filecoin_block")
		}

		assert.Equal(2, len(bs))
		assert.True(bs[1].Parents.Empty())
		assert.True(c.Equals(bs[0].Cid()))
	})

	t.Run("chain head with chain of size 1 returns genesis block", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		d := th.NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		op := d.RunSuccess("chain", "ls", "--enc", "json")
		result := op.ReadStdoutTrimNewlines()

		var b types.Block
		err := json.Unmarshal([]byte(result), &b)
		require.NoError(err)

		assert.True(b.Parents.Empty())
	})
}
