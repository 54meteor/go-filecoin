package commands

import (
	"bytes"
	"encoding/json"
	"testing"

	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"

	"github.com/stretchr/testify/assert"

	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/require"
)

func TestDagDaemon(t *testing.T) {
	t.Parallel()
	t.Run("dag get <cid> returning the genesis block", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		// get the CID of the genesis block from the "chain ls" command output

		op1 := d.RunSuccess("chain", "ls", "--enc", "json")
		result1 := op1.readStdoutTrimNewlines()

		genesisBlockJSONStr := bytes.Split([]byte(result1), []byte{'\n'})[0]

		var expected types.Block
		json.Unmarshal(genesisBlockJSONStr, &expected)

		// get an IPLD node from the DAG by its CID

		op2 := d.RunSuccess("dag", "get", expected.Cid().String(), "--enc", "json")
		result2 := op2.readStdoutTrimNewlines()

		ipldnode, err := cbor.FromJSON(bytes.NewReader([]byte(result2)), types.DefaultHashFunction, -1)
		require.NoError(err)

		// CBOR decode the IPLD node's raw data into a Filecoin block

		var actual types.Block
		err = cbor.DecodeInto(ipldnode.RawData(), &actual)
		require.NoError(err)

		// CIDs should be equal

		types.AssertHaveSameCid(assert, &expected, &actual)
	})
}
