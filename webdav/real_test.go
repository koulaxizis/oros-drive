package webdav

import (
	"os"
	"testing"

	"github.com/koulaxizis/oros-drive/vault/storetest"
)

// TestRealServer runs the connection check and the store promises
// against a real server, in a new folder that it leaves behind:
//
//	OROS_DAV_TEST_URL=https://cloud.example.com/remote.php/dav/files/me \
//	OROS_DAV_TEST_USER=me OROS_DAV_TEST_PASS=<app password> go test -run RealServer ./webdav/
//
// OROS_DAV_TEST_WANT=dav-conditions (for example) expects the check to
// refuse the server with that code instead.
func TestRealServer(t *testing.T) {
	u := os.Getenv("OROS_DAV_TEST_URL")
	if u == "" {
		t.Skip("OROS_DAV_TEST_URL not set")
	}
	conf, err := NormConf(u, os.Getenv("OROS_DAV_TEST_USER"), "/orOS-Drive-test-"+randID())
	if err != nil {
		t.Fatal(err)
	}
	c := New(conf, os.Getenv("OROS_DAV_TEST_PASS"))
	err = c.Probe()
	if want := os.Getenv("OROS_DAV_TEST_WANT"); want != "" {
		if Code(err) != want {
			t.Fatalf("check: %v, want %s", err, want)
		}
		return
	}
	if err != nil {
		t.Fatal("check:", err)
	}
	storetest.Run(t, c)
	t.Logf("left behind: %s%s", conf.URL, conf.Dir)
}
