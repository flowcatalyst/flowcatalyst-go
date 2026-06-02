package seed

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlatformEventTypesCatalogShape(t *testing.T) {
	defs := PlatformEventTypes()
	if len(defs) < 41 {
		t.Fatalf("expected 41+ event-type definitions, got %d", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Code] {
			t.Fatalf("duplicate definition: %s", d.Code)
		}
		seen[d.Code] = true
		if !strings.HasPrefix(d.Code, "platform:iam:") && !strings.HasPrefix(d.Code, "platform:admin:") {
			t.Fatalf("unexpected prefix on %s — must be platform:iam or platform:admin", d.Code)
		}
		if d.Name == "" {
			t.Fatalf("%s: empty name", d.Code)
		}
	}
}

func TestSchemasAlignedWithDefinitions(t *testing.T) {
	defs := PlatformEventTypes()
	schemas := platformEventSchemas()
	for _, d := range defs {
		if _, ok := schemas[d.Code]; !ok {
			t.Errorf("definition %q has no schema", d.Code)
		}
	}
	for code := range schemas {
		var present bool
		for _, d := range defs {
			if d.Code == code {
				present = true
				break
			}
		}
		if !present {
			t.Errorf("schema %q has no matching definition", code)
		}
	}
}

func TestSchemasAreValidJSONObjects(t *testing.T) {
	schemas := platformEventSchemas()
	if len(schemas) < 41 {
		t.Fatalf("expected 41+ schemas, got %d", len(schemas))
	}
	for code, s := range schemas {
		var v map[string]any
		if err := json.Unmarshal(s, &v); err != nil {
			t.Fatalf("schema %q: invalid JSON: %v", code, err)
		}
		if v["type"] != "object" {
			t.Errorf("schema %q: type must be object", code)
		}
		if _, ok := v["properties"]; !ok {
			t.Errorf("schema %q: missing properties", code)
		}
		if _, ok := v["required"]; !ok {
			t.Errorf("schema %q: missing required", code)
		}
	}
}

func TestNoWebhookOrDeliveryEvents(t *testing.T) {
	defs := PlatformEventTypes()
	for _, d := range defs {
		if strings.Contains(d.Code, "webhook") || strings.Contains(d.Code, "delivery") {
			t.Errorf("forbidden event type: %s", d.Code)
		}
	}
}

func TestTitleCase(t *testing.T) {
	cases := map[string]string{
		"created":        "Created",
		"roles-assigned": "Roles Assigned",
		"client-access":  "Client Access",
		"":               "",
		"already Done":   "Already Done",
	}
	for in, want := range cases {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
}
