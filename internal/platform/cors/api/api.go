// Package api wires HTTP routes for the cors subdomain via huma.
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/cors/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apicommon"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/apiroute"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/auth"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

type State struct {
	Repo *cors.Repository
	UoW  *usecasepgx.UnitOfWork
}

const tag = "cors-origins"

func Register(api huma.API, s *State) {
	g := apiroute.New(api, tag)
	apiroute.Get(g, "publicAllowedOrigins", "/api/platform/cors/allowed", "List allowed CORS origins (public)", s.publicAllowed)
	apiroute.Get(g, "listCorsOrigins", "/api/platform/cors", "List CORS origins (anchor)", s.list)
	apiroute.Post(g, "addCorsOrigin", "/api/platform/cors", "Add a CORS origin", http.StatusCreated, s.add)
	apiroute.Get(g, "getCorsOrigin", "/api/platform/cors/{id}", "Get a CORS origin by id (anchor)", s.getByID)
	apiroute.Delete(g, "deleteCorsOrigin", "/api/platform/cors/{id}", "Remove a CORS origin", http.StatusNoContent, s.delete)
}

func (s *State) publicAllowed(ctx context.Context, _ *apicommon.Empty) (*apicommon.Out[PublicAllowedResponse], error) {
	rows, err := s.Repo.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	origins := apicommon.MapSlice(rows, func(o *cors.AllowedOrigin) string { return o.Origin })
	return &apicommon.Out[PublicAllowedResponse]{Body: PublicAllowedResponse{Origins: origins}}, nil
}

func (s *State) getByID(ctx context.Context, in *apicommon.IDInput) (*apicommon.Out[AllowedOriginResponse], error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	o, err := s.Repo.FindByID(ctx, in.ID)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if o == nil {
		return nil, httperror.NotFound("CorsOrigin", in.ID)
	}
	return &apicommon.Out[AllowedOriginResponse]{Body: fromEntity(o)}, nil
}

func (s *State) list(ctx context.Context, _ *apicommon.Empty) (*apicommon.Out[CorsOriginListResponse], error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	rows, err := s.Repo.FindAll(ctx)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_all failed", err)
	}
	out := apicommon.MapSlice(rows, fromEntity)
	return &apicommon.Out[CorsOriginListResponse]{Body: CorsOriginListResponse{CorsOrigins: out, Total: len(out)}}, nil
}

func (s *State) add(ctx context.Context, in *apicommon.In[AddOriginRequest]) (*apicommon.Out[apicommon.CreatedResponse], error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	committed, err := operations.AddOrigin(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
	if err != nil {
		return nil, err
	}
	return &apicommon.Out[apicommon.CreatedResponse]{Body: apicommon.CreatedResponse{ID: committed.Event().OriginID}}, nil
}

func (s *State) delete(ctx context.Context, in *apicommon.IDInput) (*apicommon.Empty, error) {
	ac := auth.FromContext(ctx)
	if err := auth.RequireAnchor(ac); err != nil {
		return nil, err
	}
	ec := usecase.NewExecutionContext(ac.PrincipalID)
	if _, err := operations.DeleteOrigin(ctx, s.Repo, s.UoW, operations.DeleteCommand{OriginID: in.ID}, ec); err != nil {
		return nil, err
	}
	return &apicommon.Empty{}, nil
}
