package commands

import (
	"fmt"
	"testing"

	"gx/ipfs/QmdE4gMduCKCGAcczM2F5ioYDfdeKuPix138wrES1YSr7f/go-ipfs-cmdkit"
	"gx/ipfs/QmeiCcJfDW1GJnWUArudsv5rQsihpi4oyddPhdqo3CfX6i/go-datastore"

	"github.com/filecoin-project/go-filecoin/core/node"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/wallet"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromAddress(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	t.Run("when option is specified", func(t *testing.T) {
		t.Parallel()
		nd, _ := createNodeAndBackend(t)

		opts := make(cmdkit.OptMap)

		hash, err := types.AddressHash([]byte("a new test address"))
		require.NoError(err)

		specifiedAddr := types.NewMainnetAddress(hash)
		opts["from"] = specifiedAddr.String()

		addr, err := fromAddress(opts, nd)
		require.NoError(err)
		assert.Equal(specifiedAddr, addr)
	})

	t.Run("when no option specified and no addresses are in wallet, error out", func(t *testing.T) {
		t.Parallel()
		nd, _ := createNodeAndBackend(t)

		opts := make(cmdkit.OptMap)

		_, err := fromAddress(opts, nd)
		require.Error(err)
	})

	t.Run("when no option specified and too many addresses, error out", func(t *testing.T) {
		t.Parallel()
		nd, fs := createNodeAndBackend(t)

		// create 2 addresses in wallet
		_, err := fs.NewAddress()
		require.NoError(err)

		_, err = fs.NewAddress()
		require.NoError(err)

		// run command
		opts := make(cmdkit.OptMap)

		_, err = fromAddress(opts, nd)
		require.Error(err)
	})

	t.Run("when no option specified, use only address in wallet", func(t *testing.T) {
		t.Parallel()
		nd, fs := createNodeAndBackend(t)

		// create one addresses
		walletAddr1, err := fs.NewAddress()
		require.NoError(err)

		opts := make(cmdkit.OptMap)

		addr, err := fromAddress(opts, nd)
		assert.NoError(err)
		assert.Equal(walletAddr1, addr)
	})

	t.Run("when no option specified but default exists in config, use config address", func(t *testing.T) {
		t.Parallel()
		nd, fs := createNodeAndBackend(t)

		// create two addresses
		_, err := fs.NewAddress()
		require.NoError(err)

		walletAddr2, err := fs.NewAddress()
		require.NoError(err)

		nd.Repo.Config().Set("wallet.defaultAddress", fmt.Sprintf("\"%s\"", walletAddr2.String()))

		opts := make(cmdkit.OptMap)

		addr, err := fromAddress(opts, nd)
		assert.NoError(err)
		assert.Equal(walletAddr2, addr)
	})
}

func createNodeAndBackend(t *testing.T) (*node.Node, *wallet.DSBackend) {
	require := require.New(t)

	nd := node.MakeNodesUnstarted(t, 1, true, true)[0]

	ds := datastore.NewMapDatastore()
	fs, err := wallet.NewDSBackend(ds)
	require.NoError(err)

	nd.Wallet = wallet.New(fs)

	return nd, fs
}
