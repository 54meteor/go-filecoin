package pluginlocalfilecoin

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gx/ipfs/QmRKLtwMw131aK7ugC3G7ybpumMz78YrJe5dzneyindvG1/go-multiaddr"
	badgerds "gx/ipfs/QmVoK2ivqzp5ZgWiEdBNFbKH7nzf9C4wPYr8cH7CGPMHtC/go-ds-badger"
	logging "gx/ipfs/QmcuXC5cxs79ro2cUuHs4HQ2bkDLJUYokwL8aivcX6HW3C/go-log"
	gds "gx/ipfs/Qmf4xQhNomPNhrtZc67qSnfJSjxjXs9LWvknJtSXwimPrM/go-datastore"

	"github.com/ipfs/iptb/testbed/interfaces"
	"github.com/ipfs/iptb/util"
	"github.com/pkg/errors"

	"github.com/filecoin-project/go-filecoin/config"
	"github.com/filecoin-project/go-filecoin/tools/iptb-plugins/filecoin"
)

// PluginName is the name of the plugin
var PluginName = "localfilecoin"

var log = logging.Logger(PluginName)

var ErrIsAlive = errors.New("node is already running") // nolint: golint
var errTimeout = errors.New("timeout")

// Localfilecoin represents a filecoin node
type Localfilecoin struct {
	dir string
	// cache
	peerid  string
	apiaddr multiaddr.Multiaddr
}

var NewNode testbedi.NewNodeFunc // nolint: golint
const dsName = "iptb-data"

var key_peerID = gds.NewKey("peerid")

func init() {
	NewNode = func(dir string, attrs map[string]string) (testbedi.Core, error) {
		if _, err := exec.LookPath("go-filecoin"); err != nil {
			return nil, err
		}
		return &Localfilecoin{
			dir: dir,
		}, nil
	}
}

func (l *Localfilecoin) db() (*badgerds.Datastore, error) {
	return badgerds.NewDatastore(filepath.Join(l.dir, dsName), nil)
}

/** Core Interface **/

// Init runs the node init process.
func (l *Localfilecoin) Init(ctx context.Context, args ...string) (testbedi.Output, error) {
	args = append([]string{"go-filecoin", "init"}, args...)
	output, oerr := l.RunCmd(ctx, nil, args...)
	if oerr != nil {
		return nil, oerr
	}

	icfg, err := l.GetConfig()
	if err != nil {
		return nil, err
	}

	lcfg := icfg.(*config.Config)

	if err := lcfg.Set("bootstrap.addresses", "[]"); err != nil {
		return nil, err
	}

	if err := lcfg.Set("api.address", `"/ip4/127.0.0.1/tcp/0"`); err != nil {
		return nil, err
	}

	if err := lcfg.Set("swarm.address", `"/ip4/127.0.0.1/tcp/0"`); err != nil {
		return nil, err
	}

	if err := l.WriteConfig(lcfg); err != nil {
		return nil, err
	}

	l.cachePeerID()

	return output, oerr
}

