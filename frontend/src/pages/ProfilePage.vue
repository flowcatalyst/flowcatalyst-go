<script setup lang="ts">
import { useAuthStore } from "@/stores/auth";
import PasskeysSection from "@/components/PasskeysSection.vue";
import TwoFactorSection from "@/components/TwoFactorSection.vue";

const authStore = useAuthStore();
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
            <Button label="Change Password" icon="pi pi-key" outlined />
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
</style>
