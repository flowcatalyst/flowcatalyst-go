import { apiFetch } from "./client";
import type {
	ApplicationAccessListResponse as GenApplicationAccessListResponse,
	ApplicationAccessResponse,
	BulkImportResponse as GenBulkImportResponse,
	BulkImportResult,
	CheckEmailDomainResponse,
	ClientAccessGrantResponse,
	PrincipalAvailableApplication,
	PrincipalAvailableApplicationsResponse,
	PrincipalListResponse,
	PrincipalResponse,
	PrincipalRoleAssignmentDto,
	RolesAssignedResponse as GenRolesAssignedResponse,
	SetApplicationAccessResponse,
} from "./generated";

// Request-side string unions the forms rely on. The generated response
// types deliberately stay `string` (the spec doesn't carry enums — see
// docs/frontend-api-types-adoption.md on SDK coordination).
export type PrincipalType = "USER" | "SERVICE";
export type IdpType = "INTERNAL" | "OIDC" | "SAML";
export type PrincipalScope = "ANCHOR" | "PARTNER" | "CLIENT";

// Response types alias the generated contract (api/openapi.lock.json) so
// `vue-tsc` fails on backend drift. Aliased under the historical names so
// pages keep their imports.
export type User = PrincipalResponse;
export type UserListResponse = PrincipalListResponse;
export type ClientAccessGrant = ClientAccessGrantResponse;
export type RoleAssignment = PrincipalRoleAssignmentDto;
export type RolesAssignedResponse = GenRolesAssignedResponse;
export type ApplicationAccessGrant = ApplicationAccessResponse;
export type ApplicationAccessListResponse = GenApplicationAccessListResponse;
export type ApplicationAccessAssignedResponse = SetApplicationAccessResponse;
export type AvailableApplication = PrincipalAvailableApplication;
export type AvailableApplicationsResponse =
	PrincipalAvailableApplicationsResponse;
export type EmailDomainCheckResponse = CheckEmailDomainResponse;
export type BulkImportResultRow = BulkImportResult;
export type BulkImportResponse = GenBulkImportResponse;

export interface CreateUserRequest {
	email: string;
	password?: string; // Optional - only required for INTERNAL auth users
	name: string;
	clientId?: string;
}

export interface UpdateUserRequest {
	name?: string;
	active?: boolean;
	/** Optional; asserted against the stored email — a different value is rejected, not a rename. */
	email?: string;
}

/** Explicit-intent change of a user's scope/client association (anchor-gated). */
export type ClientAssociationMode = "CHANGE_CLIENT" | "TO_PARTNER";

export interface UserFilters {
	clientId?: string;
	type?: PrincipalType;
	active?: boolean;
	q?: string;
	roles?: string[];
	page?: number;
	pageSize?: number;
	sortField?: string;
	sortOrder?: string;
}

