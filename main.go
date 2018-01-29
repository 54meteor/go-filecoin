package main

import (
	"context"
	"fmt"
	"os"

	commands "github.com/filecoin-project/go-filecoin/commands"
	logging "gx/ipfs/QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52/go-log"

	cmds "gx/ipfs/Qmc5paX4ECBARnAKkcAmUYHBGor228Tkfxeya3Nu2KRL46/go-ipfs-cmds"
	cmdcli "gx/ipfs/Qmc5paX4ECBARnAKkcAmUYHBGor228Tkfxeya3Nu2KRL46/go-ipfs-cmds/cli"
	cmdhttp "gx/ipfs/Qmc5paX4ECBARnAKkcAmUYHBGor228Tkfxeya3Nu2KRL46/go-ipfs-cmds/http"
	cmdkit "gx/ipfs/QmceUdzxkimdYsgtX733uNgzf1DLHyBKN6ehGSp85ayppM/go-ipfs-cmdkit"
)

var log = logging.Logger("filecoin")

func fail(v ...interface{}) {
	fmt.Println(v)
	os.Exit(1)
}

func apiEnv() string {
	return os.Getenv("FIL_API")
}

func main() {
	daemonRunning, err := commands.DaemonIsRunning()
	if err != nil {
		fail(err)
	}

	req, err := cmdcli.Parse(context.Background(), os.Args[1:], os.Stdin, commands.RootCmd)
	if err != nil {
		panic(err)
	}

	if daemonRunning {
		if req.Command == commands.DaemonCmd { // this is a hack, go-ipfs does this slightly better
			fmt.Println("daemon already running...")
			return
		}
		api := req.Options["api"].(string)
		if ae := apiEnv(); ae != "" {
			api = ae
		}
		client := cmdhttp.NewClient(api, cmdhttp.ClientWithAPIPrefix("/api"))

		// send request to server
		res, err := client.Send(req)
		if err != nil {
			panic(err)
		}

		encType := cmds.GetEncoding(req)
		enc := req.Command.Encoders[encType]
		if enc == nil {
			enc = cmds.Encoders[encType]
		}

		// create an emitter
		re, retCh := cmdcli.NewResponseEmitter(os.Stdout, os.Stderr, enc, req)

		if pr, ok := req.Command.PostRun[cmds.CLI]; ok {
			re = pr(req, re)
		}

		wait := make(chan struct{})
		// copy received result into cli emitter
		go func() {
			err = cmds.Copy(re, res)
			if err != nil {
				re.SetError(err, cmdkit.ErrNormal|cmdkit.ErrFatal)
			}
			close(wait)
		}()

		// wait until command has returned and exit
		ret := <-retCh
		<-wait
		os.Exit(ret)
	} else {
		req.Options[cmds.EncLong] = cmds.Text

		// create an emitter
		re, retCh := cmdcli.NewResponseEmitter(os.Stdout, os.Stderr, req.Command.Encoders[cmds.Text], req)

		if pr, ok := req.Command.PostRun[cmds.CLI]; ok {
			re = pr(req, re)
		}

		wait := make(chan struct{})
		// call command in background
		go func() {
			defer close(wait)

			err = commands.RootCmd.Call(req, re, nil)
			if err != nil {
				panic(err)
			}
		}()

		// wait until command has returned and exit
		ret := <-retCh
		<-wait

		os.Exit(ret)
	}
}
