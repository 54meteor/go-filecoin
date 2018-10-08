package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/filecoin-project/go-filecoin/address"
	logging "gx/ipfs/QmekXSLDnB9iTHRsKsidP6oN89vGGGRN27JP6gz9PSNHzR/go-log"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
)

var log = logging.Logger("faucet")

func init() {
	// Info level
	logging.SetAllLoggers(4)
}

func main() {
	filapi := flag.String("fil-api", "localhost:3453", "set the api address of the filecoin node to use")
	filwal := flag.String("fil-wallet", "", "(required) set the wallet address for the controlled filecoin node to send funds from")
	faucetval := flag.Int64("faucet-val", 500, "set the amount of fil to pay to each requester")
	flag.Parse()

	if *filwal == "" {
		fmt.Println("ERROR: must provide wallet address to send funds from")
		flag.Usage()
		return
	}

	http.HandleFunc("/", displayForm)
	http.HandleFunc("/tap", func(w http.ResponseWriter, r *http.Request) {
		target := r.FormValue("target")
		if target == "" {
			http.Error(w, "must specify a target address to send FIL to", 400)
			return
		}
		log.Infof("Request to send funds to: %s", target)

		addr, err := address.NewFromString(target)
		if err != nil {
			log.Errorf("Failed to parse target address: %s %s", target, err)
			http.Error(w, fmt.Sprintf("Failed to parse target address %s %s", target, err.Error()), 400)
			return
		}

		reqStr := fmt.Sprintf("http://%s/api/message/send?arg=%s&value=%d&from=%s", *filapi, addr.String(), *faucetval, *filwal)
		log.Infof("Request URL: %s", reqStr)

		resp, err := http.Post(reqStr, "application/json", nil)
		if err != nil {
			log.Errorf("Failed to Post request. Status: %s Error: %s", resp.Status, err)
			http.Error(w, err.Error(), 500)
			return
		}

		out, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Errorf("failed to read response body: %s", err)
			http.Error(w, "failed to read response", 500)
			return
		}
		if resp.StatusCode != 200 {
			log.Errorf("Status: %s Body: %s", resp.Status, string(out))
			http.Error(w, "failed to send funds", 500)
			return
		}

		// result should be a message cid
		var msgcid cid.Cid
		if err := json.Unmarshal(out, &msgcid); err != nil {
			log.Errorf("json unmarshal from response failed: %s", err)
			log.Errorf("response data was: %s", string(out))
			http.Error(w, "faucet unmarshal failed", 500)
			return
		}

		log.Info("Request successful. Message CID: %s", msgcid.String())
		w.WriteHeader(200)
		fmt.Fprint(w, "Success! Message CID: ") // nolint: errcheck
		fmt.Fprintln(w, msgcid.String())        // nolint: errcheck
	})

	panic(http.ListenAndServe(":9797", nil))
}

const form = `
<html>
	<body>
		<h1> What is your wallet address </h1>
		<p> You can find this by running: </p>
		<tt> go-filecoin wallet addrs ls </tt>
		<p> Address: </p>
		<form action="/tap" method="post">
			<input type="text" name="target" size="30" />
			<input type="submit" value="Submit" size="30" />
		</form>
	</body>
</html>
`

func displayForm(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, form) // nolint: errcheck
}
