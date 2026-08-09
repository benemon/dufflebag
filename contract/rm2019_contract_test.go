package contract_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benemon/dufflebag/internal/compat/rm2019"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/domain/registry"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	rmclient "github.com/hashicorp/hcp-sdk-go/clients/cloud-resource-manager/stable/2019-12-10/client"
	projectservice "github.com/hashicorp/hcp-sdk-go/clients/cloud-resource-manager/stable/2019-12-10/client/project_service"
)

type resourceManagerContractRepository struct {
	project store.Project
}

func (r resourceManagerContractRepository) ListOrganizationsForPrincipal(
	_ context.Context, _ *identity.Principal,
) ([]store.Organization, error) {
	return []store.Organization{}, nil
}

func (r resourceManagerContractRepository) ListProjects(
	_ context.Context, organizationID string,
) ([]store.Project, error) {
	if organizationID != r.project.OrganizationID {
		return []store.Project{}, nil
	}
	return []store.Project{r.project}, nil
}

func (r resourceManagerContractRepository) GetProject(
	_ context.Context, organizationID, projectID string,
) (*store.Project, error) {
	if organizationID != r.project.OrganizationID || projectID != r.project.ID {
		return nil, registry.ErrNotFound
	}
	project := r.project
	return &project, nil
}

func TestGeneratedResourceManagerClientGetsBoundProject(t *testing.T) {
	repository := resourceManagerContractRepository{project: store.Project{
		ID:             contractProject,
		OrganizationID: contractOrg,
		Name:           "contract-project",
		CreatedAt:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}
	server := httptest.NewServer(rm2019.NewHandler(
		repository, contractPrincipals{}, contractAuthenticator{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	))
	defer server.Close()
	client := rmclient.NewHTTPClientWithConfig(
		nil,
		rmclient.DefaultTransportConfig().
			WithHost(strings.TrimPrefix(server.URL, "http://")).
			WithSchemes([]string{"http"}),
	).ProjectService

	response, err := client.ProjectServiceGet(
		projectservice.NewProjectServiceGetParams().WithID(contractProject),
		contractAuth,
	)
	if err != nil {
		t.Fatalf("generated ProjectService_Get: %v", err)
	}
	project := response.Payload.Project
	if project == nil || project.ID != contractProject || project.Name != "contract-project" ||
		project.Parent == nil || project.Parent.ID != contractOrg || string(*project.Parent.Type) != "ORGANIZATION" {
		t.Fatalf("generated project payload = %#v", project)
	}

	_, err = client.ProjectServiceGet(
		projectservice.NewProjectServiceGetParams().WithID("1a1b1c1d-2e2f-4a3b-8c4d-5e6f7a8b9c0d"),
		contractAuth,
	)
	if err == nil || !strings.Contains(err.Error(), `"code":5`) {
		t.Fatalf("generated foreign project error = %v, want parsed code 5", err)
	}
}
