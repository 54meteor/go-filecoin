package core

import (
	"context"

	"github.com/filecoin-project/go-filecoin/actor/builtin/account"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm"
	"github.com/filecoin-project/go-filecoin/vm/errors"
)

// Processor is the signature a function used to process blocks.
type Processor func(ctx context.Context, blk *types.Block, st state.Tree) ([]*types.MessageReceipt, error)

// ProcessBlock is the entrypoint for validating the state transitions
// of the messages in a block. When we receive a new block from the
// network ProcessBlock applies the block's messages to the beginning
// state tree ensuring that all transitions are valid, accumulating
// changes in the state tree, and returning the message receipts.
//
// ProcessBlock returns an error if it hits a message the application
// of which would result in an invalid state transition (eg, a
// message to transfer value from an unknown account). ProcessBlock
// can return one of three kinds of errors (see ApplyMessage: fault
// error, permanent error, temporary error). For the purposes of
// block validation the caller probably doesn't care if the error
// was temporary or permanent; either way the block has a bad
// message and should be thrown out. Caller should always differentiate
// a fault error as it signals something Very Bad has happened
// (eg, disk corruption).
//
// To be clear about intent: if ProcessBlock returns an ApplyError
// it is signaling that the message should not have been included
// in the block. If no error is returned this means that the
// message was applied, BUT SUCCESSFUL APPLICATION DOES NOT
// NECESSARILY MEAN THE IMPLIED CALL SUCCEEDED OR SENDER INTENT
// WAS REALIZED. It just means that the transition if any was
// valid. For example, a message that errors out in the VM
// will in many cases be successfully applied even though an
// error was thrown causing any state changes to be rolled back.
// See comments on ApplyMessage for specific intent.
//
func ProcessBlock(ctx context.Context, blk *types.Block, st state.Tree) (mr []*types.MessageReceipt, err error) {
	ctx = log.Start(ctx, "ProcessBlock")
	defer func() {
		log.SetTag(ctx, "block", blk.Cid())
		log.FinishWithErr(ctx, err)
	}()
	var receipts []*types.MessageReceipt
	emptyReceipts := []*types.MessageReceipt{}
	bh := types.NewBlockHeight(blk.Height)

	for _, msg := range blk.Messages {
		r, err := ApplyMessage(ctx, st, msg, bh)
		// If the message should not have been in the block, bail.
		// TODO: handle faults appropriately at a higher level.
		if errors.IsFault(err) || errors.IsApplyErrorPermanent(err) || errors.IsApplyErrorTemporary(err) {
			return emptyReceipts, err
		} else if err != nil {
			return emptyReceipts, errors.FaultErrorWrap(err, "someone is a bad programmer: must be a fault, perm, or temp error")
		}

		// TODO fritz check caller assumptions about receipts.
		receipts = append(receipts, r)
	}
	return receipts, nil
}

