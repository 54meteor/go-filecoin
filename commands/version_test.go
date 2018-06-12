package commands

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersion(t *testing.T) {
	assert := assert.New(t)

	var gitOut []byte
	var err error
	gitArgs := []string{"rev-parse", "--verify", "HEAD"}

	if gitOut, err = exec.Command("git", gitArgs...).Output(); err != nil {
		assert.NoError(err)
	}
	commit := string(gitOut)

	d := NewTestDaemon(t).Start()
	defer d.ShutdownSuccess()

	out := d.RunSuccess("version")
	assert.Exactly(out.ReadStdout(), fmt.Sprintf("commit: %s", commit))

}
