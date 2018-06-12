// Package vm implements the Filecoin VM
// This means this is the _only_ part of the code base that should concern itself
// with passing data between VM boundaries.
package vm

import (
	"context"

	logging "gx/ipfs/QmPuosXfnE2Xrdiw95D78AhW41GYwGqpstKMf4TEsE4f33/go-log"
	cbor "gx/ipfs/QmRVSCwQtW1rjHCay9NqKXDwbtKTgDcN4iY7PrpSqfKM5D/go-ipld-cbor"

	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"
)

var log = logging.Logger("vm")

var (
	// Most errors should live in the actors that throw them. However some
	// errors will be pervasive so we define them centrally here.

	// ErrCannotTransferNegativeValue signals a transfer error, value must be positive.
	ErrCannotTransferNegativeValue = errors.NewRevertError("cannot transfer negative values")
	// ErrInsufficientBalance signals insufficient balance for a transfer.
	ErrInsufficientBalance = errors.NewRevertError("not enough balance")
)

// Send executes a message pass inside the VM. If error is set it
// will always satisfy either ShouldRevert() or IsFault().
func Send(ctx context.Context, vmCtx *Context) (b [][]byte, ret uint8, err error) {
	ctx = log.Start(ctx, "Send")
	defer func() {
		log.SetTag(ctx, "message", vmCtx.message)
		log.FinishWithErr(ctx, err)
	}()
	deps := sendDeps{
		transfer: transfer,
	}

	return send(ctx, deps, vmCtx)
}

type sendDeps struct {
	transfer func(*types.Actor, *types.Actor, *types.TokenAmount) error
}

// send executes a message pass inside the VM. It exists alongside Send so that we can inject its dependencies during test.
func send(ctx context.Context, deps sendDeps, vmCtx *Context) ([][]byte, uint8, error) {
	if vmCtx.message.Value != nil {
		if err := deps.transfer(vmCtx.from, vmCtx.to, vmCtx.message.Value); err != nil {
			return nil, 1, err
		}
	}

	if vmCtx.message.Method == "" {
		// if only tokens are transferred there is no need for a method
		// this means we can shortcircuit execution
		return nil, 0, nil
	}

	toExecutable, err := vmCtx.state.GetBuiltinActorCode(vmCtx.to.Code)
	if err != nil {
		return nil, 1, errors.FaultErrorWrap(err, "unable to load code for To actor")
	}

	if !toExecutable.Exports().Has(vmCtx.message.Method) {
		return nil, 1, errors.NewRevertErrorf("missing export: %s", vmCtx.message.Method)
	}

	r, code, err := actor.MakeTypedExport(toExecutable, vmCtx.message.Method)(vmCtx)
	if r != nil {
		var rv [][]byte
		err = cbor.DecodeInto(r, &rv)
		if err != nil {
			return nil, 1, errors.NewRevertErrorf("method return doesn't decode as array: %s", err)
		}
		return rv, code, err
	}
	return nil, code, err
}

func transfer(fromActor, toActor *types.Actor, value *types.TokenAmount) error {
	if value.IsNegative() {
		return ErrCannotTransferNegativeValue
	}

	if fromActor.Balance.LessThan(value) {
		return ErrInsufficientBalance
	}

	if toActor.Balance == nil {
		toActor.Balance = types.ZeroToken // This would be unsafe if TokenAmount could be mutated.
	}
	fromActor.Balance = fromActor.Balance.Sub(value)
	toActor.Balance = toActor.Balance.Add(value)

	return nil
}
