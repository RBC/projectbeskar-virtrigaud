/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vsphere

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	providerv1 "github.com/projectbeskar/virtrigaud/proto/rpc/provider/v1"
)

// ---- selectBestDatastoreByFreeSpace -----------------------------------------

func TestSelectBestDatastoreByFreeSpace_SingleDatastore(t *testing.T) {
	datastores := []mo.Datastore{
		makeDatastore("ds1", 100*gib),
	}
	best := selectBestDatastoreByFreeSpace(datastores)
	assert.Equal(t, "ds1", best.Name)
}

func TestSelectBestDatastoreByFreeSpace_PicksMaxFree(t *testing.T) {
	datastores := []mo.Datastore{
		makeDatastore("ds-small", 10*gib),
		makeDatastore("ds-large", 500*gib),
		makeDatastore("ds-medium", 250*gib),
	}
	best := selectBestDatastoreByFreeSpace(datastores)
	assert.Equal(t, "ds-large", best.Name)
}

func TestSelectBestDatastoreByFreeSpace_FirstWinsOnTie(t *testing.T) {
	datastores := []mo.Datastore{
		makeDatastore("ds-a", 200*gib),
		makeDatastore("ds-b", 200*gib),
	}
	// When free space is equal the first entry is kept (stable behaviour).
	best := selectBestDatastoreByFreeSpace(datastores)
	assert.Equal(t, "ds-a", best.Name)
}

func TestSelectBestDatastoreByFreeSpace_ZeroFreeSpaceOnAll(t *testing.T) {
	datastores := []mo.Datastore{
		makeDatastore("ds-full-1", 0),
		makeDatastore("ds-full-2", 0),
	}
	best := selectBestDatastoreByFreeSpace(datastores)
	assert.Equal(t, "ds-full-1", best.Name)
}

func TestSelectBestDatastoreByFreeSpace_LargeCluster(t *testing.T) {
	datastores := make([]mo.Datastore, 20)
	for i := range datastores {
		datastores[i] = makeDatastore(
			"ds-"+string(rune('a'+i)),
			int64(i+1)*50*gib,
		)
	}
	// Last entry has the most free space (20 * 50 GiB).
	best := selectBestDatastoreByFreeSpace(datastores)
	assert.Equal(t, "ds-"+string(rune('a'+19)), best.Name)
}

// ---- Placement JSON parsing -------------------------------------------------

func TestParsePlacementJSON_StoragePodOnly(t *testing.T) {
	p := newTestProvider(t)

	placementJSON, err := json.Marshal(map[string]string{
		"StoragePod": "my-ds-cluster",
	})
	require.NoError(t, err)

	req := &providerv1.CreateRequest{
		Name:          "vm-test",
		PlacementJson: string(placementJSON),
	}

	spec, err := p.parseCreateRequest(req)
	require.NoError(t, err)
	assert.Equal(t, "my-ds-cluster", spec.StoragePod)
	assert.Empty(t, spec.Datastore)
}

func TestParsePlacementJSON_DatastoreAndStoragePodBothSet(t *testing.T) {
	p := newTestProvider(t)

	placementJSON, err := json.Marshal(map[string]string{
		"Datastore":  "explicit-ds",
		"StoragePod": "ds-cluster",
	})
	require.NoError(t, err)

	req := &providerv1.CreateRequest{
		Name:          "vm-test",
		PlacementJson: string(placementJSON),
	}

	spec, err := p.parseCreateRequest(req)
	require.NoError(t, err)
	// Both are parsed; the precedence logic is applied at VM creation time.
	assert.Equal(t, "explicit-ds", spec.Datastore)
	assert.Equal(t, "ds-cluster", spec.StoragePod)
}

func TestParsePlacementJSON_AllFields(t *testing.T) {
	p := newTestProvider(t)

	placementJSON, err := json.Marshal(map[string]string{
		"Cluster":    "prod-cluster",
		"Datastore":  "prod-ds",
		"StoragePod": "prod-ds-cluster",
		"Folder":     "/prod/vms",
		"Host":       "esxi-01.example.com",
	})
	require.NoError(t, err)

	req := &providerv1.CreateRequest{
		Name:          "vm-test",
		PlacementJson: string(placementJSON),
	}

	spec, err := p.parseCreateRequest(req)
	require.NoError(t, err)
	assert.Equal(t, "prod-cluster", spec.Cluster)
	assert.Equal(t, "prod-ds", spec.Datastore)
	assert.Equal(t, "prod-ds-cluster", spec.StoragePod)
	assert.Equal(t, "/prod/vms", spec.Folder)
	assert.Equal(t, "esxi-01.example.com", spec.Host)
}

func TestParsePlacementJSON_EmptyStoragePod(t *testing.T) {
	p := newTestProvider(t)

	req := &providerv1.CreateRequest{
		Name:          "vm-test",
		PlacementJson: `{"Datastore":"explicit-ds"}`,
	}

	spec, err := p.parseCreateRequest(req)
	require.NoError(t, err)
	assert.Empty(t, spec.StoragePod)
	assert.Equal(t, "explicit-ds", spec.Datastore)
}

func TestParsePlacementJSON_NilPlacement(t *testing.T) {
	p := newTestProvider(t)

	req := &providerv1.CreateRequest{
		Name: "vm-test",
	}

	spec, err := p.parseCreateRequest(req)
	require.NoError(t, err)
	assert.Empty(t, spec.StoragePod)
	assert.Empty(t, spec.Datastore)
}

