package chain

import (
	"testing"

	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/stretchr/testify/assert"
)

var ki = crypto.MustGenerateKeyInfo(10, crypto.GenerateKeyInfoSeed())
var mockSigner = crypto.NewMockSigner(ki)
var newSignedMessage = NewSignedMessageForTestGetter(mockSigner)

func TestSignedMessageRecover(t *testing.T) {
	assert := assert.New(t)

	smsg := newSignedMessage()

	mockRecoverer := crypto.MockRecoverer{}

	addr, err := smsg.RecoverAddress(&mockRecoverer)
	assert.NoError(err)
	assert.Equal(mockSigner.Addresses[0], addr)

}

func TestSignedMessageMarshal(t *testing.T) {
	assert := assert.New(t)

	smsg := newSignedMessage()

	marshalled, err := smsg.Marshal()
	assert.NoError(err)

	smsgBack := SignedMessage{}
	err = smsgBack.Unmarshal(marshalled)
	assert.NoError(err)

	assert.Equal(smsg.To, smsgBack.To)
	assert.Equal(smsg.From, smsgBack.From)
	assert.Equal(smsg.Value, smsgBack.Value)
	assert.Equal(smsg.Method, smsgBack.Method)
	assert.Equal(smsg.Params, smsgBack.Params)
	assert.Equal(smsg.Signature, smsgBack.Signature)
}

func TestSignedMessageCid(t *testing.T) {
	assert := assert.New(t)

	smsg1 := newSignedMessage()
	smsg2 := newSignedMessage()

	c1, err := smsg1.Cid()
	assert.NoError(err)
	c2, err := smsg2.Cid()
	assert.NoError(err)

	assert.NotEqual(c1.String(), c2.String())
}
