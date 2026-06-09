<script setup lang="ts">
import { toast } from "@/utils/errorBus";
import { ref, computed, onMounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useConfirm } from "primevue/useconfirm";
import { clientsApi, type Client, type ClientApplication } from "@/api/clients";
import { useReturnTo } from "@/composables/useReturnTo";
import { getErrorMessage } from "@/utils/errors";

const route = useRoute();
const router = useRouter();
const { returnTo } = useReturnTo();
const confirm = useConfirm();

const loading = ref(true);
const client = ref<Client | null>(null);
const editing = ref(false);
const saving = ref(false);

// Edit form
const editName = ref("");

// Applications
const applications = ref<[ClientApplication[], ClientApplication[]]>([[], []]);
const loadingApps = ref(false);
const savingApps = ref(false);

const availableApps = computed(() => applications.value[0]);
const enabledApps = computed(() => applications.value[1]);

onMounted(async () => {
	const id = route.params['id'] as string;
	if (id) {
		await Promise.all([loadClient(id), loadApplications(id)]);
	}
});

async function loadClient(id: string) {
	loading.value = true;
	try {
		client.value = await clientsApi.get(id);
	} catch {
		client.value = null;
	} finally {
		loading.value = false;
	}
}

async function loadApplications(clientId: string) {
	loadingApps.value = true;
	try {
		const response = await clientsApi.getApplications(clientId);
		const available: ClientApplication[] = [];
		const enabled: ClientApplication[] = [];

		for (const app of response.applications) {
			if (app.enabledForClient) {
				enabled.push(app);
			} else {
				available.push(app);
			}
		}

		applications.value = [available, enabled];
	} catch (error) {
		console.error("Failed to load applications:", error);
	} finally {
		loadingApps.value = false;
	}
}

async function saveApplications() {
	if (!client.value) return;

	savingApps.value = true;
	try {
		const enabledIds = applications.value[1].map((app) => app.id);
		await clientsApi.updateApplications(client.value.id, enabledIds);
		toast.success("Success", "Applications updated");
	} catch {
	} finally {
		savingApps.value = false;
	}
}

function startEditing() {
	if (client.value) {
		editName.value = client.value.name;
		editing.value = true;
	}
}

function cancelEditing() {
	editing.value = false;
}

async function saveChanges() {
	if (!client.value) return;

	saving.value = true;
	const id = client.value.id;
	try {
		await clientsApi.update(id, {
			name: editName.value,
		});
		await loadClient(id);
		editing.value = false;
		toast.success("Success", "Client updated");
	} catch {
	} finally {
		saving.value = false;
	}
}

function confirmActivate() {
	confirm.require({
		message: "Activate this client?",
		header: "Activate Client",
		icon: "pi pi-check-circle",
		acceptLabel: "Activate",
		accept: activateClient,
	});
}

async function activateClient() {
	if (!client.value) return;
	try {
		await clientsApi.activate(client.value.id);
		client.value = await clientsApi.get(client.value.id);
		toast.success("Success", "Client activated");
	} catch {
	}
}

function confirmSuspend() {
	confirm.require({
		message: "Suspend this client? Users will not be able to access it.",
		header: "Suspend Client",
		icon: "pi pi-exclamation-triangle",
		acceptLabel: "Suspend",
		acceptClass: "p-button-warning",
		accept: () => suspendClient("Manual suspension"),
	});
}

async function suspendClient(reason: string) {
	if (!client.value) return;
	try {
		await clientsApi.suspend(client.value.id, reason);
		client.value = await clientsApi.get(client.value.id);
		toast.success("Success", "Client suspended");
	} catch {
	}
}

function confirmDeactivate() {
	confirm.require({
		message: "Deactivate this client? This is a soft delete.",
		header: "Deactivate Client",
		icon: "pi pi-exclamation-triangle",
		acceptLabel: "Deactivate",
		acceptClass: "p-button-danger",
		accept: () => deactivateClient("Manual deactivation"),
	});
}

async function deactivateClient(reason: string) {
	if (!client.value) return;
	try {
		await clientsApi.deactivate(client.value.id, reason);
		toast.success("Success", "Client deactivated");
		// The client is now soft-deleted — re-fetching its detail would 404, so
		// return to the listing rather than staying on a dead detail page.
		router.push("/clients");
	} catch (e) {
		toast.error("Error", getErrorMessage(e, "Failed to deactivate client"));
	}
}

function getStatusSeverity(status: string) {
	switch (status) {
		case "ACTIVE":
			return "success";
		case "SUSPENDED":
			return "warn";
		case "INACTIVE":
			return "secondary";
		default:
			return "secondary";
	}
}

function formatDate(dateString: string) {
	return new Date(dateString).toLocaleString();
}
</script>

