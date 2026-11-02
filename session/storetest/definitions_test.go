package storetest_test

import (
	"testing"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

func TestMemoryDefinitions(t *testing.T) {
	storetest.RunDefinitions(t, func() (storetest.OpenDefinitions, func(), error) {
		shared := session.NewMemoryDefinitions()
		return func() session.Definitions { return shared }, nil, nil
	})
}

func TestFileDefinitions(t *testing.T) {
	storetest.RunDefinitions(t, func() (storetest.OpenDefinitions, func(), error) {
		dir := t.TempDir()
		return func() session.Definitions {
			d, err := session.NewFileDefinitions(dir)
			if err != nil {
				t.Fatal(err)
			}
			return d
		}, nil, nil
	})
}