// ApplyMessage attempts to apply a message to a state tree. It is the
// sole driver of state tree transitions in the system. Both block
// validation and mining use this function and we should treat any changes
// to it with extreme care.
//
// If ApplyMessage returns no error then the message was successfully applied
// to the state tree: it did not result in any invalid transitions. As you will see
// below, this does not necessarily mean that the message "succeeded" for some
// senses of "succeeded". We choose therefore to say the message was or was not
// successfully applied.
//
// If ApplyMessage returns an error then the message would've resulted in
// an invalid state transition -- it was not successfully applied. When
// ApplyMessage returns an error one of three predicates will be true:
//   - IsFault(err): a system fault occurred (corrupt disk, violated precondition,
//     etc). This is Bad. Caller should stop doing whatever they are doing and get a doctor.
//     No guarantees are made about the state of the state tree.
//   - IsApplyErrorPermanent: the message was not applied and is unlikely
//     to *ever* be successfully applied (equivalently, it is unlikely to
//     ever result in a valid state transition). For example, the message might
//     attempt to transfer negative value. The message should probably be discarded.
//     All state tree mutations will have been reverted.
//   - IsApplyErrorTemporary: the message was not applied though it is
//     *possible* but not certain that the message may become applyable in
//     the future (eg, nonce is too high). The state was reverted.
//
// Please carefully consider the following intent with respect to messages.
// The intentions span the following concerns:
//   - whether the message was successfully applied: if not don't include it
//     in a block. If so inc sender's nonce and include it in a block.
//   - whether the message might be successfully applied at a later time
//     (IsApplyErrorTemporary) vs not (IsApplyErrorPermanent). If the caller
//     is the mining code it could remove permanently unapplyable messages from
//     the message pool but keep temporarily unapplyable messages around to try
//     applying to a future block.
//   - whether to keep or revert state: should we keep or revert state changes
//     caused by the message and its callees? We always revert state changes
//     from unapplyable messages. We might or might not revert changes from
//     applyable messages.
//
// Specific intentions include:
//   - fault errors: immediately return to the caller no matter what
//   - nonce too low: permanently unapplyable (don't include, revert changes, discard)
// TODO: if we have a re-order of the chain the message with nonce too low could
//       become applyable. Except that we already have a message with that nonce.
//       Maybe give this more careful consideration?
//   - nonce too high: temporarily unapplyable (don't include, revert, keep in pool)
//   - sender account exists but insufficient funds: successfully applied
//       (include it in the block but revert its changes). This an explicit choice
//       to make failing transfers not replayable (just like a bank transfer is not
//       replayable).
//   - sender account does not exist: temporarily unapplyable (don't include, revert,
//       keep in pool). There could be an account-creating message forthcoming.
//       (TODO this is only true while we don't have nonce checking; nonce checking
//       will cover this case in the future)
//   - send to self: permanently unapplyable (don't include in a block, revert changes,
//       discard)
//   - transfer negative value: permanently unapplyable (as above)
//   - all other vmerrors: successfully applied! Include in the block and
//       revert changes. Necessarily all vm errors that are not faults are
//       revert errors.
//   - everything else: successfully applied (include, keep changes)
//
// for example squintinig at this perhaps:
//   - ApplyMessage creates a read-through cache of the state tree
//   - it loads the to and from actor into the cache
//   - changes should accumulate in the actor in callees
//   - nothing deeper than this method has direct access to the state tree
//   - no callee should get a different pointer to the to/from actors
//       (we assume the pointer we have accumulates all the changes)
//   - callees must call GetOrCreate on the cache to create a new actor that will be persisted
//   - ApplyMessage and VMContext.Send() are the only things that should call
//     Send() -- all the user-actor logic goes in ApplyMessage and all the
//     actor-actor logic goes in VMContext.Send
func ApplyMessage(ctx context.Context, st state.Tree, msg *types.Message, bh *types.BlockHeight) (mr *types.MessageReceipt, err error) {
	ctx = log.Start(ctx, "ApplyMessage")
	defer func() {
		log.SetTag(ctx, "message", msg)
	}()
	cachedStateTree := state.NewCachedStateTree(st)

	r, err := attemptApplyMessage(ctx, cachedStateTree, msg, bh)
	if err == nil {
		err = cachedStateTree.Commit(ctx)
		if err != nil {
			return nil, errors.FaultErrorWrap(err, "could not commit state tree")
		}
	} else if errors.IsFault(err) {
		return r, err
	} else if !errors.ShouldRevert(err) {
		return nil, errors.NewFaultError("someone is a bad programmer: only return revert and fault errors")
	}

	// Reject invalid state transitions.
	if err == errAccountNotFound || err == errNonceTooHigh {
		return nil, errors.ApplyErrorTemporaryWrapf(err, "apply message failed")
	} else if err == errSelfSend || err == errNonceTooLow || err == errNonAccountActor || err == vm.ErrCannotTransferNegativeValue {
		return nil, errors.ApplyErrorPermanentWrapf(err, "apply message failed")
	} else if err != nil { // nolint: megacheck
		// Do nothing. All other vm errors are ok: the state was rolled back
		// above but we applied the message successfully. This intentionally
		// includes errInsufficientFunds because we don't want the message
		// to be replayable.
	}

	// At this point we consider the message successfully applied so inc
	// the nonce.
	fromActor, err := st.GetActor(ctx, msg.From)
	if err != nil {
		return nil, errors.FaultErrorWrap(err, "couldn't load from actor")
	}
	fromActor.IncNonce()
	if err := st.SetActor(ctx, msg.From, fromActor); err != nil {
		return nil, errors.FaultErrorWrap(err, "could not set from actor after inc nonce")
	}

	return r, nil
}

var (
	// These errors are only to be used by ApplyMessage; they shouldn't be
	// used in any other context as they are an implementation detail.
	errAccountNotFound = errors.NewRevertError("account not found")
	errNonceTooHigh    = errors.NewRevertError("nonce too high")
	errNonceTooLow     = errors.NewRevertError("nonce too low")
	errNonAccountActor = errors.NewRevertError("message from non-account actor")
	// TODO we'll eventually handle sending to self.
	errSelfSend = errors.NewRevertError("cannot send to self")
)

