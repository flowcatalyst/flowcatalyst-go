<script setup lang="ts">
import { toast } from "@/utils/errorBus";
import { ref, computed, onMounted } from "vue";
import { useRoute } from "vue-router";
import { oauthClientsApi, type OAuthClient } from "@/api/oauth-clients";
import { applicationsApi, type Application } from "@/api/applications";
import { getErrorMessage } from "@/utils/errors";
import { useReturnTo } from "@/composables/useReturnTo";

const route = useRoute();
const { returnTo } = useReturnTo();

const client = ref<OAuthClient | null>(null);
const applications = ref<Application[]>([]);
const loading = ref(true);
const saving = ref(false);
// `error` is the PAGE-level (load) error — it replaces the whole card. Save
// failures use `saveError`, shown inline in the edit form, so a failed save
// never blanks the form the user is still editing.
const error = ref<string | null>(null);
const saveError = ref<string | null>(null);

// Edit mode
const isEditing = ref(false);
const editForm = ref({
	clientName: "",
	redirectUris: [] as string[],
	postLogoutRedirectUris: [] as string[],
	allowedOrigins: [] as string[],
	grantTypes: [] as string[],
	defaultScopes: [] as string[],
	pkceRequired: true,
	applicationIds: [] as string[],
});
const newRedirectUri = ref("");
const newPostLogoutRedirectUri = ref("");
const newAllowedOrigin = ref("");

// Secret rotation dialog
const showRotateSecretDialog = ref(false);
const rotateLoading = ref(false);
const showNewSecretDialog = ref(false);
const newClientSecret = ref<string | null>(null);

const grantTypeOptions = [
	{ label: "Authorization Code", value: "authorization_code" },
	{ label: "Refresh Token", value: "refresh_token" },
	{ label: "Client Credentials", value: "client_credentials" },
];

const scopeOptions = [
	{ label: "openid", value: "openid" },
	{ label: "profile", value: "profile" },
	{ label: "email", value: "email" },
	{ label: "offline_access", value: "offline_access" },
];

// Redirect URIs are required for authorization_code or refresh_token grants
const requiresRedirectUri = computed(() => {
	return (
		editForm.value.grantTypes.includes("authorization_code") ||
		editForm.value.grantTypes.includes("refresh_token")
	);
});

const isValid = computed(() => {
	const hasClientName = editForm.value.clientName.trim() !== "";
	const hasGrantTypes = editForm.value.grantTypes.length > 0;
	const hasRedirectUris = editForm.value.redirectUris.length > 0;

	// Redirect URIs only required for authorization_code grant
	const redirectUriValid = !requiresRedirectUri.value || hasRedirectUris;

	return hasClientName && hasGrantTypes && redirectUriValid;
});

const validationErrors = computed(() => {
	const errors: string[] = [];
	if (editForm.value.clientName.trim() === "") {
		errors.push("Client name is required");
	}
	if (editForm.value.grantTypes.length === 0) {
		errors.push("At least one grant type is required");
	}
	if (requiresRedirectUri.value && editForm.value.redirectUris.length === 0) {
		errors.push(
			"At least one redirect URI is required for authorization_code or refresh_token grants",
		);
	}
	return errors;
});

onMounted(async () => {
	await Promise.all([loadClient(), loadApplications()]);
});

async function loadClient() {
	loading.value = true;
	error.value = null;
	try {
		const id = route.params['id'] as string;
		client.value = await oauthClientsApi.get(id);
		resetEditForm();
	} catch (e) {
		error.value =
			e instanceof Error ? e.message : "Failed to load OAuth client";
	} finally {
		loading.value = false;
	}
}

async function loadApplications() {
	try {
		// Only load user-facing applications (not integrations)
		const response = await applicationsApi.listApplicationsOnly(true);
		applications.value = response.applications || [];
		console.log("Loaded applications:", applications.value);
	} catch (e: unknown) {
		console.error("Failed to load applications:", e);
		toast.warn("Warning", "Could not load applications: " + getErrorMessage(e, "Unknown error"));
	}
}

function resetEditForm() {
	if (client.value) {
		editForm.value = {
			clientName: client.value.clientName || "",
			redirectUris: [...(client.value.redirectUris || [])],
			postLogoutRedirectUris: [...(client.value.postLogoutRedirectUris || [])],
			allowedOrigins: [...(client.value.allowedOrigins || [])],
			grantTypes: [...(client.value.grantTypes || [])],
			defaultScopes: [...(client.value.defaultScopes || [])],
			pkceRequired: client.value.pkceRequired ?? true,
			applicationIds: [...(client.value.applicationIds || [])],
		};
	}
}

