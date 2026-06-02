package seed

import (
	"encoding/json"
)

// platformEventSchemas returns the JSON-Schema map. Direct 1:1 port of
// fc-platform/src/seed/platform_event_schemas.rs::schemas(). Keys match
// event-type codes exactly. The DSL helpers (obj/reqStr/optStr/etc.)
// mirror the Rust helpers — when modifying schemas, change BOTH files in
// lockstep so cross-language consumers stay in sync.
func platformEventSchemas() map[string]json.RawMessage {
	m := map[string]json.RawMessage{}

	// ── platform:iam:user ───────────────────────────────────────────────
	m["platform:iam:user:created"] = obj(
		reqStr("principalId"),
		reqStr("email"),
		reqStr("emailDomain"),
		reqStr("name"),
		reqStr("scope"),
		optStr("clientId"),
		reqBool("isAnchorUser"),
	)
	m["platform:iam:user:updated"] = obj(
		reqStr("principalId"),
		optStr("name"),
		optStr("email"),
	)
	m["platform:iam:user:activated"] = obj(reqStr("principalId"))
	m["platform:iam:user:deactivated"] = obj(reqStr("principalId"), optStr("reason"))
	m["platform:iam:user:deleted"] = obj(reqStr("principalId"))
	m["platform:iam:user:roles-assigned"] = obj(
		reqStr("principalId"),
		reqStrArray("roles"),
		reqStrArray("added"),
		reqStrArray("removed"),
	)
	m["platform:iam:user:application-access-assigned"] = obj(
		reqStr("userId"),
		reqStrArray("applicationIds"),
		reqStrArray("added"),
		reqStrArray("removed"),
	)
	m["platform:iam:user:client-access-granted"] = obj(
		reqStr("principalId"), reqStr("clientId"),
	)
	m["platform:iam:user:client-access-revoked"] = obj(
		reqStr("principalId"), reqStr("clientId"),
	)
	// logged-in carries a richer payload — federatedClaims oneOf, etc.
	m["platform:iam:user:logged-in"] = mustRaw(map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]any{
			"userId":               map[string]any{"type": "string"},
			"email":                map[string]any{"type": "string"},
			"loginMethod":          map[string]any{"type": "string", "enum": []string{"INTERNAL", "OIDC"}},
			"identityProviderCode": map[string]any{"type": []string{"string", "null"}},
			"flowcatalystClaims": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"email":        map[string]any{"type": "string"},
					"type":         map[string]any{"type": "string"},
					"roles":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"clients":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"applications": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"email", "type", "roles", "clients", "applications"},
			},
			"federatedClaims": map[string]any{
				"oneOf": []any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"accessToken": map[string]any{"type": "object"},
							"idToken":     map[string]any{"type": "object"},
						},
						"required": []string{"accessToken", "idToken"},
					},
					map[string]any{"type": "null"},
				},
			},
		},
		"required":             []string{"userId", "email", "loginMethod", "flowcatalystClaims"},
		"additionalProperties": true,
	})
	m["platform:iam:user:password-reset-requested"] = obj(reqStr("principalId"), reqStr("email"))
	m["platform:iam:user:password-reset-completed"] = obj(reqStr("principalId"), reqStr("email"))

	// ── platform:iam:principals (sync — plural aggregate) ───────────────
	m["platform:iam:principals:synced"] = obj(
		reqStr("applicationCode"),
		reqU32("created"),
		reqU32("updated"),
		reqU32("deactivated"),
		reqStrArray("syncedEmails"),
	)

	// ── platform:iam:serviceaccount ─────────────────────────────────────
	m["platform:iam:serviceaccount:created"] = obj(
		reqStr("serviceAccountId"), reqStr("code"), reqStr("name"),
		optStr("applicationId"), reqStrArray("clientIds"),
	)
	m["platform:iam:serviceaccount:updated"] = obj(
		reqStr("serviceAccountId"), optStr("name"), optStr("description"),
		reqStrArray("clientIdsAdded"), reqStrArray("clientIdsRemoved"),
	)
	m["platform:iam:serviceaccount:deleted"] = obj(reqStr("serviceAccountId"), reqStr("code"))
	m["platform:iam:serviceaccount:roles-assigned"] = obj(
		reqStr("serviceAccountId"),
		reqStrArray("rolesAdded"),
		reqStrArray("rolesRemoved"),
	)
	m["platform:iam:serviceaccount:token-regenerated"] = obj(
		reqStr("serviceAccountId"), reqStr("code"),
	)
	m["platform:iam:serviceaccount:secret-regenerated"] = obj(
		reqStr("serviceAccountId"), reqStr("code"),
	)

	// ── platform:iam:client ─────────────────────────────────────────────
	m["platform:iam:client:created"] = obj(
		reqStr("clientId"), reqStr("name"), reqStr("identifier"), optStr("description"),
	)
	m["platform:iam:client:updated"] = obj(
		reqStr("clientId"), optStr("name"), optStr("description"),
	)
	m["platform:iam:client:activated"] = obj(reqStr("clientId"), reqStr("previousStatus"))
	m["platform:iam:client:suspended"] = obj(reqStr("clientId"), reqStr("reason"))
	m["platform:iam:client:deleted"] = obj(reqStr("clientId"), reqStr("name"), reqStr("identifier"))
	m["platform:iam:client:note-added"] = obj(
		reqStr("clientId"), reqStr("category"), reqStr("text"), reqStr("author"),
	)

	// ── platform:iam:role ───────────────────────────────────────────────
	m["platform:iam:role:created"] = obj(
		reqStr("roleId"), reqStr("code"), reqStr("displayName"),
		reqStr("applicationCode"), reqStrArray("permissions"),
	)
	m["platform:iam:role:updated"] = obj(
		reqStr("roleId"), optStr("displayName"), optStr("description"),
		reqStrArray("permissionsAdded"), reqStrArray("permissionsRemoved"),
	)
	m["platform:iam:role:deleted"] = obj(reqStr("roleId"), reqStr("code"))
	m["platform:iam:roles:synced"] = obj(
		reqStr("applicationCode"),
		reqU32("created"), reqU32("updated"), reqU32("deleted"),
		reqStrArray("syncedNames"),
	)

	// ── platform:iam:application ────────────────────────────────────────
	m["platform:iam:application:created"] = obj(
		reqStr("applicationId"), reqStr("code"), reqStr("name"), reqStr("applicationType"),
	)
	m["platform:iam:application:updated"] = obj(
		reqStr("applicationId"), optStr("name"), optStr("description"),
	)
	m["platform:iam:application:activated"] = obj(reqStr("applicationId"), reqStr("code"))
	m["platform:iam:application:deactivated"] = obj(reqStr("applicationId"), reqStr("code"))
	m["platform:iam:application:deleted"] = obj(
		reqStr("applicationId"), reqStr("code"), reqStr("name"),
	)
	m["platform:iam:application:service-account-provisioned"] = obj(
		reqStr("applicationId"), reqStr("applicationCode"),
		reqStr("serviceAccountId"), reqStr("serviceAccountCode"),
	)
	m["platform:iam:application:enabled-for-client"] = obj(
		reqStr("applicationId"), reqStr("clientId"), reqStr("configId"),
	)
	m["platform:iam:application:disabled-for-client"] = obj(
		reqStr("applicationId"), reqStr("clientId"), reqStr("configId"),
	)

	// ── platform:iam:anchor-domain ──────────────────────────────────────
	m["platform:iam:anchor-domain:created"] = obj(reqStr("anchorDomainId"), reqStr("domain"))
	m["platform:iam:anchor-domain:deleted"] = obj(reqStr("anchorDomainId"), reqStr("domain"))

	// ── platform:iam:auth-config ────────────────────────────────────────
	m["platform:iam:auth-config:created"] = obj(
		reqStr("authConfigId"), reqStr("emailDomain"), reqStr("configType"),
	)
	m["platform:iam:auth-config:updated"] = obj(reqStr("authConfigId"), reqStr("emailDomain"))
	m["platform:iam:auth-config:deleted"] = obj(reqStr("authConfigId"), reqStr("emailDomain"))

	// ── platform:admin:cors ─────────────────────────────────────────────
	m["platform:admin:cors:origin-added"] = obj(reqStr("originId"), reqStr("origin"))
	m["platform:admin:cors:origin-deleted"] = obj(reqStr("originId"), reqStr("origin"))

	// ── platform:admin:idp ──────────────────────────────────────────────
	m["platform:admin:idp:created"] = obj(
		reqStr("idpId"), reqStr("code"), reqStr("name"), reqStr("idpType"),
	)
	m["platform:admin:idp:updated"] = obj(reqStr("idpId"), optStr("name"))
	m["platform:admin:idp:deleted"] = obj(reqStr("idpId"), reqStr("code"))

	// ── platform:admin:edm ──────────────────────────────────────────────
	m["platform:admin:edm:created"] = obj(
		reqStr("mappingId"), reqStr("emailDomain"),
		reqStr("identityProviderId"), reqStr("scopeType"),
	)
	m["platform:admin:edm:updated"] = obj(reqStr("mappingId"), reqStr("emailDomain"))
	m["platform:admin:edm:deleted"] = obj(reqStr("mappingId"), reqStr("emailDomain"))

	// ── platform:admin:eventtype ────────────────────────────────────────
	m["platform:admin:eventtype:created"] = obj(
		reqStr("eventTypeId"), reqStr("code"), reqStr("name"), optStr("description"),
		reqStr("application"), reqStr("subdomain"), reqStr("aggregate"),
		reqStr("eventName"), optStr("clientId"),
	)
	m["platform:admin:eventtype:updated"] = obj(
		reqStr("eventTypeId"), optStr("name"), optStr("description"),
	)
	m["platform:admin:eventtype:archived"] = obj(reqStr("eventTypeId"), reqStr("code"))
	m["platform:admin:eventtype:deleted"] = obj(reqStr("eventTypeId"), reqStr("code"))
	m["platform:admin:eventtype:schema-added"] = obj(
		reqStr("eventTypeId"), reqStr("version"), reqStr("mimeType"), reqStr("schemaType"),
	)
	m["platform:admin:eventtype:schema-finalised"] = obj(
		reqStr("eventTypeId"), reqStr("version"), optStr("deprecatedVersion"),
	)
	m["platform:admin:eventtype:schema-deprecated"] = obj(reqStr("eventTypeId"), reqStr("version"))
	m["platform:admin:eventtypes:synced"] = obj(
		reqStr("applicationCode"),
		reqU32("created"), reqU32("updated"), reqU32("deleted"),
		reqStrArray("syncedCodes"),
	)

	// ── platform:admin:connection ───────────────────────────────────────
	m["platform:admin:connection:created"] = obj(
		reqStr("connectionId"), reqStr("code"), reqStr("name"), reqStr("endpoint"),
		reqStr("serviceAccountId"), optStr("clientId"),
	)
	m["platform:admin:connection:updated"] = obj(
		reqStr("connectionId"), reqStr("code"),
		optStr("name"), optStr("endpoint"), optStr("status"),
	)
	m["platform:admin:connection:deleted"] = obj(
		reqStr("connectionId"), reqStr("code"), optStr("clientId"),
	)

	// ── platform:admin:dispatch-pool ────────────────────────────────────
	m["platform:admin:dispatch-pool:created"] = obj(
		reqStr("dispatchPoolId"), reqStr("code"), reqStr("name"), optStr("clientId"),
	)
	m["platform:admin:dispatch-pool:updated"] = obj(
		reqStr("dispatchPoolId"), optStr("name"),
		optU32("rateLimit"), optU32("concurrency"),
	)
	m["platform:admin:dispatch-pool:archived"] = obj(reqStr("dispatchPoolId"), reqStr("code"))
	m["platform:admin:dispatch-pool:deleted"] = obj(reqStr("dispatchPoolId"), reqStr("code"))
	m["platform:admin:dispatch-pools:synced"] = obj(
		reqStr("applicationCode"),
		reqU32("created"), reqU32("updated"), reqU32("deleted"),
		reqStrArray("syncedCodes"),
	)

	// ── platform:admin:subscription ─────────────────────────────────────
	m["platform:admin:subscription:created"] = obj(
		reqStr("subscriptionId"), reqStr("code"), reqStr("name"),
		reqStr("connectionId"), reqStrArray("eventTypes"), optStr("clientId"),
	)
	m["platform:admin:subscription:updated"] = obj(
		reqStr("subscriptionId"), optStr("name"),
		reqStrArray("eventTypesAdded"), reqStrArray("eventTypesRemoved"),
	)
	m["platform:admin:subscription:paused"] = obj(reqStr("subscriptionId"), reqStr("code"))
	m["platform:admin:subscription:resumed"] = obj(reqStr("subscriptionId"), reqStr("code"))
	m["platform:admin:subscription:deleted"] = obj(reqStr("subscriptionId"), reqStr("code"))
	m["platform:admin:subscription:synced"] = obj(
		reqStr("applicationCode"),
		reqU32("created"), reqU32("updated"), reqU32("deleted"),
		reqStrArray("syncedCodes"),
	)

	return m
}

// ── DSL — mirrors platform_event_schemas.rs helpers ──────────────────────

type prop struct {
	name     string
	schema   any
	required bool
}

func obj(props ...prop) json.RawMessage {
	properties := map[string]any{}
	required := []string{}
	for _, p := range props {
		properties[p.name] = p.schema
		if p.required {
			required = append(required, p.name)
		}
	}
	return mustRaw(map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	})
}

func reqStr(name string) prop {
	return prop{name: name, schema: map[string]any{"type": "string"}, required: true}
}

func optStr(name string) prop {
	return prop{name: name, schema: map[string]any{"type": []string{"string", "null"}}}
}

func reqBool(name string) prop {
	return prop{name: name, schema: map[string]any{"type": "boolean"}, required: true}
}

func reqU32(name string) prop {
	return prop{name: name, schema: map[string]any{"type": "integer", "minimum": 0}, required: true}
}

func optU32(name string) prop {
	return prop{name: name, schema: map[string]any{"type": []string{"integer", "null"}, "minimum": 0}}
}

func reqStrArray(name string) prop {
	return prop{
		name:     name,
		schema:   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		required: true,
	}
}

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("seed: marshal schema: " + err.Error())
	}
	return json.RawMessage(b)
}
