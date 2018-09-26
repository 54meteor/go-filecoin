package commands

import (
	"encoding/json"
	"testing"

	"github.com/filecoin-project/go-filecoin/fixtures"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	"github.com/filecoin-project/go-filecoin/types"

	"github.com/stretchr/testify/require"
)

func TestBlockDaemon(t *testing.T) {
	t.Parallel()
	t.Run("show block <cid-of-genesis-block> --enc json returns JSON for a Filecoin block", func(t *testing.T) {
		require := require.New(t)

		d := th.NewDaemon(t, th.WithMiner(fixtures.TestMiners[0])).Start()
		defer d.ShutdownSuccess()

		// mine a block and get its CID
		minedBlockCidStr := th.RunSuccessFirstLine(d, "mining", "once")

		// get the mined block by its CID

		blockGetLine := th.RunSuccessFirstLine(d, "show", "block", minedBlockCidStr, "--enc", "json")
		var blockGetBlock types.Block
		json.Unmarshal([]byte(blockGetLine), &blockGetBlock)

		// ensure that we were returned the correct block

		require.Equal(minedBlockCidStr, blockGetBlock.Cid().String())

		// ensure that the JSON we received from block get conforms to schema

		requireSchemaConformance(t, []byte(blockGetLine), "filecoin_block")
	})
}