function startEditing() {
	resetEditForm();
	saveError.value = null;
	isEditing.value = true;
}

function cancelEditing() {
	resetEditForm();
	saveError.value = null;
	isEditing.value = false;
}

function addRedirectUri() {
	const uri = newRedirectUri.value.trim();
	if (uri && !editForm.value.redirectUris.includes(uri)) {
		try {
			new URL(uri);
			editForm.value.redirectUris.push(uri);
			newRedirectUri.value = "";
		} catch {
	}
	}
}

function removeRedirectUri(uri: string) {
	editForm.value.redirectUris = editForm.value.redirectUris.filter(
		(u) => u !== uri,
	);
}

function addPostLogoutRedirectUri() {
	const uri = newPostLogoutRedirectUri.value.trim();
	if (uri && !editForm.value.postLogoutRedirectUris.includes(uri)) {
		try {
			new URL(uri);
			editForm.value.postLogoutRedirectUris.push(uri);
			newPostLogoutRedirectUri.value = "";
		} catch {
		}
	}
}

function removePostLogoutRedirectUri(uri: string) {
	editForm.value.postLogoutRedirectUris =
		editForm.value.postLogoutRedirectUris.filter((u) => u !== uri);
}

function addAllowedOrigin() {
	const origin = newAllowedOrigin.value.trim();
	if (origin && !editForm.value.allowedOrigins.includes(origin)) {
		try {
			const url = new URL(origin);
			if (url.pathname !== "/" && url.pathname !== "") {
				toast.error("Invalid Origin", "Origin should not include a path (e.g., https://example.com)");
				return;
			}
			editForm.value.allowedOrigins.push(url.origin);
			newAllowedOrigin.value = "";
		} catch {
	}
	}
}

function removeAllowedOrigin(origin: string) {
	editForm.value.allowedOrigins = editForm.value.allowedOrigins.filter(
		(o) => o !== origin,
	);
}

async function saveChanges() {
	if (!client.value || !isValid.value) return;

	saving.value = true;
	saveError.value = null;

	try {
		await oauthClientsApi.update(
			client.value.id,
			{
				clientName: editForm.value.clientName.trim(),
				redirectUris: editForm.value.redirectUris,
				postLogoutRedirectUris: editForm.value.postLogoutRedirectUris,
				allowedOrigins: editForm.value.allowedOrigins,
				grantTypes: editForm.value.grantTypes,
				defaultScopes: editForm.value.defaultScopes,
				pkceRequired: editForm.value.pkceRequired,
				applicationIds: editForm.value.applicationIds,
			},
			// Handled inline below — don't also fire the global red banner.
			{ suppressGlobalErrorToast: true },
		);

		await loadClient();
		isEditing.value = false;
		toast.success("Success", "OAuth client updated successfully");
	} catch (e: unknown) {
		// Keep the form populated so the user can correct and retry.
		saveError.value = getErrorMessage(e, "Failed to update OAuth client");
	} finally {
		saving.value = false;
	}
}

async function rotateSecret() {
	if (!client.value) return;

	rotateLoading.value = true;

	try {
		const response = await oauthClientsApi.rotateSecret(client.value.id);
		newClientSecret.value = response.clientSecret;
		showRotateSecretDialog.value = false;
		showNewSecretDialog.value = true;
		toast.success("Success", "Client secret rotated successfully");
	} catch (e: unknown) {
	} finally {
		rotateLoading.value = false;
	}
}

function copySecret() {
	if (newClientSecret.value) {
		navigator.clipboard.writeText(newClientSecret.value);
		toast.success("Copied", "Client secret copied to clipboard");
	}
}

function copyClientId() {
	if (client.value) {
		navigator.clipboard.writeText(client.value.clientId);
		toast.success("Copied", "Client ID copied to clipboard");
	}
}

async function toggleActive() {
	if (!client.value) return;

	try {
		if (client.value.active) {
			await oauthClientsApi.deactivate(client.value.id);
			client.value.active = false;
			toast.success("Deactivated", "OAuth client has been deactivated");
		} else {
			await oauthClientsApi.activate(client.value.id);
			client.value.active = true;
			toast.success("Activated", "OAuth client has been activated");
		}
	} catch (e: unknown) {
	}
}

function formatDate(dateString: string) {
	return new Date(dateString).toLocaleString();
}

function getClientTypeSeverity(clientType: string) {
	return clientType === "PUBLIC" ? "info" : "warn";
}
</script>

