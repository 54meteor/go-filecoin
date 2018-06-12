package commands

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDaemonStartupMessage(t *testing.T) {
	assert := assert.New(t)
	daemon := NewTestDaemon(t).Start()
	daemon.ShutdownSuccess()

	out := daemon.ReadStdout()
	assert.Regexp("^My peer ID is [a-zA-Z0-9]*", out)
	assert.Regexp("\\nSwarm listening on.*", out)
}

func TestDaemonApiFile(t *testing.T) {
	assert := assert.New(t)
	daemon := NewTestDaemon(t).Start()

	apiPath := filepath.Join(daemon.RepoDir, "api")
	assert.FileExists(apiPath)

	daemon.ShutdownEasy()

	_, err := os.Lstat(apiPath)
	assert.Error(err, "Expect api file to be deleted on shutdown")
	assert.True(os.IsNotExist(err))
}

func TestDaemonCORS(t *testing.T) {
	t.Run("default allowed origins work", func(t *testing.T) {
		assert := assert.New(t)
		td := NewTestDaemon(t).Start()

		url := fmt.Sprintf("http://127.0.0.1%s/api/id", td.CmdAddr)
		req, err := http.NewRequest("GET", url, nil)
		assert.NoError(err)
		req.Header.Add("Origin", "http://localhost")
		res, err := http.DefaultClient.Do(req)
		assert.NoError(err)
		assert.Equal(http.StatusOK, res.StatusCode)

		req, err = http.NewRequest("GET", url, nil)
		assert.NoError(err)
		req.Header.Add("Origin", "https://localhost")
		res, err = http.DefaultClient.Do(req)
		assert.NoError(err)
		assert.Equal(http.StatusOK, res.StatusCode)

		req, err = http.NewRequest("GET", url, nil)
		assert.NoError(err)
		req.Header.Add("Origin", "http://127.0.0.1")
		res, err = http.DefaultClient.Do(req)
		assert.NoError(err)
		assert.Equal(http.StatusOK, res.StatusCode)

		req, err = http.NewRequest("GET", url, nil)
		assert.NoError(err)
		req.Header.Add("Origin", "https://127.0.0.1")
		res, err = http.DefaultClient.Do(req)
		assert.NoError(err)
		assert.Equal(http.StatusOK, res.StatusCode)
	})

	t.Run("non-configured origin fails", func(t *testing.T) {
		assert := assert.New(t)
		td := NewTestDaemon(t).Start()

		url := fmt.Sprintf("http://127.0.0.1%s/api/id", td.CmdAddr)
		req, err := http.NewRequest("GET", url, nil)
		assert.NoError(err)
		req.Header.Add("Origin", "http://disallowed.origin")
		res, err := http.DefaultClient.Do(req)
		assert.NoError(err)
		assert.Equal(http.StatusForbidden, res.StatusCode)
	})
}