<template>
  <div class="page-container">
    <div v-if="loading" class="loading-container">
      <ProgressSpinner strokeWidth="3" />
    </div>

    <template v-else-if="client">
      <!-- Header -->
      <header class="page-header">
        <div class="header-content">
          <Button
            icon="pi pi-arrow-left"
            text
            severity="secondary"
            @click="returnTo('/clients')"
            v-tooltip="'Back to list'"
          />
          <div class="header-text">
            <h1 class="page-title">{{ client.name }}</h1>
            <code class="client-code">{{ client.identifier }}</code>
          </div>
          <Tag :value="client.status" :severity="getStatusSeverity(client.status)" />
        </div>
      </header>

      <!-- Details Card -->
      <div class="section-card">
        <div class="card-header">
          <h3>Client Details</h3>
          <Button v-if="!editing" icon="pi pi-pencil" label="Edit" text @click="startEditing" />
        </div>
        <div class="card-content">
          <template v-if="editing">
            <div class="form-field">
              <label>Name</label>
              <InputText v-model="editName" class="full-width" />
            </div>
            <div class="form-actions">
              <Button label="Cancel" severity="secondary" outlined @click="cancelEditing" />
              <Button label="Save" :loading="saving" @click="saveChanges" />
            </div>
          </template>

          <template v-else>
            <div class="detail-grid">
              <div class="detail-item">
                <label>Identifier</label>
                <code>{{ client.identifier }}</code>
              </div>
              <div class="detail-item">
                <label>Name</label>
                <span>{{ client.name }}</span>
              </div>
              <div class="detail-item">
                <label>Status</label>
                <Tag :value="client.status" :severity="getStatusSeverity(client.status)" />
              </div>
              <div class="detail-item" v-if="client.statusReason">
                <label>Status Reason</label>
                <span>{{ client.statusReason }}</span>
              </div>
              <div class="detail-item">
                <label>Created</label>
                <span>{{ formatDate(client.createdAt) }}</span>
              </div>
              <div class="detail-item">
                <label>Updated</label>
                <span>{{ formatDate(client.updatedAt) }}</span>
              </div>
            </div>
          </template>
        </div>
      </div>

      <!-- Applications Card -->
      <div class="section-card">
        <div class="card-header">
          <h3>Applications</h3>
          <Button
            label="Save"
            icon="pi pi-save"
            :loading="savingApps"
            :disabled="savingApps"
            @click="saveApplications"
          />
        </div>
        <div class="card-content">
          <div v-if="loadingApps" class="loading-apps">
            <ProgressSpinner strokeWidth="3" style="width: 30px; height: 30px" />
            <span>Loading applications...</span>
          </div>
          <PickList
            v-else
            v-model="applications"
            dataKey="id"
            :pt="{
              list: { style: { height: '300px' } },
            }"
          >
            <template #sourceheader>
              <span class="picklist-header">Available ({{ availableApps.length }})</span>
            </template>
            <template #targetheader>
              <span class="picklist-header">Enabled ({{ enabledApps.length }})</span>
            </template>
            <template #item="{ item }">
              <div class="app-item">
                <div class="app-info">
                  <span class="app-name">{{ item.name }}</span>
                  <span class="app-code">{{ item.code }}</span>
                </div>
                <Tag v-if="!item.active" value="Inactive" severity="secondary" class="app-status" />
              </div>
            </template>
          </PickList>
          <p class="help-text">
            Move applications between lists to enable or disable them for this client. Click Save to
            apply changes.
          </p>
        </div>
      </div>

      <!-- Actions Card -->
      <div class="section-card">
        <div class="card-header">
          <h3>Actions</h3>
        </div>
        <div class="card-content">
          <div class="action-items">
            <div v-if="client.status !== 'ACTIVE'" class="action-item">
              <div class="action-info">
                <strong>Activate Client</strong>
                <p>Make this client active and accessible.</p>
              </div>
              <Button label="Activate" severity="success" outlined @click="confirmActivate" />
            </div>

            <div v-if="client.status === 'ACTIVE'" class="action-item">
              <div class="action-info">
                <strong>Suspend Client</strong>
                <p>Temporarily disable access to this client.</p>
              </div>
              <Button label="Suspend" severity="warn" outlined @click="confirmSuspend" />
            </div>

            <div v-if="client.status !== 'INACTIVE'" class="action-item">
              <div class="action-info">
                <strong>Deactivate Client</strong>
                <p>Soft delete this client. Can be reactivated later.</p>
              </div>
              <Button label="Deactivate" severity="danger" outlined @click="confirmDeactivate" />
            </div>
          </div>
        </div>
      </div>
    </template>

    <Message v-else severity="error">Client not found</Message>
  </div>
</template>

<style scoped>
.page-container {
  max-width: 900px;
}

.loading-container {
  display: flex;
  justify-content: center;
  padding: 60px;
}

.header-content {
  display: flex;
  align-items: flex-start;
  gap: 16px;
}

.header-text {
  flex: 1;
}

.client-code {
  display: inline-block;
  margin-top: 4px;
  background: #f1f5f9;
  padding: 4px 10px;
  border-radius: 4px;
  font-size: 14px;
  color: #475569;
}

.section-card {
  margin-bottom: 24px;
  background: white;
  border-radius: 8px;
  border: 1px solid #e2e8f0;
  overflow: hidden;
}

.card-content {
  padding: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 20px;
  border-bottom: 1px solid #e2e8f0;
}

.card-header h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 20px;
}

.detail-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.detail-item.full-width {
  grid-column: 1 / -1;
}

.detail-item label {
  font-size: 12px;
  font-weight: 500;
  color: #64748b;
  text-transform: uppercase;
}

.form-field {
  margin-bottom: 20px;
}

.form-field label {
  display: block;
  margin-bottom: 6px;
  font-weight: 500;
}

.full-width {
  width: 100%;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  padding-top: 16px;
  border-top: 1px solid #e2e8f0;
}

.action-items {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.action-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  background: #fafafa;
  border-radius: 8px;
  border: 1px solid #e5e7eb;
}

.action-info strong {
  display: block;
  margin-bottom: 4px;
}

.action-info p {
  margin: 0;
  font-size: 13px;
  color: #64748b;
}

.loading-apps {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 40px;
  color: #64748b;
}

.picklist-header {
  font-weight: 600;
  font-size: 14px;
}

.app-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 0;
}

.app-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.app-name {
  font-weight: 500;
}

.app-code {
  font-size: 12px;
  color: #64748b;
  font-family: monospace;
}

.app-status {
  font-size: 11px;
}

.help-text {
  margin-top: 12px;
  font-size: 13px;
  color: #64748b;
}

@media (max-width: 640px) {
  .detail-grid {
    grid-template-columns: 1fr;
  }
}
</style>