<template>
  <div class="page-container">
    <header class="page-header">
      <div>
        <Button
          icon="pi pi-arrow-left"
          text
          class="back-button"
          @click="returnTo('/authentication/oauth-clients')"
        />
        <h1 class="page-title">{{ client?.clientName || 'OAuth Client Details' }}</h1>
        <p class="page-subtitle" v-if="client">
          <code class="client-id">{{ client.clientId }}</code>
          <Button
            icon="pi pi-copy"
            text
            size="small"
            v-tooltip="'Copy Client ID'"
            @click="copyClientId"
          />
        </p>
      </div>
      <div v-if="client && !isEditing" class="header-actions">
        <Button
          :label="client.active ? 'Deactivate' : 'Activate'"
          :icon="client.active ? 'pi pi-ban' : 'pi pi-check-circle'"
          :severity="client.active ? 'warn' : 'success'"
          text
          @click="toggleActive"
        />
        <Button label="Edit" icon="pi pi-pencil" @click="startEditing" />
      </div>
    </header>

    <div v-if="loading" class="loading-container">
      <ProgressSpinner strokeWidth="3" />
    </div>

    <Message v-else-if="error" severity="error" class="error-message">{{ error }}</Message>

    <template v-else-if="client">
      <div class="fc-card">
        <div class="card-header">
          <h2 class="card-title">Client Configuration</h2>
          <div v-if="!isEditing" class="status-badges">
            <Tag :value="client.clientType" :severity="getClientTypeSeverity(client.clientType)" />
            <Tag
              :value="client.active ? 'Active' : 'Inactive'"
              :severity="client.active ? 'success' : 'secondary'"
            />
          </div>
        </div>

        <div class="form-content">
          <!-- View Mode -->
          <template v-if="!isEditing">
            <div class="field-group">
              <label>Client Name</label>
              <span class="field-value">{{ client.clientName }}</span>
            </div>

            <div class="field-group">
              <label>Redirect URIs</label>
              <div class="uri-list">
                <Chip v-for="uri in client.redirectUris" :key="uri" :label="uri" />
              </div>
            </div>

            <div class="field-group">
              <label>Post-Logout Redirect URIs</label>
              <div
                v-if="
                  client.postLogoutRedirectUris &&
                  client.postLogoutRedirectUris.length > 0
                "
                class="uri-list"
              >
                <Chip
                  v-for="uri in client.postLogoutRedirectUris"
                  :key="uri"
                  :label="uri"
                />
              </div>
              <span v-else class="text-muted">No post-logout redirects configured</span>
            </div>

            <div class="field-group">
              <label>Allowed CORS Origins</label>
              <div
                v-if="client.allowedOrigins && client.allowedOrigins.length > 0"
                class="uri-list"
              >
                <Chip v-for="origin in client.allowedOrigins" :key="origin" :label="origin" />
              </div>
              <span v-else class="text-muted">No CORS origins configured</span>
            </div>

            <div class="field-group">
              <label>Grant Types</label>
              <div class="tag-list">
                <Tag
                  v-for="grant in client.grantTypes"
                  :key="grant"
                  :value="grant"
                  severity="secondary"
                />
              </div>
            </div>

            <div class="field-group">
              <label>Default Scopes</label>
              <div class="tag-list">
                <Tag
                  v-for="scope in client.defaultScopes"
                  :key="scope"
                  :value="scope"
                  severity="secondary"
                />
              </div>
            </div>

            <div class="field-group">
              <label>PKCE Required</label>
              <span class="field-value">
                <i
                  :class="
                    client.pkceRequired ? 'pi pi-check text-success' : 'pi pi-times text-muted'
                  "
                />
                {{ client.pkceRequired ? 'Yes' : 'No' }}
              </span>
            </div>

            <div class="field-group">
              <label>Associated Applications</label>
              <div v-if="(client.applicationIds?.length ?? 0) > 0" class="tag-list">
                <Tag
                  v-for="appId in client.applicationIds"
                  :key="appId"
                  :value="applications.find(a => a.id === appId)?.name || appId"
                  severity="info"
                />
              </div>
              <span v-else class="text-muted">No application restrictions</span>
            </div>

            <div class="field-group">
              <label>Created</label>
              <span class="field-value">{{ formatDate(client.createdAt) }}</span>
            </div>

            <div class="field-group">
              <label>Last Updated</label>
              <span class="field-value">{{ formatDate(client.updatedAt) }}</span>
            </div>

            <!-- Client Secret Section -->
            <div v-if="client.clientType === 'CONFIDENTIAL'" class="secret-section">
              <h3 class="section-title">Client Secret</h3>
              <p class="section-description">
                The client secret is encrypted and cannot be displayed. If you need a new secret,
                you can rotate it.
              </p>
              <Button
                label="Rotate Secret"
                icon="pi pi-refresh"
                severity="warn"
                @click="showRotateSecretDialog = true"
              />
            </div>
          </template>

          <!-- Edit Mode -->
          <template v-else>
            <div class="field">
              <label for="clientName">Client Name *</label>
              <InputText id="clientName" v-model="editForm.clientName" class="w-full" />
            </div>

            <div class="field">
              <label>Redirect URIs *</label>
              <div class="redirect-uri-input">
                <InputText
                  v-model="newRedirectUri"
                  placeholder="https://app.example.com/callback"
                  class="flex-grow"
                  @keyup.enter="addRedirectUri"
                />
                <Button
                  icon="pi pi-plus"
                  @click="addRedirectUri"
                  :disabled="!newRedirectUri.trim()"
                />
              </div>
              <div v-if="editForm.redirectUris.length > 0" class="uri-list">
                <Chip
                  v-for="uri in editForm.redirectUris"
                  :key="uri"
                  :label="uri"
                  removable
                  @remove="removeRedirectUri(uri)"
                />
              </div>
              <small class="field-help">Must use HTTPS (except localhost).</small>
            </div>

            <div class="field">
              <label>Post-Logout Redirect URIs</label>
              <div class="redirect-uri-input">
                <InputText
                  v-model="newPostLogoutRedirectUri"
                  placeholder="https://app.example.com/logged-out"
                  class="flex-grow"
                  @keyup.enter="addPostLogoutRedirectUri"
                />
                <Button
                  icon="pi pi-plus"
                  @click="addPostLogoutRedirectUri"
                  :disabled="!newPostLogoutRedirectUri.trim()"
                />
              </div>
              <div
                v-if="editForm.postLogoutRedirectUris.length > 0"
                class="uri-list"
              >
                <Chip
                  v-for="uri in editForm.postLogoutRedirectUris"
                  :key="uri"
                  :label="uri"
                  removable
                  @remove="removePostLogoutRedirectUri(uri)"
                />
              </div>
              <small class="field-help">
                OIDC RP-Initiated Logout. Required for session-end redirects — callers must also
                send id_token_hint.
              </small>
            </div>

            <div class="field">
              <label>Allowed CORS Origins</label>
              <div class="redirect-uri-input">
                <InputText
                  v-model="newAllowedOrigin"
                  placeholder="https://app.example.com"
                  class="flex-grow"
                  @keyup.enter="addAllowedOrigin"
                />
                <Button
                  icon="pi pi-plus"
                  @click="addAllowedOrigin"
                  :disabled="!newAllowedOrigin.trim()"
                />
              </div>
              <div v-if="editForm.allowedOrigins.length > 0" class="uri-list">
                <Chip
                  v-for="origin in editForm.allowedOrigins"
                  :key="origin"
                  :label="origin"
                  removable
                  @remove="removeAllowedOrigin(origin)"
                />
              </div>
              <small class="field-help"
                >Origins allowed to make browser requests to the token endpoint. Must use HTTPS
                (except localhost).</small
              >
            </div>

            <div class="field">
              <label for="grantTypes">Grant Types *</label>
              <MultiSelect
                id="grantTypes"
                v-model="editForm.grantTypes"
                :options="grantTypeOptions"
                optionLabel="label"
                optionValue="value"
                class="w-full"
              />
            </div>

            <div class="field">
              <label for="defaultScopes">Default Scopes</label>
              <MultiSelect
                id="defaultScopes"
                v-model="editForm.defaultScopes"
                :options="scopeOptions"
                optionLabel="label"
                optionValue="value"
                class="w-full"
              />
            </div>

            <div class="field checkbox-field">
              <Checkbox
                id="pkceRequired"
                v-model="editForm.pkceRequired"
                :binary="true"
                :disabled="client.clientType === 'PUBLIC'"
              />
              <label for="pkceRequired" class="checkbox-label">Require PKCE</label>
            </div>

            <div class="field">
              <label for="applications">Associated Applications</label>
              <MultiSelect
                id="applications"
                v-model="editForm.applicationIds"
                :options="applications"
                optionLabel="name"
                optionValue="id"
                placeholder="Select applications (optional)"
                class="w-full"
                filter
              />
              <small class="field-help">
                Only users with access to these applications can authenticate. Leave empty for no
                restrictions.
              </small>
            </div>

            <Message
              v-if="validationErrors.length > 0"
              severity="warn"
              :closable="false"
              class="validation-message"
            >
              <ul class="validation-list">
                <li v-for="err in validationErrors" :key="err">{{ err }}</li>
              </ul>
            </Message>

            <Message
              v-if="saveError"
              severity="error"
              :closable="false"
              class="validation-message"
            >
              {{ saveError }}
            </Message>

            <div class="form-actions">
              <Button label="Cancel" text @click="cancelEditing" :disabled="saving" />
              <Button
                label="Save Changes"
                icon="pi pi-check"
                @click="saveChanges"
                :loading="saving"
                :disabled="!isValid"
              />
            </div>
          </template>
        </div>
      </div>
    </template>

    <!-- Rotate Secret Confirmation Dialog -->
    <Dialog
      v-model:visible="showRotateSecretDialog"
      header="Rotate Client Secret"
      modal
      :style="{ width: '450px' }"
    >
      <div class="dialog-content">
        <Message severity="warn" :closable="false">
          This will invalidate the current secret. Any applications using the old secret will stop
          working.
        </Message>
        <p>Are you sure you want to rotate the client secret?</p>
      </div>

      <template #footer>
        <Button
          label="Cancel"
          text
          @click="showRotateSecretDialog = false"
          :disabled="rotateLoading"
        />
        <Button
          label="Rotate Secret"
          icon="pi pi-refresh"
          severity="warn"
          @click="rotateSecret"
          :loading="rotateLoading"
        />
      </template>
    </Dialog>

    <!-- New Secret Display Dialog -->
    <Dialog
      v-model:visible="showNewSecretDialog"
      header="New Client Secret"
      modal
      :closable="false"
      :style="{ width: '500px' }"
    >
      <div class="dialog-content">
        <Message severity="warn" :closable="false">
          Copy this secret now. It will not be shown again.
        </Message>

        <div class="secret-display">
          <code class="secret-code">{{ newClientSecret }}</code>
          <Button icon="pi pi-copy" text v-tooltip="'Copy to clipboard'" @click="copySecret" />
        </div>
      </div>

      <template #footer>
        <Button
          label="I've copied the secret"
          icon="pi pi-check"
          @click="showNewSecretDialog = false"
        />
      </template>
    </Dialog>
  </div>
