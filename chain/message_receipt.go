package chain

import (
	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"

	"github.com/filecoin-project/go-filecoin/types"
)

func init() {
	cbor.RegisterCborType(MessageReceipt{})
}

// MessageReceipt represents the result of sending a message.
type MessageReceipt struct {
	// `0` is success, anything else is an error code in unix style.
	ExitCode uint8 `json:"exitCode"`

	// Return contains the return values, if any, from processing a message.
	// This can be non-empty even in the case of error (e.g., to provide
	// programmatically readable detail about errors).
	Return []types.Bytes `json:"return"`
}
