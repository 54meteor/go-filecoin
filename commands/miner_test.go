package commands

import (
	"strings"
	"sync"
	"testing"

	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/types"
)

func TestMinerCreate(t *testing.T) {
	assert := assert.New(t)

	t.Run("success", func(t *testing.T) {
		var err error
		var addr types.Address

		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		tf := func(fromAddress types.Address, expectSuccess bool) {
			args := []string{"miner", "create"}
			if !fromAddress.Empty() {
				args = append(args, "--from", fromAddress.String())
			}
			args = append(args, "1000000", "20")
			if !expectSuccess {
				d.RunFail(ErrCouldNotDefaultFromAddress.Error(), args...)
				return
			}

			var wg sync.WaitGroup

			wg.Add(1)
			go func() {
				miner := d.RunSuccess(args...)
				addr, err = types.NewAddressFromString(strings.Trim(miner.ReadStdout(), "\n"))
				assert.NoError(err)
				assert.NotEqual(addr, types.Address{})
				wg.Done()
			}()

			d.RunSuccess("mining once")
			wg.Wait()

			// expect address to have been written in config
			config := d.RunSuccess("config mining.minerAddresses")
			assert.Contains(config.ReadStdout(), addr.String())
		}

		// If there's one address, --from can be omitted and we should default
		tf(address.TestAddress, true)
		tf(types.Address{}, true)

		// If there's more than one, then --from must be specified
		d.CreateWalletAddr()
		tf(types.Address{}, false)
		tf(address.TestAddress, true)
	})

	t.Run("validation failure", func(t *testing.T) {
		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		d.CreateWalletAddr()

		d.RunFail("invalid from address",
			"miner", "create",
			"--from", "hello", "1000000", "20",
		)
		d.RunFail("invalid pledge",
			"miner", "create",
			"--from", address.TestAddress.String(), "'-123'", "20",
		)
		d.RunFail("invalid pledge",
			"miner", "create",
			"--from", address.TestAddress.String(), "1f", "20",
		)
		d.RunFail("invalid collateral",
			"miner", "create",
			"--from", address.TestAddress.String(), "100", "2f",
		)
	})

	t.Run("creation failure", func(t *testing.T) {
		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			d.RunFail("pledge must be at least",
				"miner", "create",
				"--from", address.TestAddress.String(), "10", "10",
			)
			wg.Done()
		}()

		d.RunSuccess("mining once")
		wg.Wait()
	})
}

func TestMinerAddAskSuccess(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	d := NewDaemon(t).Start()
	defer d.ShutdownSuccess()

	d.CreateWalletAddr()

	var wg sync.WaitGroup
	var minerAddr types.Address

	wg.Add(1)
	go func() {
		miner := d.RunSuccess("miner", "create", "--from", address.TestAddress.String(), "1000000", "20")
		addr, err := types.NewAddressFromString(strings.Trim(miner.ReadStdout(), "\n"))
		assert.NoError(err)
		assert.NotEqual(addr, types.Address{})
		minerAddr = addr
		wg.Done()
	}()

	// ensure mining runs after the command in our goroutine
	d.RunSuccess("mpool --wait-for-count=1")

	d.RunSuccess("mining once")

	wg.Wait()

	wg.Add(1)
	go func() {
		ask := d.RunSuccess("miner", "add-ask", minerAddr.String(), "2000", "10",
			"--from", address.TestAddress.String(),
		)
		askCid, err := cid.Parse(strings.Trim(ask.ReadStdout(), "\n"))
		require.NoError(t, err)
		assert.NotNil(askCid)

		wg.Done()
	}()

	wg.Wait()
}

func TestMinerAddAskFail(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	d := NewDaemon(t).Start()
	defer d.ShutdownSuccess()

	d.CreateWalletAddr()

	var wg sync.WaitGroup
	var minerAddr types.Address

	wg.Add(1)
	go func() {
		miner := d.RunSuccess("miner", "create",
			"--from", address.TestAddress.String(), "1000000", "20",
		)
		addr, err := types.NewAddressFromString(strings.Trim(miner.ReadStdout(), "\n"))
		assert.NoError(err)
		assert.NotEqual(addr, types.Address{})
		minerAddr = addr
		wg.Done()
	}()

	// ensure mining runs after the command in our goroutine
	d.RunSuccess("mpool --wait-for-count=1")

	d.RunSuccess("mining once")

	wg.Wait()

	d.RunFail(
		"invalid from address",
		"miner", "add-ask", minerAddr.String(), "2000", "10",
		"--from", "hello",
	)
	d.RunFail(
		"invalid miner address",
		"miner", "add-ask", "hello", "2000", "10",
		"--from", address.TestAddress.String(),
	)
	d.RunFail(
		"invalid size",
		"miner", "add-ask", minerAddr.String(), "2f", "10",
		"--from", address.TestAddress.String(),
	)
	d.RunFail(
		"invalid price",
		"miner", "add-ask", minerAddr.String(), "10", "3f",
		"--from", address.TestAddress.String(),
	)
}
