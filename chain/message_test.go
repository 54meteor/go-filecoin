package chain

import (
	"testing"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
)

func TestMessageMarshal(t *testing.T) {
	assert := assert.New(t)
	addrGetter := address.NewForTestGetter()

	// TODO: allow more types than just strings for the params
	// currently []interface{} results in type information getting lost when doing
	// a roundtrip with the default cbor encoder.
	msg := NewMessage(
		addrGetter(),
		addrGetter(),
		0,
		types.NewAttoFILFromFIL(17777),
		"send",
		[]byte("foobar"),
	)

	marshalled, err := msg.Marshal()
	assert.NoError(err)

	msgBack := Message{}
	err = msgBack.Unmarshal(marshalled)
	assert.NoError(err)

	assert.Equal(msg.To, msgBack.To)
	assert.Equal(msg.From, msgBack.From)
	assert.Equal(msg.Value, msgBack.Value)
	assert.Equal(msg.Method, msgBack.Method)
	assert.Equal(msg.Params, msgBack.Params)
}

func TestMessageCid(t *testing.T) {
	assert := assert.New(t)
	addrGetter := address.NewForTestGetter()

	msg1 := NewMessage(
		addrGetter(),
		addrGetter(),
		0,
		types.NewAttoFILFromFIL(999),
		"send",
		nil,
	)

	msg2 := NewMessage(
		addrGetter(),
		addrGetter(),
		0,
		types.NewAttoFILFromFIL(4004),
		"send",
		nil,
	)

	c1, err := msg1.Cid()
	assert.NoError(err)
	c2, err := msg2.Cid()
	assert.NoError(err)

	assert.NotEqual(c1.String(), c2.String())
}