export const usersApi = {
	list(filters?: UserFilters): Promise<UserListResponse> {
		const params = new URLSearchParams();
		if (filters?.clientId) params.append("clientId", filters.clientId);
		if (filters?.type) params.append("type", filters.type);
		if (filters?.active !== undefined)
			params.append("active", String(filters.active));
		if (filters?.q) params.append("q", filters.q);
		if (filters?.roles?.length) params.append("roles", filters.roles.join(","));
		if (filters?.page !== undefined)
			params.append("page", String(filters.page));
		if (filters?.pageSize !== undefined)
			params.append("pageSize", String(filters.pageSize));
		if (filters?.sortField) params.append("sortField", filters.sortField);
		if (filters?.sortOrder) params.append("sortOrder", filters.sortOrder);

		const query = params.toString();
		return apiFetch(`/principals${query ? `?${query}` : ""}`);
	},

	get(id: string): Promise<User> {
		return apiFetch(`/principals/${id}`);
	},

	create(data: CreateUserRequest): Promise<User> {
		return apiFetch("/principals/users", {
			method: "POST",
			body: JSON.stringify(data),
		});
	},

	update(id: string, data: UpdateUserRequest): Promise<User> {
		return apiFetch(`/principals/${id}`, {
			method: "PUT",
			body: JSON.stringify(data),
		});
	},

	/**
	 * Change a user's scope/client association with explicit intent (anchor-gated).
	 * Pass clientId "*" to make the user an ANCHOR; otherwise supply a mode:
	 * CHANGE_CLIENT (replace home client) or TO_PARTNER (promote to PARTNER,
	 * keeping the old client and adding the new one).
	 */
	setClientAssociation(
		id: string,
		clientId: string,
		mode?: ClientAssociationMode,
	): Promise<User> {
		return apiFetch(`/principals/${id}/client-association`, {
			method: "PUT",
			body: JSON.stringify({ clientId, mode }),
		});
	},

	activate(id: string): Promise<{ message: string }> {
		return apiFetch(`/principals/${id}/activate`, {
			method: "POST",
		});
	},

	deactivate(id: string): Promise<{ message: string }> {
		return apiFetch(`/principals/${id}/deactivate`, {
			method: "POST",
		});
	},

	resetPassword(id: string, newPassword: string): Promise<{ message: string }> {
		return apiFetch(`/principals/${id}/reset-password`, {
			method: "POST",
			body: JSON.stringify({ newPassword }),
		});
	},

	/**
	 * Trigger a password reset email for an internal-auth user.
	 * Sends the same single-use email as the user-initiated /auth/password-reset/request flow.
	 * Rejects OIDC users and users without an email.
	 */
	sendPasswordReset(id: string): Promise<{ message: string }> {
		return apiFetch(`/principals/${id}/send-password-reset`, {
			method: "POST",
		});
	},

	/**
	 * Clear a user's enrolled 2FA (factors, recovery codes, pending PINs, trusted
	 * devices). The user must re-enroll at next sign-in if their domain requires
	 * 2FA — i.e. this re-triggers 2FA onboarding for a lost-device recovery.
	 */
	resetTwoFactor(id: string): Promise<{ message: string }> {
		return apiFetch(`/principals/${id}/reset-2fa`, {
			method: "POST",
		});
	},

	/**
	 * Create a CLIENT-scope user in a specific client. Used by the client-admin
	 * user-management page; goes through POST /principals with an explicit scope
	 * (the email-domain-driven /principals/users path derives scope instead).
	 */
	createClientUser(data: {
		email: string;
		name: string;
		password?: string;
		clientId: string;
	}): Promise<{ id: string }> {
		return apiFetch("/principals", {
			method: "POST",
			body: JSON.stringify({ ...data, scope: "CLIENT" }),
		});
	},

	// Client access grants
	getClientAccess(id: string): Promise<{ grants: ClientAccessGrant[] }> {
		return apiFetch(`/principals/${id}/client-access`);
	},

	grantClientAccess(id: string, clientId: string): Promise<ClientAccessGrant> {
		return apiFetch(`/principals/${id}/client-access`, {
			method: "POST",
			body: JSON.stringify({ clientId }),
		});
	},

	revokeClientAccess(id: string, clientId: string): Promise<void> {
		return apiFetch(`/principals/${id}/client-access/${clientId}`, {
			method: "DELETE",
		});
	},

	delete(id: string): Promise<void> {
		return apiFetch(`/principals/${id}`, {
			method: "DELETE",
		});
	},

	checkEmailDomain(email: string): Promise<EmailDomainCheckResponse> {
		return apiFetch(
			`/principals/check-email-domain?email=${encodeURIComponent(email)}`,
		);
	},

	// Role management
	getRoles(id: string): Promise<{ roles: RoleAssignment[] }> {
		return apiFetch(`/principals/${id}/roles`);
	},

	assignRole(id: string, roleName: string): Promise<RoleAssignment> {
		return apiFetch(`/principals/${id}/roles`, {
			method: "POST",
			body: JSON.stringify({ roleName }),
		});
	},

	removeRole(id: string, roleName: string): Promise<void> {
		return apiFetch(
			`/principals/${id}/roles/${encodeURIComponent(roleName)}`,
			{
				method: "DELETE",
			},
		);
	},

	/**
	 * Batch assign roles to a user.
	 * This is a declarative operation - sets the complete role list.
	 * Roles not in the list will be removed, new roles will be added.
	 */
	assignRoles(id: string, roles: string[]): Promise<RolesAssignedResponse> {
		return apiFetch(`/principals/${id}/roles`, {
			method: "PUT",
			body: JSON.stringify({ roles }),
		});
	},

	// Application access management

	/**
	 * Get the application access grants for a user.
	 */
	getApplicationAccess(id: string): Promise<ApplicationAccessListResponse> {
		return apiFetch(`/principals/${id}/application-access`);
	},

	/**
	 * Get applications available to grant to a user.
	 * Returns applications that are enabled for at least one of the user's accessible clients.
	 */
	getAvailableApplications(id: string): Promise<AvailableApplicationsResponse> {
		return apiFetch(`/principals/${id}/available-applications`);
	},

	/**
	 * Batch assign application access to a user.
	 * This is a declarative operation - sets the complete application access list.
	 * Applications not in the list will be removed, new applications will be added.
	 */
	assignApplicationAccess(
		id: string,
		applicationIds: string[],
	): Promise<ApplicationAccessAssignedResponse> {
		return apiFetch(`/principals/${id}/application-access`, {
			method: "PUT",
			body: JSON.stringify({ applicationIds }),
		});
	},

	/**
	 * Bulk-import CLIENT users for a client (CSV onboarding). Missing users are
	 * created with the listed roles (validated against the client's apps);
	 * existing users are skipped. Returns a per-row outcome.
	 */
	bulkImport(
		clientId: string,
		users: BulkImportUserRow[],
	): Promise<BulkImportResponse> {
		return apiFetch("/principals/bulk-import", {
			method: "POST",
			body: JSON.stringify({ clientId, users }),
		});
	},
};

// Request shape for bulkImport (hand-rolled: the CSV form always sends
// roles, so it stays required here even though the wire accepts absent).
export interface BulkImportUserRow {
	name: string;
	email: string;
	roles: string[];
}
