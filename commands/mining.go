package commands

import (
	"context"
	"fmt"
	"io"

	cmds "gx/ipfs/QmUf5GFfV2Be3UtSAPKDVkoRd1TwEBTmx9TSSCFGGjNgdQ/go-ipfs-cmds"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	cmdkit "gx/ipfs/QmceUdzxkimdYsgtX733uNgzf1DLHyBKN6ehGSp85ayppM/go-ipfs-cmdkit"

	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/mining"
	"github.com/filecoin-project/go-filecoin/state"
)

var miningCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Manage mining operations",
	},
	Subcommands: map[string]*cmds.Command{
		"once":  miningOnceCmd,
		"start": miningStartCmd,
		"stop":  miningStopCmd,
	},
}

var miningOnceCmd = &cmds.Command{
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		fcn := GetNode(env)

		cur := fcn.ChainMgr.GetBestBlock()

		addrs := fcn.Wallet.Addresses()
		if len(addrs) == 0 {
			return ErrNoWalletAddresses
		}
		rewardAddr := addrs[0]

		blockGenerator := mining.NewBlockGenerator(fcn.MsgPool, func(ctx context.Context, ts core.TipSet) (state.Tree, error) {
			return fcn.ChainMgr.LoadStateTreeTS(ctx, ts)
		}, func(ctx context.Context, ts core.TipSet) (state.Tree, error) {
			return fcn.ChainMgr.LoadParentStateTree(ctx, ts)
		}, core.ApplyMessages)
		// TODO(EC): Need to read best tipsets from storage and pass in. See also Node::StartMining().
		ts, err := core.NewTipSet(cur)
		if err != nil {
			return err
		}
		res := mining.MineOnce(req.Context, mining.NewWorker(blockGenerator), ts, rewardAddr)
		if res.Err != nil {
			return res.Err
		}
		if err := fcn.AddNewBlock(req.Context, res.NewBlock); err != nil {
			return err
		}
		re.Emit(res.NewBlock.Cid()) // nolint: errcheck

		return nil
	},
	Type: cid.Cid{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, c *cid.Cid) error {
			fmt.Fprintln(w, c)
			return nil
		}),
	},
}

var miningStartCmd = &cmds.Command{
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		if err := GetNode(env).StartMining(); err != nil {
			return err
		}
		re.Emit("Started mining\n") // nolint: errcheck

		return nil
	},
}

var miningStopCmd = &cmds.Command{
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		GetNode(env).StopMining()
		re.Emit("Stopped mining\n") // nolint: errcheck

		return nil
	},
}
