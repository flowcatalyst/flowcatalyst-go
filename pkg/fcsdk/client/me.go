package client

import "context"

// MyClient is a tenant accessible to the current user.
type MyClient struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Status     string `json:"status,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// MyClientsResponse — GET /api/me/clients.
type MyClientsResponse struct {
	Clients []MyClient `json:"clients"`
	Total   uint64     `json:"total,omitempty"`
}

// MyApplication is one application accessible within a tenant.
type MyApplication struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	IconURL      string `json:"iconUrl,omitempty"`
	BaseURL      string `json:"baseUrl,omitempty"`
	Website      string `json:"website,omitempty"`
	LogoMimeType string `json:"logoMimeType,omitempty"`
}

// MyApplicationsResponse — GET /api/me/clients/{id}/applications.
type MyApplicationsResponse struct {
	Applications []MyApplication `json:"applications"`
	Total        uint64          `json:"total,omitempty"`
	ClientID     string          `json:"clientId,omitempty"`
}

// MeResource — /api/me/*.
type MeResource struct {
	c *FlowCatalystClient
}

// Clients — GET /api/me/clients. Access is determined by user scope:
// ANCHOR → all active clients; PARTNER → IDP-granted + explicit grants;
// CLIENT → home client + IDP + explicit grants.
func (r *MeResource) Clients(ctx context.Context) (*MyClientsResponse, error) {
	var out MyClientsResponse
	if err := r.c.Get(ctx, "/api/me/clients", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Client — GET /api/me/clients/{id}.
func (r *MeResource) Client(ctx context.Context, clientID string) (*MyClient, error) {
	var out MyClient
	if err := r.c.Get(ctx, "/api/me/clients/"+clientID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClientApplications — GET /api/me/clients/{id}/applications.
func (r *MeResource) ClientApplications(ctx context.Context, clientID string) (*MyApplicationsResponse, error) {
	var out MyApplicationsResponse
	if err := r.c.Get(ctx, "/api/me/clients/"+clientID+"/applications", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
