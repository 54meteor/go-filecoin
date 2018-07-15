package testhelpers

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// GetFreePort gets a free port from the kernel
// Credit: https://github.com/phayes/freeport
func GetFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close() // nolint: errcheck
	return l.Addr().(*net.TCPAddr).Port, nil
}

// GetFilecoinBinary returns the path where the filecoin binary will be if it has been built.
func GetFilecoinBinary() (string, error) {
	bin := filepath.FromSlash(fmt.Sprintf("%s/src/github.com/filecoin-project/go-filecoin/go-filecoin", os.Getenv("GOPATH")))
	_, err := os.Stat(bin)
	if err == nil {
		return bin, nil
	}

	if os.IsNotExist(err) {
		return "", fmt.Errorf("You are missing the filecoin binary...try building, searched in '%s'", bin)
	}

	return "", err
}

// SwarmAddr sets the swarmaddr
func SwarmAddr(addr string) func(*Daemon) {
	return func(td *Daemon) {
		td.SwarmAddr = addr
	}
}

// RepoDir sets the repodir
func RepoDir(dir string) func(*Daemon) {
	return func(td *Daemon) {
		td.RepoDir = dir
	}
}

// InsecureApi enables all origins
func InsecureApi() func(*Daemon) {
	return func(td *Daemon) {
		td.insecureApi = true
	}
}

// ShouldInit sets if the daemon should run `init` before becoming a daemon
func ShouldInit(i bool) func(*Daemon) {
	return func(td *Daemon) {
		td.Init = i
	}
}

// ShouldStartMining sets whether the daemon should start mining automatically
func ShouldStartMining(m bool) func(*Daemon) {
	return func(td *Daemon) {
		td.startMining = m
	}
}
