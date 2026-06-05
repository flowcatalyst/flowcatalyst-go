import { useAuthStore, type User } from "@/stores/auth";
import router from "@/router";
import { getErrorMessage } from "@/utils/errors";
import type { TwoFactorMethod } from "./twofactor";

// Auth endpoints are at /auth/* (not /api/auth/*)
const AUTH_URL = "/auth";

interface LoginCredentials {
	email: string;
	password: string;
}

interface LoginResponse {
	principalId: string;
	name: string;
	email: string;
	roles: string[];
	permissions?: string[];
	clientId: string | null;
}

// RawLoginResponse is the on-the-wire shape of /auth/login and the 2FA
// completion endpoints (verify / enroll-confirm): an "ok" payload carries the
// principal; the pending statuses carry a token + method list instead.
export interface RawLoginResponse extends Partial<LoginResponse> {
	status?: "ok" | "mfa_required" | "enrollment_required";
	mfaToken?: string;
	enrollToken?: string;
	methods?: TwoFactorMethod[];
	allowedMethods?: TwoFactorMethod[];
	rememberDeviceAllowed?: boolean;
	recoveryCodes?: string[];
}

// LoginResult is what the SPA branches on after a password submit.
export type LoginResult =
	| { status: "ok" }
	| {
			status: "mfa_required";
			mfaToken: string;
			methods: TwoFactorMethod[];
			rememberDeviceAllowed: boolean;
	  }
	| {
			status: "enrollment_required";
			enrollToken: string;
			allowedMethods: TwoFactorMethod[];
	  };

export interface DomainCheckResponse {
	authMethod: "internal" | "external";
	loginUrl?: string;
	idpIssuer?: string;
}

function mapLoginResponseToUser(response: LoginResponse): User {
	return {
		id: response.principalId,
		email: response.email,
		name: response.name,
		clientId: response.clientId,
		roles: response.roles,
		// Flat list of permission codes the backend resolved from the
		// user's roles. Empty when the backend doesn't ship them.
		permissions: response.permissions ?? [],
	};
}

export async function checkEmailDomain(
	email: string,
): Promise<DomainCheckResponse> {
	const response = await fetch(`${AUTH_URL}/check-domain`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ email }),
		credentials: "include",
	});

	if (!response.ok) {
		throw new Error("Failed to check email domain");
	}

	return response.json();
}

export async function checkSession(): Promise<boolean> {
	const authStore = useAuthStore();
	authStore.setLoading(true);

	try {
		const response = await fetch(`${AUTH_URL}/me`, {
			credentials: "include",
		});

		if (!response.ok) {
			authStore.clearAuth();
			return false;
		}

		const data: LoginResponse = await response.json();
		authStore.setUser(mapLoginResponseToUser(data));
		return true;
	} catch {
		authStore.clearAuth();
		return false;
	}
}

// setSessionUser records the authenticated user in the store (the session
// cookie is already set server-side). It does NOT navigate — callers that need
// to show something first (e.g. recovery codes) call redirectAfterLogin later.
export function setSessionUser(data: RawLoginResponse): void {
	const authStore = useAuthStore();
	authStore.setUser(mapLoginResponseToUser(data as LoginResponse));
}

// redirectAfterLogin performs the post-login navigation: OIDC interaction,
// OAuth authorize round-trip, or the dashboard.
export function redirectAfterLogin(): void {
	const urlParams = new URLSearchParams(window.location.search);
	const interactionUid = urlParams.get("interaction");
	if (interactionUid) {
		window.location.href = `/oidc/interaction/${interactionUid}/login`;
		return;
	}
	if (urlParams.get("oauth") === "true") {
		const oauthParams = new URLSearchParams();
		const oauthFields = [
			"response_type",
			"client_id",
			"redirect_uri",
			"scope",
			"state",
			"code_challenge",
			"code_challenge_method",
			"nonce",
		];
		for (const field of oauthFields) {
			const value = urlParams.get(field);
			if (value) oauthParams.set(field, value);
		}
		window.location.href = `/oauth/authorize?${oauthParams.toString()}`;
		return;
	}
	void router.replace("/dashboard");
}

