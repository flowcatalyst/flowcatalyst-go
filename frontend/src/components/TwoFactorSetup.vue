<script setup lang="ts">
import { ref } from "vue";
import {
	enrollEmailBegin,
	enrollEmailConfirm,
	enrollTotpBegin,
	enrollTotpConfirm,
	redirectAfterLogin,
	selfEnrollEmailBegin,
	selfEnrollEmailConfirm,
	selfEnrollTotpBegin,
	selfEnrollTotpConfirm,
	type TotpEnrollment,
	type TwoFactorMethod,
} from "@/api/twofactor";
import { getErrorMessage } from "@/utils/errors";

const props = defineProps<{
	// When set, enrollment runs in the post-login "enroll token" mode and the
	// session is completed on confirm. When absent, it runs in session mode
	// (Profile) for an already-authenticated user.
	enrollToken?: string;
	allowedMethods: TwoFactorMethod[];
}>();

const emit = defineEmits<{ (e: "done"): void }>();

type Stage = "choose" | "totp" | "email" | "recovery";
const stage = ref<Stage>("choose");
const busy = ref(false);
const error = ref<string | null>(null);

const totp = ref<TotpEnrollment | null>(null);
const code = ref("");
const recoveryCodes = ref<string[]>([]);

const isTokenMode = () => !!props.enrollToken;

async function chooseTotp() {
	error.value = null;
	busy.value = true;
	try {
		totp.value = isTokenMode()
			? await enrollTotpBegin(props.enrollToken!)
			: await selfEnrollTotpBegin();
		code.value = "";
		stage.value = "totp";
	} catch (e) {
		error.value = getErrorMessage(e, "Could not start setup");
	} finally {
		busy.value = false;
	}
}

async function chooseEmail() {
	error.value = null;
	busy.value = true;
	try {
		if (isTokenMode()) await enrollEmailBegin(props.enrollToken!);
		else await selfEnrollEmailBegin();
		code.value = "";
		stage.value = "email";
	} catch (e) {
		error.value = getErrorMessage(e, "Could not send code");
	} finally {
		busy.value = false;
	}
}

async function confirmTotp() {
	if (busy.value || !code.value) return;
	error.value = null;
	busy.value = true;
	try {
		const codes = isTokenMode()
			? await enrollTotpConfirm(props.enrollToken!, code.value.trim())
			: (await selfEnrollTotpConfirm(code.value.trim())).recoveryCodes;
		finishOrShowCodes(codes);
	} catch (e) {
		error.value = getErrorMessage(e, "That code didn't match");
	} finally {
		busy.value = false;
	}
}

async function confirmEmail() {
	if (busy.value || !code.value) return;
	error.value = null;
	busy.value = true;
	try {
		const codes = isTokenMode()
			? await enrollEmailConfirm(props.enrollToken!, code.value.trim())
			: (await selfEnrollEmailConfirm(code.value.trim())).recoveryCodes;
		finishOrShowCodes(codes);
	} catch (e) {
		error.value = getErrorMessage(e, "That code didn't match");
	} finally {
		busy.value = false;
	}
}

function finishOrShowCodes(codes: string[]) {
	if (codes.length > 0) {
		recoveryCodes.value = codes;
		stage.value = "recovery";
		return;
	}
	finish();
}

function finish() {
	if (isTokenMode()) redirectAfterLogin();
	else emit("done");
}

function copyCodes() {
	void navigator.clipboard?.writeText(recoveryCodes.value.join("\n"));
}
</script>

<template>
  <div class="tfa-setup">
    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- Choose a method -->
    <template v-if="stage === 'choose'">
      <p class="hint">
        Two-factor authentication adds a second step when you sign in with a
        password. Choose how you'd like to receive your codes.
      </p>
      <div class="tfa-methods">
        <Button
          v-if="allowedMethods.includes('TOTP')"
          label="Use an authenticator app"
          icon="pi pi-mobile"
          :loading="busy"
          @click="chooseTotp"
        />
        <Button
          v-if="allowedMethods.includes('EMAIL_PIN')"
          label="Use email codes"
          icon="pi pi-envelope"
          severity="secondary"
          :loading="busy"
          @click="chooseEmail"
        />
      </div>
    </template>

    <!-- TOTP -->
    <template v-else-if="stage === 'totp'">
      <p class="hint">
        Scan or paste this secret into your authenticator app (Google
        Authenticator, 1Password, Authy…), then enter the 6-digit code.
      </p>
      <div v-if="totp" class="tfa-secret">
        <code>{{ totp.secret }}</code>
        <a :href="totp.uri" class="tfa-uri-link">Open in app</a>
      </div>
      <InputText
        v-model="code"
        placeholder="123456"
        inputmode="numeric"
        autocomplete="one-time-code"
        @keyup.enter="confirmTotp"
      />
      <div class="tfa-actions">
        <Button label="Back" text @click="stage = 'choose'" />
        <Button label="Verify" :loading="busy" :disabled="!code" @click="confirmTotp" />
      </div>
    </template>

    <!-- Email PIN -->
    <template v-else-if="stage === 'email'">
      <p class="hint">We sent a code to your email. Enter it below.</p>
      <InputText
        v-model="code"
        placeholder="123456"
        inputmode="numeric"
        autocomplete="one-time-code"
        @keyup.enter="confirmEmail"
      />
      <div class="tfa-actions">
        <Button label="Back" text @click="stage = 'choose'" />
        <Button label="Verify" :loading="busy" :disabled="!code" @click="confirmEmail" />
      </div>
    </template>

    <!-- Recovery codes -->
    <template v-else-if="stage === 'recovery'">
      <p class="hint">
        Save these recovery codes somewhere safe. Each can be used once if you
        lose access to your second factor.
      </p>
      <ul class="tfa-recovery">
        <li v-for="c in recoveryCodes" :key="c"><code>{{ c }}</code></li>
      </ul>
      <div class="tfa-actions">
        <Button label="Copy codes" icon="pi pi-copy" text @click="copyCodes" />
        <Button label="I've saved them — continue" @click="finish" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.tfa-setup {
	display: flex;
	flex-direction: column;
	gap: 1rem;
}
.tfa-methods,
.tfa-actions {
	display: flex;
	gap: 0.75rem;
	flex-wrap: wrap;
}
.tfa-actions {
	justify-content: space-between;
}
.tfa-secret {
	display: flex;
	align-items: center;
	gap: 1rem;
	padding: 0.75rem 1rem;
	background: var(--surface-100, #f3f4f6);
	border-radius: 6px;
	font-size: 1.05rem;
	letter-spacing: 1px;
	word-break: break-all;
}
.tfa-uri-link {
	white-space: nowrap;
}
.tfa-recovery {
	list-style: none;
	padding: 1rem;
	margin: 0;
	background: var(--surface-100, #f3f4f6);
	border-radius: 6px;
	columns: 2;
	font-size: 1.05rem;
}
.tfa-recovery li {
	padding: 0.15rem 0;
}
</style>