</template>

<style scoped>
.back-button {
  margin-right: 8px;
}

.client-id {
  background: #f1f5f9;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 13px;
}

.header-actions {
  display: flex;
  gap: 8px;
}

.loading-container {
  display: flex;
  justify-content: center;
  padding: 60px;
}

.error-message {
  margin-bottom: 16px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
  padding-bottom: 16px;
  border-bottom: 1px solid #e2e8f0;
}

.card-title {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  color: #1e293b;
}

.status-badges {
  display: flex;
  gap: 8px;
}

.form-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
  max-width: 600px;
}

.field-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.field-group label {
  font-weight: 500;
  color: #64748b;
  font-size: 13px;
}

.field-value {
  color: #1e293b;
  font-size: 15px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.field label {
  font-weight: 500;
  color: #334155;
}

.field-help {
  color: #64748b;
  font-size: 12px;
}

.checkbox-field {
  flex-direction: row;
  align-items: center;
  gap: 8px;
}

.checkbox-label {
  margin: 0;
  cursor: pointer;
}

.uri-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.redirect-uri-input {
  display: flex;
  gap: 8px;
}

.flex-grow {
  flex: 1;
}

.secret-section {
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #e2e8f0;
}

.section-title {
  font-size: 16px;
  font-weight: 600;
  margin: 0 0 8px 0;
  color: #1e293b;
}

.section-description {
  color: #64748b;
  font-size: 14px;
  margin: 0 0 16px 0;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #e2e8f0;
}

.dialog-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.secret-display {
  display: flex;
  align-items: center;
  gap: 8px;
  background: #f8fafc;
  padding: 12px;
  border-radius: 6px;
  border: 1px solid #e2e8f0;
}

.secret-code {
  flex: 1;
  font-size: 13px;
  word-break: break-all;
  color: #1e293b;
}

.text-muted {
  color: #94a3b8;
}

.text-success {
  color: #22c55e;
}

.w-full {
  width: 100%;
}

.validation-message {
  margin-bottom: 0;
}

.validation-list {
  margin: 0;
  padding-left: 20px;
}

.validation-list li {
  margin: 2px 0;
}
</style>