// applyLoginSuccess = set user + redirect. Used by the password and 2FA-verify
// paths (no interstitial). The enroll-and-complete path uses setSessionUser +
// redirectAfterLogin separately so it can show recovery codes in between.
export function applyLoginSuccess(data: RawLoginResponse): void {
	setSessionUser(data);
	redirectAfterLogin();
}

export async function login(
	credentials: LoginCredentials,
): Promise<LoginResult> {
	const authStore = useAuthStore();
	authStore.setLoading(true);
	authStore.setError(null);

	try {
		const response = await fetch(`${AUTH_URL}/login`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(credentials),
			credentials: "include",
		});

		if (!response.ok) {
			const errorData = await response.json().catch(() => ({}));
			throw new Error(
				errorData.message ||
					errorData.error ||
					"Login failed. Please check your credentials.",
			);
		}

		const data: RawLoginResponse = await response.json();

		// 2FA pending: no session yet — hand the token back to the caller.
		if (data.status === "mfa_required") {
			authStore.setLoading(false);
			return {
				status: "mfa_required",
				mfaToken: data.mfaToken ?? "",
				methods: data.methods ?? [],
				rememberDeviceAllowed: data.rememberDeviceAllowed ?? false,
			};
		}
		if (data.status === "enrollment_required") {
			authStore.setLoading(false);
			return {
				status: "enrollment_required",
				enrollToken: data.enrollToken ?? "",
				allowedMethods: data.allowedMethods ?? [],
			};
		}

		applyLoginSuccess(data);
		return { status: "ok" };
	} catch (error: unknown) {
		authStore.setLoading(false);
		authStore.setError(getErrorMessage(error, "Login failed"));
		throw error;
	}
}

export async function logout(): Promise<void> {
	const authStore = useAuthStore();

	try {
		await fetch(`${AUTH_URL}/logout`, {
			method: "POST",
			credentials: "include",
		});
	} catch {
		// Ignore errors - clear local state anyway
	}

	authStore.clearAuth();
	// Use replace to clear navigation history on logout
	await router.replace("/auth/login");
}

export async function requestPasswordReset(email: string): Promise<void> {
	const response = await fetch(`${AUTH_URL}/password-reset/request`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ email }),
		credentials: "include",
	});

	if (!response.ok) {
		const errorData = await response.json().catch(() => ({}));
		throw new Error(
			errorData.error || "Failed to request password reset.",
		);
	}
}

export async function validateResetToken(
	token: string,
): Promise<{ valid: boolean; reason?: string }> {
	const response = await fetch(
		`${AUTH_URL}/password-reset/validate?token=${encodeURIComponent(token)}`,
		{ credentials: "include" },
	);

	if (!response.ok) {
		return { valid: false, reason: "not_found" };
	}

	return response.json();
}

export interface ConfirmPasswordResetResult {
	status: "ok" | "enrollment_required";
	message: string;
	enrollToken?: string;
	allowedMethods?: TwoFactorMethod[];
}

export async function confirmPasswordReset(
	token: string,
	password: string,
): Promise<ConfirmPasswordResetResult> {
	const response = await fetch(`${AUTH_URL}/password-reset/confirm`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ token, password }),
		credentials: "include",
	});

	if (!response.ok) {
		const errorData = await response.json().catch(() => ({}));
		throw new Error(errorData.message || errorData.error || "Failed to reset password.");
	}

	return response.json();
}

export async function switchClient(clientId: string): Promise<void> {
	const authStore = useAuthStore();

	try {
		const response = await fetch(`${AUTH_URL}/client/${clientId}`, {
			method: "POST",
			credentials: "include",
		});

		if (!response.ok) {
			const errorData = await response.json().catch(() => ({}));
			throw new Error(errorData.message || "Failed to switch client");
		}

		authStore.selectClient(clientId);
	} catch (error: unknown) {
		authStore.setError(getErrorMessage(error, "Failed to switch client"));
		throw error;
	}
}
