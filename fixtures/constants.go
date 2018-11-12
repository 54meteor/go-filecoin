package fixtures

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sort"

	"github.com/filecoin-project/go-filecoin/util/project"

	cid "gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"

	"github.com/filecoin-project/go-filecoin/types"
)

// The file used to build these addresses can be found in:
// $GOPATH/src/github.com/filecoin-project/go-filecoin/fixtures/setup.json
//
// If said file is modified these addresses will need to change as well
// rebuild using
// TODO: move to build script
// https://github.com/filecoin-project/go-filecoin/issues/921
// cat ./fixtures/setup.json | ./gengen/gengen --json --keypath fixtures > fixtures/genesis.car 2> fixtures/gen.json

// TestAddresses is a list of pregenerated addresses.
var TestAddresses []string

// testKeys is a list of filenames, which contain the private keys of the pregenerated addresses.
var testKeys []string

// TestMiners is a list of pregenerated miner acccounts. They are owned by the matching TestAddress.
var TestMiners []string

type detailsStruct struct {
	Keys   []*types.KeyInfo
	Miners []struct {
		Owner   int
		Address string
		Power   uint64
	}
	GenesisCid *cid.Cid
}

func init() {
	detailsPath := project.Root("fixtures/gen.json")
	detailsFile, err := os.Open(detailsPath)
	if err != nil {
		// fmt.Printf("Fixture data not found. Skipping fixture initialization: %s\n", err)
		return
	}
	defer func() {
		if err := detailsFile.Close(); err != nil {
			panic(err)
		}
	}()
	detailsFileBytes, err := ioutil.ReadAll(detailsFile)
	if err != nil {
		panic(err)
	}
	var details detailsStruct
	if err := json.Unmarshal(detailsFileBytes, &details); err != nil {
		panic(err)
	}

	var keys []int
	for key := range details.Keys {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	miners := details.Miners

	for _, key := range keys {
		info := details.Keys[key]
		addr, err := info.Address()
		if err != nil {
			panic(err)
		}
		TestAddresses = append(TestAddresses, addr.String())
		testKeys = append(testKeys, fmt.Sprintf("%d.key", key))
	}

	for _, miner := range miners {
		TestMiners = append(TestMiners, miner.Address)
	}
}

// KeyFilePaths returns the paths to the wallets of the testaddresses
func KeyFilePaths() []string {
	res := make([]string, len(testKeys))
	for i, k := range testKeys {
		res[i] = project.Root("fixtures/", k)
	}

	return res
}

// Lab week cluster addrs
const (
	filecoinBootstrap0 string = "/dns4/test.kittyhawk.wtf/tcp/9000/ipfs/Qmd6xrWYHsxivfakYRy6MszTpuAiEoFbgE1LWw4EvwBpp4"
	filecoinBootstrap1 string = "/dns4/test.kittyhawk.wtf/tcp/9001/ipfs/QmXq6XEYeEmUzBFuuKbVEGgxEpVD4xbSkG2Rhek6zkFMp4"
	filecoinBootstrap2 string = "/dns4/test.kittyhawk.wtf/tcp/9002/ipfs/QmXhxqTKzBKHA5FcMuiKZv8YaMPwpbKGXHRVZcFB2DX9XY"
	filecoinBootstrap3 string = "/dns4/test.kittyhawk.wtf/tcp/9003/ipfs/QmZGDLdQLUTi7uYTNavKwCd7SBc5KMfxzWxAyvqRQvwuiV"
	filecoinBootstrap4 string = "/dns4/test.kittyhawk.wtf/tcp/9004/ipfs/QmZRnwmCjyNHgeNDiyT8mXRtGhP6uSzgHtrozc42crmVbg"
)

// LabWeekRelayAddrs are the dns multiaddrs for the nodes of the filecoin
// cluster that run relays
var LabWeekRelayAddrs = []string{
	filecoinBootstrap0,
}

// LabWeekBootstrapAddrs are the dns multiaddrs for the nodes of the filecoin
// cluster running at lab week.
var LabWeekBootstrapAddrs = []string{
	filecoinBootstrap1,
	filecoinBootstrap2,
	filecoinBootstrap3,
	filecoinBootstrap4,
}
