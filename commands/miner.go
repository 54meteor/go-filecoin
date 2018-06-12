package commands

import (
	"io"

	"gx/ipfs/QmUf5GFfV2Be3UtSAPKDVkoRd1TwEBTmx9TSSCFGGjNgdQ/go-ipfs-cmds"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	"gx/ipfs/QmceUdzxkimdYsgtX733uNgzf1DLHyBKN6ehGSp85ayppM/go-ipfs-cmdkit"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/node"
	"github.com/filecoin-project/go-filecoin/types"
)

var minerCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Manage miner operations",
	},
	Subcommands: map[string]*cmds.Command{
		"create":  minerCreateCmd,
		"add-ask": minerAddAskCmd,
	},
}

var minerCreateCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Create a new file miner",
		ShortDescription: `Issues a new message to the network to create the miner. Then waits for the
message to be mined as this is required to return the address of the new miner.`,
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("pledge", true, false, "the size of the pledge for the miner"),
		cmdkit.StringArg("collateral", true, false, "the amount of collateral to be sent"),
	},
	Options: []cmdkit.Option{
		cmdkit.StringOption("from", "address to send from"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) (err error) {
		req.Context = log.Start(req.Context, "minerCreateCmd")
		defer func() {
			log.SetTags(req.Context, map[string]interface{}{
				"args": req.Arguments,
				"path": req.Path,
			})
			log.FinishWithErr(req.Context, err)
		}()
		n := GetNode(env)

		fromAddr, err := fromAddress(req.Options, n)
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "from-address", fromAddr.String())

		pledge, ok := types.NewBytesAmountFromString(req.Arguments[0], 10)
		if !ok {
			return ErrInvalidPledge
		}
		log.SetTag(req.Context, "pledge", pledge.String())

		collateral, ok := types.NewTokenAmountFromString(req.Arguments[1], 10)
		if !ok {
			return ErrInvalidCollateral
		}
		log.SetTag(req.Context, "collateral", collateral.String())

		addr, err := n.CreateMiner(req.Context, fromAddr, *pledge, *collateral)
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "addr", addr.String())

		return re.Emit(addr)
	},
	Type: types.Address{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, a *types.Address) error {
			return PrintString(w, a)
		}),
	},
}

var minerAddAskCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Add an ask to the storage market",
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("miner", true, false, "the address of the miner owning the ask"),
		cmdkit.StringArg("size", true, false, "size in bytes of the ask"),
		cmdkit.StringArg("price", true, false, "the price of the ask"),
	},
	Options: []cmdkit.Option{
		cmdkit.StringOption("from", "address to send the ask from"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) (err error) {
		req.Context = log.Start(req.Context, "minerAddAskCmd")
		defer func() {
			log.SetTags(req.Context, map[string]interface{}{
				"args": req.Arguments,
				"path": req.Path,
			})
			log.FinishWithErr(req.Context, err)
		}()
		n := GetNode(env)

		fromAddr, err := fromAddress(req.Options, n)
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "from-address", fromAddr.String())

		minerAddr, err := types.NewAddressFromString(req.Arguments[0])
		if err != nil {
			return errors.Wrap(err, "invalid miner address")
		}
		log.SetTag(req.Context, "miner-address", minerAddr.String())

		size, ok := types.NewBytesAmountFromString(req.Arguments[1], 10)
		if !ok {
			return ErrInvalidSize
		}
		log.SetTag(req.Context, "size", size.String())

		price, ok := types.NewTokenAmountFromString(req.Arguments[2], 10)
		if !ok {
			return ErrInvalidPrice
		}
		log.SetTag(req.Context, "price", price.String())

		params, err := abi.ToEncodedValues(price, size)
		if err != nil {
			return err
		}

		msg, err := node.NewMessageWithNextNonce(req.Context, n, fromAddr, minerAddr, nil, "addAsk", params)
		if err != nil {
			return err
		}

		if err := n.AddNewMessage(req.Context, msg); err != nil {
			return err
		}

		c, err := msg.Cid()
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "msg", c.String())

		re.Emit(c) // nolint: errcheck

		return nil
	},
	Type: cid.Cid{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, c *cid.Cid) error {
			return PrintString(w, c)
		}),
	},
}
