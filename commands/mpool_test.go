package commands

import (
	"strings"
	"testing"

	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	"github.com/stretchr/testify/assert"

	"sync"

	"github.com/filecoin-project/go-filecoin/address"
)

func TestMpool(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	t.Run("return all messages", func(t *testing.T) {
		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		d.RunSuccess("message", "send",
			"--from", address.NetworkAddress.String(),
			"--value=10", address.TestAddress.String(),
		)

		out := d.RunSuccess("mpool")
		c := strings.Trim(out.ReadStdout(), "\n")
		ci, err := cid.Decode(c)
		assert.NoError(err)
		assert.NotNil(ci)
	})

	t.Run("wait for enough messages", func(t *testing.T) {
		d := NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		wg := sync.WaitGroup{}
		wg.Add(1)

		complete := false
		go func() {
			out := d.RunSuccess("mpool", "--wait-for-count=3")
			complete = true
			wg.Done()
			c := strings.Split(strings.Trim(out.ReadStdout(), "\n"), "\n")
			assert.Equal(3, len(c))
		}()

		d.RunSuccess("message", "send",
			"--from", address.NetworkAddress.String(),
			"--value=10", address.TestAddress.String(),
		)

		assert.False(complete)

		d.RunSuccess("message", "send",
			"--from", address.NetworkAddress.String(),
			"--value=10", address.TestAddress.String(),
		)

		assert.False(complete)

		d.RunSuccess("message", "send",
			"--from", address.NetworkAddress.String(),
			"--value=10", address.TestAddress.String(),
		)

		wg.Wait()

		assert.True(complete)
	})
}
