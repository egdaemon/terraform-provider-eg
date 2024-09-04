package tarx_test

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/egdaemon/egt/internal/tarx"
	"github.com/stretchr/testify/assert"
)

func TestTarxCreateArchiveWith(t *testing.T) {
	dst, err := os.CreateTemp("", "archive.*.tar")
	assert.NoError(t, err)
	contents := "hello world"
	r := strings.NewReader(contents)
	_, err = CreateArchiveWith(dst, HeaderFromReader("example.txt", r), r)
	assert.NoError(t, err)
}

func TestTarxWriteFileToArchive(t *testing.T) {
	dst, err := os.CreateTemp("", "archive.*.tar")
	assert.NoError(t, err)
	contents := "hello world"
	contents2 := []byte("what's up")
	r := strings.NewReader(contents)
	tw, err := CreateArchiveWith(dst, HeaderFromReader("example.txt", r), r)
	assert.NoError(t, err)
	err = WriteFileToArchive(tw, NewHeader("foo.bar", time.Now(), int64(len(contents2)), 0600), bytes.NewReader(contents2))
	assert.NoError(t, err)
	assert.NoError(t, tw.Flush())
}