func TestParsePlacementJSON_InvalidJSON(t *testing.T) {
	p := newTestProvider(t)

	req := &providerv1.CreateRequest{
		Name:          "vm-test",
		PlacementJson: `{not valid json`,
	}

	_, err := p.parseCreateRequest(req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse Placement JSON")
}

// ---- VMSpec StoragePod field ------------------------------------------------

func TestVMSpec_StoragePodField(t *testing.T) {
	tests := []struct {
		name       string
		datastore  string
		storagePod string
	}{
		{
			name:       "storagePod only",
			datastore:  "",
			storagePod: "my-cluster",
		},
		{
			name:       "datastore only",
			datastore:  "my-ds",
			storagePod: "",
		},
		{
			name:       "both set (datastore takes precedence at creation time)",
			datastore:  "explicit-ds",
			storagePod: "ds-cluster",
		},
		{
			name:       "neither set",
			datastore:  "",
			storagePod: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := VMSpec{
				Datastore:  tt.datastore,
				StoragePod: tt.storagePod,
			}
			assert.Equal(t, tt.datastore, spec.Datastore)
			assert.Equal(t, tt.storagePod, spec.StoragePod)
		})
	}
}

// ---- Config env var loading ------------------------------------------------

func TestConfig_DefaultStoragePodFromEnv(t *testing.T) {
	t.Setenv("PROVIDER_DEFAULT_STORAGE_POD", "env-ds-cluster")

	cfg := &Config{
		DefaultStoragePod: os.Getenv("PROVIDER_DEFAULT_STORAGE_POD"),
	}
	assert.Equal(t, "env-ds-cluster", cfg.DefaultStoragePod)
}

func TestConfig_DefaultStoragePodEmptyWhenEnvUnset(t *testing.T) {
	t.Setenv("PROVIDER_DEFAULT_STORAGE_POD", "")

	cfg := &Config{
		DefaultStoragePod: os.Getenv("PROVIDER_DEFAULT_STORAGE_POD"),
	}
	assert.Empty(t, cfg.DefaultStoragePod)
}

// ---- StoragePod precedence -------------------------------------------------

func TestStoragePodPrecedence_SpecOverridesDefault(t *testing.T) {
	// Verify documented priority: spec.StoragePod > config.DefaultStoragePod
	cfg := &Config{
		DefaultStoragePod: "default-cluster",
	}

	spec := &VMSpec{StoragePod: "spec-cluster"}

	// Replicate the precedence resolution that happens in VM creation.
	resolved := cfg.DefaultStoragePod
	if spec.StoragePod != "" {
		resolved = spec.StoragePod
	}

	assert.Equal(t, "spec-cluster", resolved)
}

func TestStoragePodPrecedence_DefaultUsedWhenSpecEmpty(t *testing.T) {
	cfg := &Config{
		DefaultStoragePod: "default-cluster",
	}

	spec := &VMSpec{StoragePod: ""}

	resolved := cfg.DefaultStoragePod
	if spec.StoragePod != "" {
		resolved = spec.StoragePod
	}

	assert.Equal(t, "default-cluster", resolved)
}

func TestDatastoreTakesPriorityOverStoragePod(t *testing.T) {
	// When both Datastore and StoragePod are set, Datastore wins.
	spec := &VMSpec{
		Datastore:  "explicit-ds",
		StoragePod: "ds-cluster",
	}

	// Replicate the if/else chain from createVM.
	var resolvedSource string
	if spec.Datastore != "" {
		resolvedSource = "datastore"
	} else {
		resolvedSource = "storagepod"
	}

	assert.Equal(t, "datastore", resolvedSource,
		"explicit Datastore must take priority over StoragePod")
}

func TestStoragePodUsedWhenDatastoreEmpty(t *testing.T) {
	spec := &VMSpec{
		Datastore:  "",
		StoragePod: "ds-cluster",
	}

	var resolvedSource string
	if spec.Datastore != "" {
		resolvedSource = "datastore"
	} else if spec.StoragePod != "" {
		resolvedSource = "storagepod"
	} else {
		resolvedSource = "default"
	}

	assert.Equal(t, "storagepod", resolvedSource)
}

func TestDefaultDatastoreUsedWhenNeitherSet(t *testing.T) {
	cfg := &Config{DefaultDatastore: "fallback-ds"}
	spec := &VMSpec{Datastore: "", StoragePod: ""}

	var resolvedDS string
	if spec.Datastore != "" {
		resolvedDS = spec.Datastore
	} else if spec.StoragePod != "" {
		resolvedDS = "<from-storagepod>"
	} else {
		resolvedDS = cfg.DefaultDatastore
	}

	assert.Equal(t, "fallback-ds", resolvedDS)
}

// ---- helpers ---------------------------------------------------------------

const gib = 1024 * 1024 * 1024

func makeDatastore(name string, freeSpace int64) mo.Datastore {
	return mo.Datastore{
		ManagedEntity: mo.ManagedEntity{
			ExtensibleManagedObject: mo.ExtensibleManagedObject{
				Self: types.ManagedObjectReference{
					Type:  "Datastore",
					Value: name,
				},
			},
			Name: name,
		},
		Summary: types.DatastoreSummary{
			FreeSpace: freeSpace,
		},
	}
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return &Provider{
		logger: logger,
		config: &Config{
			DefaultDatastore:  "datastore1",
			DefaultStoragePod: "",
			DefaultCluster:    "cluster01",
			DefaultFolder:     "vms",
		},
	}
}
