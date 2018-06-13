package commands

import (
	"encoding/hex"
	"fmt"
	"io"

	cmds "gx/ipfs/QmUf5GFfV2Be3UtSAPKDVkoRd1TwEBTmx9TSSCFGGjNgdQ/go-ipfs-cmds"
	chunk "gx/ipfs/QmWo8jYc19ppG7YoTsrr2kEtLRbARTJho5oNXFTR6B7Peq/go-ipfs-chunker"
	cid "gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	cmdkit "gx/ipfs/QmceUdzxkimdYsgtX733uNgzf1DLHyBKN6ehGSp85ayppM/go-ipfs-cmdkit"

	imp "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/importer"
	dag "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/merkledag"
	uio "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/unixfs/io"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/actor/builtin/storagemarket"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/node"
	"github.com/filecoin-project/go-filecoin/types"
)

var clientCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Manage client operations",
	},
	Subcommands: map[string]*cmds.Command{
		"add-bid":      clientAddBidCmd,
		"cat":          clientCatCmd,
		"import":       clientImportDataCmd,
		"propose-deal": clientProposeDealCmd,
		"query-deal":   clientQueryDealCmd,
	},
}

var clientAddBidCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Add a bid to the storage market",
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("size", true, false, "size in bytes of the bid"),
		cmdkit.StringArg("price", true, false, "the price of the bid"),
	},
	Options: []cmdkit.Option{
		cmdkit.StringOption("from", "address to send the bid from"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) (err error) {
		req.Context = log.Start(req.Context, "clientAddBidCmd")
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

		size, ok := types.NewBytesAmountFromString(req.Arguments[0], 10)
		if !ok {
			return ErrInvalidSize
		}
		log.SetTag(req.Context, "size", size.String())

		price, ok := types.NewTokenAmountFromString(req.Arguments[1], 10)
		if !ok {
			return ErrInvalidPrice
		}
		log.SetTag(req.Context, "price", price.String())

		funds := price.CalculatePrice(size)
		log.SetTag(req.Context, "funds", funds.String())

		params, err := abi.ToEncodedValues(price, size)
		if err != nil {
			return err
		}

		msg, err := node.NewMessageWithNextNonce(req.Context, n, fromAddr, address.StorageMarketAddress, funds, "addBid", params)
		if err != nil {
			return err
		}

		err = n.AddNewMessage(req.Context, msg)
		if err != nil {
			return err
		}

		msgCid, err := msg.Cid()
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "message", msg)

		re.Emit(msgCid) // nolint: errcheck

		return nil
	},
	Type: cid.Cid{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, c *cid.Cid) error {
			return PrintString(w, c)
		}),
	},
}

var clientCatCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Read out data stored on the network",
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("cid", true, false, "cid of data to read"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		nd := GetNode(env)

		// TODO: this goes back to 'how is data stored and referenced'
		// For now, lets just do things the ipfs way.

		c, err := cid.Decode(req.Arguments[0])
		if err != nil {
			return err
		}

		ds := dag.NewDAGService(nd.Blockservice)

		data, err := ds.Get(req.Context, c)
		if err != nil {
			return err
		}

		dr, err := uio.NewDagReader(req.Context, data, ds)
		if err != nil {
			return err
		}

		re.Emit(dr) // nolint: errcheck
		return nil
	},
}

var clientProposeDealCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "propose a deal",
	},
	Options: []cmdkit.Option{
		// TODO: use UintOption once its fixed, ref go-ipfs-cmdkit#15
		cmdkit.IntOption("ask", "ID of ask to propose a deal for"),
		cmdkit.IntOption("bid", "ID of bid to propose a deal for"),
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("data", true, false, "bid to propose a deal with"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) (err error) {
		req.Context = log.Start(req.Context, "clientProposeDealCmd")
		defer func() {
			log.SetTags(req.Context, map[string]interface{}{
				"args": req.Arguments,
				"path": req.Path,
			})
			log.FinishWithErr(req.Context, err)
		}()
		nd := GetNode(env)

		askID, ok := req.Options["ask"].(int)
		if !ok {
			return fmt.Errorf("must specify an ask")
		}
		log.SetTag(req.Context, "ask-id", askID)

		bidID, ok := req.Options["bid"].(int)
		if !ok {
			return fmt.Errorf("must specify a bid")
		}
		log.SetTag(req.Context, "bid-id", bidID)

		data, err := cid.Decode(req.Arguments[0])
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "data", data.String())

		defaddr := nd.RewardAddress()

		propose := &node.DealProposal{
			ClientSig: string(defaddr[:]), // TODO: actual crypto
			Deal: &storagemarket.Deal{
				Ask:     uint64(askID),
				Bid:     uint64(bidID),
				DataRef: data,
			},
		}

		resp, err := nd.StorageClient.ProposeDeal(req.Context, propose)
		if err != nil {
			return err
		}
		log.SetTag(req.Context, "deal-status", resp.State.String())
		log.SetTag(req.Context, "deal-id", resp.ID)

		re.Emit(resp) // nolint: errcheck
		return nil
	},
	Type: node.DealResponse{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, resp *node.DealResponse) error {
			fmt.Fprintf(w, "Status: %s\n", resp.State.String())
			fmt.Fprintf(w, "ID: %x\n", resp.ID)
			return nil
		}),
	},
}

var clientQueryDealCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "query a deals status",
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("id", true, false, "hex ID of deal to query"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		nd := GetNode(env)

		idslice, err := hex.DecodeString(req.Arguments[0])
		if err != nil {
			return err
		}

		if len(idslice) != 32 {
			re.SetError("id must be 32 bytes long", cmdkit.ErrNormal)
			return nil
		}

		var id [32]byte
		copy(id[:], idslice)

		resp, err := nd.StorageClient.QueryDeal(req.Context, id)
		if err != nil {
			return err
		}

		re.Emit(resp) // nolint: errcheck
		return nil
	},
	Type: node.DealResponse{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, resp *node.DealResponse) error {
			fmt.Fprintf(w, "Status: %s\n", resp.State.String())
			fmt.Fprintf(w, "ID: %x\n", resp.ID)
			fmt.Fprintf(w, "Message: %s\n", resp.Message)
			if resp.MsgCid != nil {
				fmt.Fprintf(w, "MsgCid: %s\n", resp.MsgCid)
			}
			return nil
		}),
	},
}

var clientImportDataCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "import data into the local node",
	},
	Arguments: []cmdkit.Argument{
		cmdkit.FileArg("file", true, false, "path to file to import").EnableStdin(),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) error {
		nd := GetNode(env)

		ds := dag.NewDAGService(nd.Blockservice)

		fi, err := req.Files.NextFile()
		if err != nil {
			return err
		}

		spl := chunk.DefaultSplitter(fi)

		out, err := imp.BuildDagFromReader(ds, spl)
		if err != nil {
			return err
		}

		re.Emit(out.Cid()) // nolint: errcheck
		return nil
	},
	Type: cid.Cid{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, c *cid.Cid) error {
			return PrintString(w, c)
		}),
	},
}
