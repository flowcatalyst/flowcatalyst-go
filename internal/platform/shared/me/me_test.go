package me

import (
	"testing"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
)

func strptr(s string) *string { return &s }

func TestFilterMyApplications(t *testing.T) {
	base := "https://orders.example"
	all := []application.Application{
		{ID: "app_1", Code: "orders", Name: "Orders", DefaultBaseURL: &base},
		{ID: "app_2", Code: "billing", Name: "Billing", Description: strptr("Billing app")},
		{ID: "app_3", Code: "secret", Name: "Secret"},
	}

	t.Run("anchor sees all + maps DefaultBaseURL to baseUrl", func(t *testing.T) {
		got := filterMyApplications(all, true, nil)
		if len(got) != 3 {
			t.Fatalf("anchor must see all 3 apps; got %d", len(got))
		}
		if got[0].BaseURL == nil || *got[0].BaseURL != base {
			t.Fatalf("DefaultBaseURL must map to baseUrl; got %v", got[0].BaseURL)
		}
		if got[1].Description == nil || *got[1].Description != "Billing app" {
			t.Fatalf("description must pass through; got %v", got[1].Description)
		}
	})

	t.Run("non-anchor sees only accessible", func(t *testing.T) {
		got := filterMyApplications(all, false, map[string]bool{"app_1": true, "app_3": true})
		if len(got) != 2 {
			t.Fatalf("non-anchor must see only granted apps; got %d", len(got))
		}
		for _, a := range got {
			if a.ID == "app_2" {
				t.Fatalf("app_2 was not granted and must be excluded")
			}
		}
	})

	t.Run("non-anchor with no grants sees none", func(t *testing.T) {
		if got := filterMyApplications(all, false, nil); len(got) != 0 {
			t.Fatalf("non-anchor with no grants must see 0 apps; got %d", len(got))
		}
	})
}
