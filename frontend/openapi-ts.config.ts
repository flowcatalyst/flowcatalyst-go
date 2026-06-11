import { defineConfig } from "@hey-api/openapi-ts";

// Default to the backend's committed OpenAPI lockfile — the same contract
// `make api-diff` gates in CI — so the SPA's generated types can never
// drift from what the Go server actually serves. OPENAPI_LIVE=true points
// at a running server instead (useful while iterating on an unmerged
// backend change).
const livePort = process.env.FC_API_PORT ?? "8080";
const openApiInput =
	process.env.OPENAPI_LIVE === "true"
		? `http://localhost:${livePort}/q/openapi`
		: "../api/openapi.lock.json";

export default defineConfig({
	input: openApiInput,
	parser: {
		patch: {
			schemas: {
				// The backend's jsontime/httpcompat Time types don't declare a
				// JSON schema, so the spec models them as an empty object — on
				// the wire they are RFC3339 strings. Patch at generate time so
				// every createdAt/updatedAt/… field types as `string`. Remove
				// when the backend gives Time a real schema (an SDK-coordinated
				// lockfile change).
				Time: (schema) => {
					delete schema.additionalProperties;
					delete schema.properties;
					schema.type = "string";
					schema.format = "date-time";
				},
			},
		},
	},
	output: {
		path: "src/api/generated",
	},
	postProcess: [],
	// Types only: the app's transport is the hand-rolled api/client.ts
	// (toasts, 401 handling, field errors). The previously-generated fetch
	// client + SDK were never imported by app code, and the retry layer
	// attached to them never executed.
	plugins: ["@hey-api/typescript"],
});
