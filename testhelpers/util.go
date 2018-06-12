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
	defer l.Close()
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

func SwarmAddr(addr string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.SwarmAddr = addr
	}
}

func RepoDir(dir string) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.RepoDir = dir
	}
}

func ShouldInit(i bool) func(*TestDaemon) {
	return func(td *TestDaemon) {
		td.init = i
	}
}
