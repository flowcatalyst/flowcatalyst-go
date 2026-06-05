// Two-factor auth API. Endpoints live under /auth/2fa/* (NOT /api), mirroring
// the rest of the auth surface (see auth.ts, webauthn.ts).
import {
	applyLoginSuccess,
	setSessionUser,
	type RawLoginResponse,
} from "./auth";

export { redirectAfterLogin } from "./auth";

const AUTH = "/auth";

export type TwoFactorMethod = "TOTP" | "EMAIL_PIN";
export type ChallengeMethod = TwoFactorMethod | "RECOVERY_CODE";

async function req<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const res = await fetch(`${AUTH}${path}`, {
		method,
		headers: body ? { "Content-Type": "application/json" } : undefined,
		body: body ? JSON.stringify(body) : undefined,
		credentials: "include",
	});
	if (!res.ok) {
		const e = await res.json().catch(() => ({}));
		throw new Error(e.message || e.error || "Request failed");
	}
	// Some endpoints (204-ish) may have an empty body.
	const text = await res.text();
	return (text ? JSON.parse(text) : {}) as T;
}

const post = <T>(path: string, body?: unknown) => req<T>("POST", path, body ?? {});
const get = <T>(path: string) => req<T>("GET", path);
const del = <T>(path: string) => req<T>("DELETE", path);

export interface TotpEnrollment {
	secret: string;
	uri: string;
}

// ── login challenge (token-gated) ──────────────────────────────────────────

export async function verifyTwoFactor(args: {
	mfaToken: string;
	method: ChallengeMethod;
	code: string;
	rememberDevice?: boolean;
}): Promise<void> {
	const data = await post<RawLoginResponse>("/2fa/verify", args);
	applyLoginSuccess(data);
}

export function sendEmailChallenge(mfaToken: string): Promise<{ message: string }> {
	return post("/2fa/challenge/email", { mfaToken });
}

// ── enrollment during login/reset (enroll-token-gated) ─────────────────────
// The confirm endpoints set the session cookie and complete the login.

export function enrollTotpBegin(enrollToken: string): Promise<TotpEnrollment> {
	return post("/2fa/enroll/totp/begin", { enrollToken });
}

export async function enrollTotpConfirm(
	enrollToken: string,
	code: string,
): Promise<string[]> {
	const data = await post<RawLoginResponse>("/2fa/enroll/totp/confirm", {
		enrollToken,
		code,
	});
	// Session is established; defer navigation so the caller can show the
	// recovery codes first, then call redirectAfterLogin().
	setSessionUser(data);
	return data.recoveryCodes ?? [];
}

export function enrollEmailBegin(enrollToken: string): Promise<{ message: string }> {
	return post("/2fa/enroll/email/begin", { enrollToken });
}

export async function enrollEmailConfirm(
	enrollToken: string,
	code: string,
): Promise<string[]> {
	const data = await post<RawLoginResponse>("/2fa/enroll/email/confirm", {
		enrollToken,
		code,
	});
	setSessionUser(data);
	return data.recoveryCodes ?? [];
}

// ── self-service (session-gated) ───────────────────────────────────────────

export interface TwoFactorStatus {
	methods: TwoFactorMethod[];
	required: boolean;
	allowedMethods: TwoFactorMethod[];
	recoveryCodesLeft: number;
	rememberDeviceEnabled: boolean;
	trustedDeviceCount: number;
}

export function getTwoFactorStatus(): Promise<TwoFactorStatus> {
	return get("/2fa/status");
}

export function selfEnrollTotpBegin(): Promise<TotpEnrollment> {
	return post("/2fa/methods/totp/begin", {});
}

export function selfEnrollTotpConfirm(
	code: string,
): Promise<{ recoveryCodes: string[] }> {
	return post("/2fa/methods/totp/confirm", { code });
}

export function selfEnrollEmailBegin(): Promise<{ message: string }> {
	return post("/2fa/methods/email/begin", {});
}

export function selfEnrollEmailConfirm(
	code: string,
): Promise<{ recoveryCodes: string[] }> {
	return post("/2fa/methods/email/confirm", { code });
}

export function removeTwoFactorMethod(
	method: TwoFactorMethod,
): Promise<{ message: string }> {
	return del(`/2fa/methods/${method}`);
}

export function regenerateRecoveryCodes(): Promise<{ recoveryCodes: string[] }> {
	return post("/2fa/recovery-codes/regenerate", {});
}

export interface TrustedDevice {
	id: string;
	label?: string;
	createdAt: string;
	lastUsedAt?: string;
	expiresAt: string;
}

export function listTrustedDevices(): Promise<{ devices: TrustedDevice[] }> {
	return get("/2fa/trusted-devices");
}

export function revokeTrustedDevice(id: string): Promise<{ message: string }> {
	return del(`/2fa/trusted-devices/${id}`);
}

export function methodLabel(method: string): string {
	switch (method) {
		case "TOTP":
			return "Authenticator app";
		case "EMAIL_PIN":
			return "Email code";
		default:
			return method;
	}
}
