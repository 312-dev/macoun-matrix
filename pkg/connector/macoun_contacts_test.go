// macoun-matrix: tests for disable_icloud_contacts (from corten-matrix 64f9d73).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

func TestDisableICloudContactsConfig(t *testing.T) {
	var cfg IMConfig
	if err := yaml.Unmarshal([]byte("disable_icloud_contacts: true\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableICloudContacts {
		t.Fatal("disable_icloud_contacts: true not parsed")
	}
	example, err := os.ReadFile("../imconfig/example-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(example), "\ndisable_icloud_contacts: false\n") {
		t.Fatal("example config lacks disable_icloud_contacts (config upgrade would drop it)")
	}
}

// With the switch on, the shared-profile refresh returns before touching its
// store or the network. The store here has no database, so reaching it panics.
func TestDisableICloudContactsSkipsSharedProfileRefresh(t *testing.T) {
	c := &IMClient{
		Main:               &IMConnector{Config: IMConfig{DisableICloudContacts: true}},
		sharedProfileStore: &sharedProfileStore{},
		client:             &rustpushgo.Client{},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("refresh ran with iCloud contacts disabled: %v", r)
		}
	}()
	c.refreshAllSharedProfiles(zerolog.Nop())
}

// Control for the test above: with the switch off, the same setup does reach
// the store (and panics on its missing database), so the test above can fail.
func TestSharedProfileRefreshReachesStoreWhenEnabled(t *testing.T) {
	c := &IMClient{
		Main:               &IMConnector{Config: IMConfig{}},
		sharedProfileStore: &sharedProfileStore{},
		client:             &rustpushgo.Client{},
	}
	defer func() {
		if recover() == nil {
			t.Fatal("refresh did not reach the store with iCloud contacts enabled")
		}
	}()
	c.refreshAllSharedProfiles(zerolog.Nop())
}
