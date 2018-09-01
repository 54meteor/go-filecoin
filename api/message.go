package api

import (
	"context"

	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/exec"
	"github.com/filecoin-project/go-filecoin/types"
)

// Message is the interface that defines methods to manage various message operations,
// like sending and awaiting mined ones.
type Message interface {
	Send(ctx context.Context, from, to address.Address, val *types.AttoFIL, method string, params ...interface{}) (*cid.Cid, error)
	Query(ctx context.Context, from, to address.Address, method string, params ...interface{}) ([][]byte, *exec.FunctionSignature, error)
	Wait(ctx context.Context, msgCid *cid.Cid, cb func(blk *chain.Block, msg *chain.SignedMessage, receipt *chain.MessageReceipt, signature *exec.FunctionSignature) error) error
}
