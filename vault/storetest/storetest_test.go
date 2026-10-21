package storetest

import (
	"testing"

	"github.com/koulaxizis/oros-drive/vault"
)

func TestDirStore(t *testing.T) { Run(t, vault.NewDirStore(t.TempDir())) }
