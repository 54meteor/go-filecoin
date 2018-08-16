package commands

import (
	"fmt"
	"io"
	"strings"

	cmds "gx/ipfs/QmVTmXZC2yE38SDKRihn96LXX6KwBWgzAg8aCDZaMirCHm/go-ipfs-cmds"
	ma "gx/ipfs/QmYmsdtJ3HsodkePE3eU3TsCaP2YvPZJ4LoXnNkDE5Tpt7/go-multiaddr"
	cmdkit "gx/ipfs/QmdE4gMduCKCGAcczM2F5ioYDfdeKuPix138wrES1YSr7f/go-ipfs-cmdkit"

	"github.com/filecoin-project/go-filecoin/api"
)

// swarmCmd contains swarm commands.
var swarmCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Interact with the swarm.",
		ShortDescription: `
'go-filecoin swarm' is a tool to manipulate the libp2p swarm. The swarm is the
component that opens, listens for, and maintains connections to other
libp2p peers on the internet.
`,
	},
	Subcommands: map[string]*cmds.Command{
		"addrs":   swarmAddrsCmd,
		"connect": swarmConnectCmd,
		"peers":   swarmPeersCmd,
	},
}

var swarmPeersCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "List peers with open connections.",
		ShortDescription: `
'ipfs swarm peers' lists the set of peers this node is connected to.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption("verbose", "v", "display all extra information"),
		cmdkit.BoolOption("streams", "Also list information about open streams for each peer"),
		cmdkit.BoolOption("latency", "Also list information about latency to each peer"),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) {
		verbose, _ := req.Options["verbose"].(bool)
		latency, _ := req.Options["latency"].(bool)
		streams, _ := req.Options["streams"].(bool)

		out, err := GetAPI(env).Swarm().Peers(req.Context, verbose, latency, streams)
		if err != nil {
			re.SetError(err, cmdkit.ErrNormal)
			return
		}

		re.Emit(&out) // nolint: errcheck
	},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, ci *api.SwarmConnInfos) error {
			pipfs := ma.ProtocolWithCode(ma.P_IPFS).Name
			for _, info := range ci.Peers {
				ids := fmt.Sprintf("/%s/%s", pipfs, info.Peer)
				if strings.HasSuffix(info.Addr, ids) {
					fmt.Fprintf(w, "%s", info.Addr) // nolint: errcheck
				} else {
					fmt.Fprintf(w, "%s%s", info.Addr, ids) // nolint: errcheck
				}
				if info.Latency != "" {
					fmt.Fprintf(w, " %s", info.Latency) // nolint: errcheck
				}
				fmt.Fprintln(w) // nolint: errcheck

				for _, s := range info.Streams {
					if s.Protocol == "" {
						s.Protocol = "<no protocol name>"
					}

					fmt.Fprintf(w, "  %s\n", s.Protocol) // nolint: errcheck
				}
			}

			return nil
		}),
	},
	Type: api.SwarmConnInfos{},
}

var swarmConnectCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Open connection to a given address.",
		ShortDescription: `
'go-filecoin swarm connect' opens a new direct connection to a peer address.

The address format is a multiaddr:

go-filecoin swarm connect /ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ
`,
	},
	Arguments: []cmdkit.Argument{
		cmdkit.StringArg("address", true, true, "Address of peer to connect to.").EnableStdin(),
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) {
		out, err := GetAPI(env).Swarm().Connect(req.Context, req.Arguments)
		if err != nil {
			re.SetError(err, cmdkit.ErrNormal)
			return
		}

		re.Emit(out) // nolint: errcheck
	},
	Type: []api.SwarmConnectResult{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, res *[]api.SwarmConnectResult) error {
			for _, a := range *res {
				fmt.Fprintf(w, "connect %s success\n", a.Peer) // nolint: errcheck
			}
			return nil
		}),
	},
}

var swarmAddrsCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "List known addresses. Useful for debugging.",
		ShortDescription: `
'go-filecoin swarm addrs' lists all addresses this node is aware of.
`,
	},
	Run: func(req *cmds.Request, re cmds.ResponseEmitter, env cmds.Environment) {
		out, err := GetAPI(env).Swarm().Addrs(req.Context)
		if err != nil {
			re.SetError(err, cmdkit.ErrNormal)
			return
		}

		re.Emit(out) // nolint: errcheck
	},
	Type: map[string][]string{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, res *map[string][]string) error {
			for id, addr := range *res {
				fmt.Fprintf(w, "%s\n", id) // nolint: errcheck
				for _, a := range addr {
					fmt.Fprintf(w, "\t%s", a)
				}
			}
			return nil
		}),
	},
}
