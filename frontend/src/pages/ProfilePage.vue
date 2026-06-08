<script setup lang="ts">
import { ref, computed } from "vue";
import { useAuthStore } from "@/stores/auth";
import { toast } from "@/utils/errorBus";
import { getErrorMessage } from "@/utils/errors";
import {
	changePassword,
	sendChangePasswordEmailCode,
} from "@/api/changePassword";
import PasskeysSection from "@/components/PasskeysSection.vue";
import TwoFactorSection from "@/components/TwoFactorSection.vue";

const authStore = useAuthStore();

// ── Change password ─────────────────────────────────────────────────────────
const showChangePassword = ref(false);
const cpCurrent = ref("");
const cpNew = ref("");
const cpConfirm = ref("");
const cpCode = ref("");
const cpMfaRequired = ref(false);
const cpMethods = ref<string[]>([]);
const cpBusy = ref(false);
const cpError = ref("");

const cpHasEmailFactor = computed(() => cpMethods.value.includes("EMAIL_PIN"));
const cpHasTotpFactor = computed(() => cpMethods.value.includes("TOTP"));

function openChangePassword() {
	cpCurrent.value = "";
	cpNew.value = "";
	cpConfirm.value = "";
	cpCode.value = "";
	cpMfaRequired.value = false;
	cpMethods.value = [];
	cpError.value = "";
	showChangePassword.value = true;
}

async function submitChangePassword() {
	cpError.value = "";
	if (cpNew.value.length < 8) {
		cpError.value = "New password must be at least 8 characters.";
		return;
	}
	if (cpNew.value !== cpConfirm.value) {
		cpError.value = "New password and confirmation do not match.";
		return;
	}
	if (cpMfaRequired.value && !cpCode.value.trim()) {
		cpError.value = "Enter your two-factor code.";
		return;
	}
	cpBusy.value = true;
	try {
		const result = await changePassword({
			currentPassword: cpCurrent.value,
			newPassword: cpNew.value,
			code: cpMfaRequired.value ? cpCode.value.trim() : undefined,
		});
		if (result.ok) {
			toast.success("Password changed", "Your password has been updated.");
			showChangePassword.value = false;
			return;
		}
		if (result.mfaRequired) {
			// Reveal the 2FA step. For an email factor, send the code right away.
			cpMfaRequired.value = true;
			cpMethods.value = result.methods ?? [];
			if (cpMethods.value.includes("EMAIL_PIN") && !cpMethods.value.includes("TOTP")) {
				await sendEmailCode();
			}
			cpError.value = result.message ?? "Two-factor verification required.";
			return;
		}
		cpError.value = result.message ?? "Could not change your password.";
	} catch (e) {
		cpError.value = getErrorMessage(e, "Could not change your password.");
	} finally {
		cpBusy.value = false;
	}
}

async function sendEmailCode() {
	try {
		const r = await sendChangePasswordEmailCode();
		toast.success("Code sent", r.message);
	} catch (e) {
		toast.error("Send failed", getErrorMessage(e, "Could not send a code."));
	}
}
</script>