// ApplyQueryMessage sends a message into the VM to query actor state. Only read-only methods should be called on
// the actor as the state tree will be rolled back after the execution.
func ApplyQueryMessage(ctx context.Context, st state.Tree, msg *types.Message, bh *types.BlockHeight) ([]byte, uint8, error) {
	fromActor, err := st.GetActor(ctx, msg.From)
	if state.IsActorNotFoundError(err) {
		return nil, 1, errAccountNotFound
	} else if err != nil {
		return nil, 1, errors.ApplyErrorPermanentWrapf(err, "failed to get From actor %s", msg.From)
	}

	if msg.From == msg.To {
		return nil, 1, errSelfSend
	}

	toActor, err := st.GetActor(ctx, msg.To)
	if err != nil {
		return nil, 1, errors.ApplyErrorPermanentWrapf(err, "failed to get To actor")
	}

	// guarantees changes won't make it to stored state tree
	cachedSt := state.NewCachedStateTree(st)

	vmCtx := vm.NewVMContext(fromActor, toActor, msg, cachedSt, bh)
	ret, retCode, err := vm.Send(ctx, vmCtx)

	return ret, retCode, err
}

// attemptApplyMessage encapsulates the work of trying to apply the message in order
// to make ApplyMessage more readable. The distinction is that attemptApplyMessage
// should deal with trying got apply the message to the state tree whereas
// ApplyMessage should deal with any side effects and how it should be presented
// to the caller. attemptApplyMessage should only be called from ApplyMessage.
func attemptApplyMessage(ctx context.Context, st *state.CachedTree, msg *types.Message, bh *types.BlockHeight) (*types.MessageReceipt, error) {
	fromActor, err := st.GetActor(ctx, msg.From)
	if state.IsActorNotFoundError(err) {
		return nil, errAccountNotFound
	} else if err != nil {
		return nil, errors.FaultErrorWrapf(err, "failed to get From actor %s", msg.From)
	}

	if msg.From == msg.To {
		// TODO: handle this
		return nil, errSelfSend
	}

	toActor, err := st.GetOrCreateActor(ctx, msg.To, func() (*types.Actor, error) {
		// Addresses are deterministic so sending a message to a non-existent address must not install an actor,
		// else actors could be installed ahead of address activation. So here we create the empty, upgradable
		// actor to collect any balance that may be transferred.
		return &types.Actor{}, nil
	})
	if err != nil {
		return nil, errors.FaultErrorWrap(err, "failed to get To actor")
	}

	// processing an exernal message from an empty actor upgrades it to an account actor.
	if fromActor.Code == nil {
		err = account.UpgradeActor(fromActor)
		if err != nil {
			return nil, errors.FaultErrorWrap(err, "failed to upgrade empty actor")
		}
	}

	// if from actor is not an account actor revert message
	if !fromActor.Code.Equals(types.AccountActorCodeCid) {
		return nil, errNonAccountActor
	}

	if msg.Nonce < fromActor.Nonce {
		return nil, errNonceTooLow
	}
	if msg.Nonce > fromActor.Nonce {
		return nil, errNonceTooHigh
	}

	vmCtx := vm.NewVMContext(fromActor, toActor, msg, st, bh)
	ret, exitCode, vmErr := vm.Send(ctx, vmCtx)
	if errors.IsFault(vmErr) {
		return nil, vmErr
	}

	var retBytes []byte
	if vmErr != nil {
		// ensure error strings that are too long don't cause another failure
		retBytes = truncate([]byte(vmErr.Error()), types.ReturnValueLength)
	} else if len(ret) > types.ReturnValueLength {
		vmErr = errors.RevertErrorWrap(types.ErrReturnValueTooLarge, "")
		retBytes = truncate([]byte(vmErr.Error()), types.ReturnValueLength)
	} else {
		retBytes = ret
	}

	retVal, retSize, err := types.SliceToReturnValue(retBytes)
	if err != nil {
		// this should never happen, as we take care of larger values above, if it does
		// then we need to fail
		return nil, errors.FaultErrorWrap(err, "failed to convert to return value")
	}

	return types.NewMessageReceipt(exitCode, retVal, retSize), vmErr
}

func truncate(val []byte, size int) []byte {
	if len(val) <= size {
		return val
	}

	return val[0:size]
}
