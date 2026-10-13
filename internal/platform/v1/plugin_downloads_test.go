package v1

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPluginDownloadsReaderGetsTotals(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginDownloads = store.PluginDownloads{
		Today:       initTestTime.UTC(),
		AllVersions: store.PluginDownloadWindows{Last7Days: 7, Last30Days: 30, Last365Days: 365, AllTime: 1204},
		Versions: []store.PluginVersionDownloads{{
			Version: "1.10.0", State: "available",
			PluginDownloadWindows: store.PluginDownloadWindows{Last7Days: 4, Last30Days: 20, Last365Days: 300, AllTime: 900},
		}},
	}
	handler, trail := auditedPlatform(t, pluginRegistryHandler(identity.RoleReader, repository))
	response := call(t, handler, http.MethodGet, pluginRegistryPath("plugins/probe/downloads"), nil, testToken)
	var downloads PluginDownloads
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &downloads) != nil {
		t.Fatalf("downloads = %d %s", response.Code, response.Body.String())
	}
	if downloads.AllVersions.AllTime != 1204 || downloads.AllVersions.Last7Days != 7 || len(downloads.Versions) != 1 ||
		downloads.Versions[0].Version != "1.10.0" || downloads.Versions[0].State != PluginVersionDownloadAvailable || downloads.Versions[0].AllTime != 900 {
		t.Fatalf("downloads = %+v", downloads)
	}
	assertPlatformAudit(t, trail.response(t), map[string]any{
		"operation": "plugin.downloads.read", "target_type": "plugin", "target_id": "probe", "outcome": "success",
	})
}

func TestPluginDownloadsCallerOutsideOrganizationIsRefused(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginDownloads = store.PluginDownloads{Today: initTestTime.UTC()}
	handler := newHandler(repository, &fakeInstanceRepository{}, testAuth{}, testRoles{
		role: identity.RoleReader, scope: identity.Scope{OrganizationID: uuid.New()},
	}, testLogger(), func() time.Time { return initTestTime })
	response := call(t, handler, http.MethodGet, pluginRegistryPath("plugins/probe/downloads"), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("caller outside organization status = %d, want 404", response.Code)
	}
}

func TestPluginDownloadsUnknownPluginIsNotFound(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{Enabled: true})
	repository.pluginDownloadsErr = registry.ErrNotFound
	response := call(t, pluginRegistryHandler(identity.RoleReader, repository), http.MethodGet,
		pluginRegistryPath("plugins/absent/downloads"), nil, testToken)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin status = %d, want 404", response.Code)
	}
}

func TestPluginDownloadsRegistryNotEnabledIsConflict(t *testing.T) {
	repository := pluginRegistryRepository(store.PluginRegistry{})
	repository.pluginDownloadsErr = store.ErrPluginRegistryNotEnabled
	response := call(t, pluginRegistryHandler(identity.RoleReader, repository), http.MethodGet,
		pluginRegistryPath("plugins/probe/downloads"), nil, testToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("disabled registry status = %d, want 409", response.Code)
	}
}