// Start starts the node process.
func (l *Localfilecoin) Start(ctx context.Context, wait bool, args ...string) (testbedi.Output, error) {
	alive, err := l.isAlive()
	if err != nil {
		return nil, err
	}

	if alive {
		return nil, ErrIsAlive
	}

	dir := l.dir
	repoFlag := fmt.Sprintf("--repodir=%s", l.Dir())
	dargs := append([]string{"daemon", repoFlag}, args...)
	cmd := exec.CommandContext(ctx, "go-filecoin", dargs...)
	cmd.Dir = dir

	cmd.Env, err = l.env()
	if err != nil {
		return nil, err
	}

	iptbutil.SetupOpt(cmd)

	stdout, err := os.Create(filepath.Join(dir, "daemon.stdout"))
	if err != nil {
		return nil, err
	}

	stderr, err := os.Create(filepath.Join(dir, "daemon.stderr"))
	if err != nil {
		return nil, err
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	pid := cmd.Process.Pid
	if pid == 0 {
		panic("here")
	}

	l.Infof("Started daemon: %s, pid: %d", l, pid)

	if err := ioutil.WriteFile(filepath.Join(dir, "daemon.pid"), []byte(fmt.Sprint(pid)), 0666); err != nil {
		return nil, err
	}
	if err := filecoin.WaitOnAPI(l); err != nil {
		return nil, err
	}
	return iptbutil.NewOutput(dargs, []byte{}, []byte{}, 0, err), nil
}

// Stop stops the node process.
func (l *Localfilecoin) Stop(ctx context.Context) error {
	pid, err := l.getPID()
	if err != nil {
		return fmt.Errorf("error killing daemon %s: %s", l.dir, err)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("error killing daemon %s: %s", l.dir, err)
	}

	waitch := make(chan struct{}, 1)
	go func() {
		// TODO pass return state
		p.Wait() // nolint: errcheck
		waitch <- struct{}{}
	}()

	defer func() {
		err := os.Remove(filepath.Join(l.dir, "daemon.pid"))
		if err != nil && !os.IsNotExist(err) {
			panic(fmt.Errorf("error removing pid file for daemon at %s: %s", l.dir, err))
		}
		err = os.Remove(filepath.Join(l.dir, "api"))
		if err != nil && !os.IsNotExist(err) {
			panic(fmt.Errorf("error removing pid file for daemon at %s: %s", l.dir, err))
		}
	}()

	if err := l.signalAndWait(p, waitch, syscall.SIGTERM, 1*time.Second); err != errTimeout {
		return err
	}

	if err := l.signalAndWait(p, waitch, syscall.SIGTERM, 2*time.Second); err != errTimeout {
		return err
	}

	if err := l.signalAndWait(p, waitch, syscall.SIGQUIT, 5*time.Second); err != errTimeout {
		return err
	}

	if err := l.signalAndWait(p, waitch, syscall.SIGKILL, 5*time.Second); err != errTimeout {
		return err
	}

	for {
		err := p.Signal(syscall.Signal(0))
		if err != nil {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}

	return nil
}

// RunCmd runs a command in the context of the node.
func (l *Localfilecoin) RunCmd(ctx context.Context, stdin io.Reader, args ...string) (testbedi.Output, error) {
	log.Infof("[%s] | RUN: %s", l.peerid, args)
	env, err := l.env()
	if err != nil {
		return nil, fmt.Errorf("error getting env: %s", err)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdin = stdin

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	stderrbytes, err := ioutil.ReadAll(stderr)
	if err != nil {
		return nil, err
	}

	stdoutbytes, err := ioutil.ReadAll(stdout)
	if err != nil {
		return nil, err
	}

	exiterr := cmd.Wait()

	var exitcode = 0
	switch oerr := exiterr.(type) {
	case *exec.ExitError:
		if ctx.Err() == context.DeadlineExceeded {
			err = errors.Wrapf(oerr, "context deadline exceeded for command: %q", strings.Join(cmd.Args, " "))
		}

		exitcode = 1
	case nil:
		err = oerr
	}

	return iptbutil.NewOutput(args, stdoutbytes, stderrbytes, exitcode, err), nil
}

// Connect connects the node to another testbed node.
func (l *Localfilecoin) Connect(ctx context.Context, n testbedi.Core) error {
	swarmaddrs, err := n.SwarmAddrs()
	if err != nil {
		return err
	}

	output, err := l.RunCmd(ctx, nil, "go-filecoin", "swarm", "connect", swarmaddrs[0])

	if err != nil {
		return err
	}

	if output.ExitCode() != 0 {
		out, err := ioutil.ReadAll(output.Stderr())
		if err != nil {
			return err
		}

		return fmt.Errorf("%s", string(out))
	}

	return err
}

// Shell starts a shell in the context of a node.
func (l *Localfilecoin) Shell(ctx context.Context, ns []testbedi.Core) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return fmt.Errorf("no shell found")
	}

	if len(os.Getenv("FIL_PATH")) != 0 {
		// If the users shell sets IPFS_PATH, it will just be overridden by the shell again
		return fmt.Errorf("shell has FIL_PATH set, please unset before trying to use iptb shell")
	}

	nenvs, err := l.env()
	if err != nil {
		return err
	}

	// TODO(tperson): It would be great if we could guarantee that the shell
	// is using the same binary. However, the users shell may prepend anything
	// we change in the PATH

	for i, n := range ns {
		peerid, err := n.PeerID()

		if err != nil {
			return err
		}

		nenvs = append(nenvs, fmt.Sprintf("NODE%d=%s", i, peerid))
	}

	return syscall.Exec(shell, []string{shell}, nenvs)
}

// Infof writes an info log.
func (l *Localfilecoin) Infof(format string, args ...interface{}) {
	log.Infof("Node: %s %s", l, fmt.Sprintf(format, args...))
}

// Errorf writes an error log.
func (l *Localfilecoin) Errorf(format string, args ...interface{}) {
	log.Errorf("Node: %s %s", l, fmt.Sprintf(format, args...))
}

// StderrReader returns a reader to the nodes stderr.
func (l *Localfilecoin) StderrReader() (io.ReadCloser, error) {
	return l.readerFor("daemon.stdout")
}

// StdoutReader returns a reader to the nodes stdout.
func (l *Localfilecoin) StdoutReader() (io.ReadCloser, error) {
	return l.readerFor("daemon.stdout")
}

// Dir returns the directory the node is using.
func (l *Localfilecoin) Dir() string {
	return l.dir
}

// Type returns the type of the node.
func (l *Localfilecoin) Type() string {
	return PluginName
}

// String implements the stringr interface.
func (l *Localfilecoin) String() string {
	return l.dir
}

/** Libp2p Interface **/

// PeerID returns the peerID of the localfilecoin node
func (l *Localfilecoin) PeerID() (string, error) {
	db, err := l.db()
	if err != nil {
		return "", err
	}
	defer db.Close()

	bpeerID, err := db.Get(key_peerID)
	if err != nil {
		return "", err
	}
	return string(bpeerID), nil
}

// APIAddr returns the api address of the node.
func (l *Localfilecoin) APIAddr() (string, error) {
	var err error
	l.apiaddr, err = filecoin.GetAPIAddrFromRepo(l.dir)
	if err != nil {
		return "", err
	}

	return l.apiaddr.String(), err
}

// SwarmAddrs returns the addresses a node is listening on for swarm connections.
func (l *Localfilecoin) SwarmAddrs() ([]string, error) {
	out, err := l.RunCmd(context.Background(), nil, "go-filecoin", "id", "--format=<addrs>")
	if err != nil {
		return nil, err
	}

	outStr, err := ioutil.ReadAll(out.Stdout())
	if err != nil {
		return nil, err
	}

	addrs := strings.Split(string(outStr), "\n")
	return addrs, nil
}

/** Config Interface **/

// GetConfig returns the nodes config.
func (l *Localfilecoin) GetConfig() (interface{}, error) {
	return config.ReadFile(filepath.Join(l.dir, "config.json"))
}

// WriteConfig writes a nodes config file.
func (l *Localfilecoin) WriteConfig(cfg interface{}) error {
	lcfg := cfg.(*config.Config)
	return lcfg.WriteFile(filepath.Join(l.dir, "config.json"))
}