<template>
  <div class="page-container">
    <header class="page-header">
      <div>
        <h1 class="page-title">Profile</h1>
        <p class="page-subtitle">Manage your account settings</p>
      </div>
    </header>

    <div class="profile-grid">
      <!-- User Info Card -->
      <div class="fc-card">
        <h2 class="section-title">User Information</h2>
        <div class="profile-info">
          <div class="avatar-large">
            {{ authStore.userInitials }}
          </div>
          <div class="user-details">
            <h3>{{ authStore.displayName }}</h3>
            <p>{{ authStore.user?.email }}</p>
            <div class="roles-list">
              <Tag
                v-for="role in authStore.user?.roles || []"
                :key="role"
                :value="role"
                class="role-tag"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- Account Settings Card -->
      <div class="fc-card">
        <h2 class="section-title">Account Settings</h2>
        <div class="settings-form">
          <div class="form-field">
            <label>Display Name</label>
            <InputText :value="authStore.displayName" disabled class="w-full" />
          </div>
          <div class="form-field">
            <label>Email</label>
            <InputText :value="authStore.user?.email" disabled class="w-full" />
          </div>
          <div class="form-actions">
            <Button
              label="Change Password"
              icon="pi pi-key"
              outlined
              @click="openChangePassword"
            />
          </div>
        </div>
      </div>

      <!-- Passkeys Card -->
      <PasskeysSection />

      <!-- Two-Factor Authentication Card -->
      <TwoFactorSection />

      <!-- Security Card -->
      <div class="fc-card">
        <h2 class="section-title">Security</h2>
        <div class="security-info">
          <div class="security-item">
            <div class="security-icon">
              <i class="pi pi-clock"></i>
            </div>
            <div class="security-details">
              <h4>Session Management</h4>
              <p>View and manage your active sessions</p>
            </div>
            <Button label="View" outlined size="small" />
          </div>
        </div>
      </div>
    </div>

    <!-- Change Password dialog -->
    <Dialog
      v-model:visible="showChangePassword"
      header="Change Password"
      modal
      :style="{ width: '28rem' }"
    >
      <div class="cp-form">
        <div class="cp-field">
          <label for="cp-current">Current password</label>
          <Password
            id="cp-current"
            v-model="cpCurrent"
            toggleMask
            :feedback="false"
            inputClass="w-full"
            class="w-full"
          />
        </div>
        <div class="cp-field">
          <label for="cp-new">New password</label>
          <Password id="cp-new" v-model="cpNew" toggleMask inputClass="w-full" class="w-full" />
        </div>
        <div class="cp-field">
          <label for="cp-confirm">Confirm new password</label>
          <Password
            id="cp-confirm"
            v-model="cpConfirm"
            toggleMask
            :feedback="false"
            inputClass="w-full"
            class="w-full"
          />
        </div>

        <div v-if="cpMfaRequired" class="cp-field cp-mfa">
          <label for="cp-code">Two-factor code</label>
          <InputText
            id="cp-code"
            v-model="cpCode"
            placeholder="Enter your code"
            class="w-full"
            autocomplete="one-time-code"
          />
          <small v-if="cpHasTotpFactor" class="cp-hint">
            Enter the code from your authenticator app{{ cpHasEmailFactor ? ", or send an email code below." : "." }}
          </small>
          <small v-else-if="cpHasEmailFactor" class="cp-hint">We sent a code to your email.</small>
          <Button
            v-if="cpHasEmailFactor"
            label="Send email code"
            icon="pi pi-envelope"
            text
            size="small"
            class="cp-send"
            @click="sendEmailCode"
          />
        </div>

        <p v-if="cpError" class="cp-error">{{ cpError }}</p>
      </div>
      <template #footer>
        <Button label="Cancel" text severity="secondary" @click="showChangePassword = false" />
        <Button
          :label="cpMfaRequired ? 'Verify & Change' : 'Change Password'"
          icon="pi pi-check"
          :loading="cpBusy"
          @click="submitChangePassword"
        />
      </template>
    </Dialog>
  </div>
</template>

<style scoped>
.profile-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
  gap: 24px;
}

.section-title {
  font-size: 16px;
  font-weight: 600;
  color: #243b53;
  margin: 0 0 20px;
  padding-bottom: 12px;
  border-bottom: 1px solid #e2e8f0;
}

.profile-info {
  display: flex;
  gap: 20px;
  align-items: flex-start;
}

.avatar-large {
  width: 80px;
  height: 80px;
  border-radius: 50%;
  background: linear-gradient(135deg, #0967d2 0%, #47a3f3 100%);
  color: white;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 600;
  font-size: 28px;
  flex-shrink: 0;
}

.user-details h3 {
  margin: 0 0 4px;
  font-size: 18px;
  color: #1e293b;
}

.user-details p {
  margin: 0 0 12px;
  color: #64748b;
  font-size: 14px;
}

.roles-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.role-tag {
  font-size: 12px;
}

.settings-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.form-field label {
  font-size: 14px;
  font-weight: 500;
  color: #334e68;
}

.form-actions {
  margin-top: 8px;
}

.security-info {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.security-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px;
  background: #f8fafc;
  border-radius: 8px;
}

.security-icon {
  width: 40px;
  height: 40px;
  border-radius: 8px;
  background: #e2e8f0;
  display: flex;
  align-items: center;
  justify-content: center;
}

.security-icon i {
  font-size: 18px;
  color: #475569;
}

.security-details {
  flex: 1;
}

.security-details h4 {
  margin: 0 0 4px;
  font-size: 14px;
  font-weight: 500;
  color: #1e293b;
}

.security-details p {
  margin: 0;
  font-size: 13px;
  color: #64748b;
}

.cp-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding-top: 8px;
}

.cp-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.cp-field label {
  font-size: 14px;
  font-weight: 500;
  color: #334e68;
}

.cp-mfa {
  border-top: 1px solid #e2e8f0;
  padding-top: 16px;
}

.cp-hint {
  font-size: 12px;
  color: #64748b;
}

.cp-send {
  align-self: flex-start;
  padding-left: 0;
}

.cp-error {
  margin: 0;
  font-size: 13px;
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  border-radius: 6px;
  padding: 8px 12px;
}

.w-full {
  width: 100%;
}
</style>
