import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AdminOverviewPayload,
  type AdminRouteGroupItem,
  type AdminSpaceMemberPayload,
  type AdminSpacePayload,
  type AdminUserPayload,
  type AiTokenListItem,
  ApiError,
  type BackupPayload,
  type EmergencyAccessPayload,
  type NetworkEntryPayload,
  type SharePayload,
  type SpaceMemberRole,
  createAdminBackup,
  createAdminSpace,
  createAdminUser,
  createEmergencyAccess,
  disableAdminUser,
  enableAdminUser,
  getAdminOverview,
  listAdminAiTokens,
  listAdminBackups,
  listAdminShares,
  listAdminSpaces,
  listAdminUsers,
  listEmergencyAccess,
  listNetworkEntries,
  listSpaceMembers,
  putNetworkEntry,
  putSpaceMember,
  removeSpaceMember,
  restoreAdminBackup,
  revokeAdminAiToken,
  revokeAdminShare,
  revokeAdminUserSessions,
  revokeEmergencyAccess,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    const message = body?.error?.message?.trim();
    if (message) return `HTTP ${error.status}: ${message}`;
    if (body?.error?.code) return `HTTP ${error.status}: ${body.error.code}`;
    return `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : 'Unknown error';
}

function formatDate(value: string | undefined, locale: MemberLocale) {
  if (!value) return '--';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

function CountList({ title, counts }: { title: string; counts?: Record<string, number> }) {
  const entries = Object.entries(counts ?? {});
  if (entries.length === 0) return null;
  return (
    <div className="member-overview-group">
      <h2>{title}</h2>
      <ul className="member-overview-list">
        {entries.map(([key, value]) => (
          <li key={key}><span className="member-status-dot ok" />{key}<strong>{value}</strong></li>
        ))}
      </ul>
    </div>
  );
}

function emergencyStatus(record: EmergencyAccessPayload): 'active' | 'expired' | 'revoked' {
  if (record.revokedAt) return 'revoked';
  if (new Date(record.expiresAt) <= new Date()) return 'expired';
  return 'active';
}

// -- Overview -----------------------------------------------------------------------

export function AdminOverviewPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [overview, setOverview] = useState<AdminOverviewPayload | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setOverview(await getAdminOverview());
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const isEmpty = !overview;

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.overviewTitle}</h1><p>{text.overviewDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : isEmpty ? <div className="member-empty">{text.overviewEmpty}</div> : (
        <div className="member-overview-grid">
          {overview?.risks && overview.risks.length > 0 && (
            <div className="member-overview-group">
              <h2>{text.overviewRisks}</h2>
              <ul className="member-overview-list">
                {overview.risks.map((risk, index) => (
                  <li key={index}><span className="member-status-dot danger" />{risk}</li>
                ))}
              </ul>
            </div>
          )}
          <CountList title={text.overviewAccounts} counts={overview?.accounts} />
          <CountList title={text.overviewMountHealth} counts={overview?.mountsByHealth} />
          <CountList title={text.overviewJobs} counts={overview?.jobsByStatus} />
          <div className="member-overview-group">
            <h2>{text.overviewSpaces}</h2>
            <p className="member-overview-metric">{overview?.spaces ?? 0}</p>
          </div>
          <div className="member-overview-group">
            <h2>{text.overviewLatestBackup}</h2>
            {overview?.latestBackup ? (
              <p>{formatDate(overview.latestBackup.createdAt, locale)} · {overview.latestBackup.status}</p>
            ) : (
              <p className="member-readonly">{text.overviewNoBackup}</p>
            )}
          </div>
          {overview?.routeGroups && overview.routeGroups.length > 0 && (
            <div className="member-overview-group">
              <h2>{text.overviewRouteGroups}</h2>
              <ul className="member-overview-list">
                {overview.routeGroups.map((group: AdminRouteGroupItem) => (
                  <li key={group.id}><span className={`member-status-dot ${group.exposed ? 'ok' : 'muted'}`} />{group.label}<strong>{group.exposed ? text.routeExposed : text.routeClosed}</strong></li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// -- Users --------------------------------------------------------------------------

export function AdminUsersPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [users, setUsers] = useState<AdminUserPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState<{ email: string; displayName: string; password: string; role: 'member' | 'admin' }>({
    email: '', displayName: '', password: '', role: 'member',
  });
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminUsers();
      setUsers(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setCreating(true);
    setError('');
    try {
      await createAdminUser(form);
      setForm({ email: '', displayName: '', password: '', role: 'member' });
      setFormOpen(false);
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function onToggleStatus(user: AdminUserPayload) {
    setLoading(true);
    setError('');
    try {
      if (user.status === 'active') {
        await disableAdminUser(user.id);
      } else {
        await enableAdminUser(user.id);
      }
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onRevokeSessions(user: AdminUserPayload) {
    setLoading(true);
    setError('');
    try {
      await revokeAdminUserSessions(user.id);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.usersTitle}</h1><p>{text.usersDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
          <button className="member-primary" type="button" onClick={() => setFormOpen(true)}>{text.userCreate}</button>
        </div>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : users.length === 0 ? <div className="member-empty">{text.userNoUsers}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.userColumnEmail}</th><th>{text.userColumnStatus}</th><th>{text.userColumnTotp}</th><th>{text.userColumnAdmin}</th><th>{text.actions}</th></tr></thead>
          <tbody>{users.map((user) => (
            <tr key={user.id}>
              <td>{user.email}<small>{user.displayName}</small></td>
              <td>{user.status === 'active' ? text.userStatusActive : text.userStatusDisabled}</td>
              <td>{user.totpRequired ? text.accountTotpEnabled : text.accountTotpDisabledLabel}</td>
              <td>{user.role === 'admin' ? text.userStatusActive : '--'}</td>
              <td><div className="member-admin-table-actions">
                <button className="member-table-action" type="button" onClick={() => void onToggleStatus(user)} disabled={loading}>{user.status === 'active' ? text.userDisable : text.userEnable}</button>
                <button className="member-table-action member-table-danger" type="button" onClick={() => void onRevokeSessions(user)} disabled={loading}>{text.userRevokeSessions}</button>
              </div></td>
            </tr>
          ))}</tbody>
        </table>
      )}
      {formOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form" onSubmit={onSubmit}>
            <h2 className="member-admin-form-wide">{text.userCreate}</h2>
            <label>{text.userEmail}<input type="email" value={form.email} onChange={(event) => setForm({ ...form, email: event.target.value })} required /></label>
            <label>{text.userDisplayName}<input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} required /></label>
            <label>{text.userPassword}<input type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} autoComplete="new-password" required /></label>
            <label className="member-admin-checkbox"><input type="checkbox" checked={form.role === 'admin'} onChange={(event) => setForm({ ...form, role: event.target.checked ? 'admin' : 'member' })} />{text.userIsAdmin}</label>
            {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={() => setFormOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={creating}>{text.userSubmit}</button></div>
          </form>
        </div>
      )}
    </div>
  );
}

// -- Spaces and ACL -------------------------------------------------------------------

export function AdminSpacesPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [selectedSpaceId, setSelectedSpaceId] = useState('');
  const [members, setMembers] = useState<AdminSpaceMemberPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [spaceName, setSpaceName] = useState('');
  const [creating, setCreating] = useState(false);
  const [memberAccountId, setMemberAccountId] = useState('');
  const [memberPermission, setMemberPermission] = useState<SpaceMemberRole>('viewer');

  const loadSpaces = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminSpaces();
      setSpaces(response.items);
      setSelectedSpaceId((current) => (response.items.some((space) => space.id === current) ? current : (response.items[0]?.id ?? '')));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadMembers = useCallback(async (spaceId: string) => {
    if (!spaceId) {
      setMembers([]);
      return;
    }
    try {
      const response = await listSpaceMembers(spaceId);
      setMembers(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    void loadSpaces();
  }, [loadSpaces]);

  useEffect(() => {
    void loadMembers(selectedSpaceId);
  }, [loadMembers, selectedSpaceId]);

  async function onCreateSpace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!spaceName.trim()) return;
    setCreating(true);
    setError('');
    try {
      await createAdminSpace({ name: spaceName.trim() });
      setSpaceName('');
      await loadSpaces();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function onSetMember(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedSpaceId || !memberAccountId.trim()) return;
    setLoading(true);
    setError('');
    try {
      await putSpaceMember(selectedSpaceId, memberAccountId.trim(), memberPermission);
      setMemberAccountId('');
      await loadMembers(selectedSpaceId);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onUpdatePermission(accountId: string, permission: SpaceMemberRole) {
    setError('');
    try {
      await putSpaceMember(selectedSpaceId, accountId, permission);
      await loadMembers(selectedSpaceId);
    } catch (caught) {
      setError(describeError(caught));
    }
  }

  async function onRemoveMember(accountId: string) {
    setError('');
    try {
      await removeSpaceMember(selectedSpaceId, accountId);
      await loadMembers(selectedSpaceId);
    } catch (caught) {
      setError(describeError(caught));
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.spacesAdminTitle}</h1><p>{text.spacesAdminDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void loadSpaces()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}

      <form className="member-admin-inline-form" onSubmit={onCreateSpace}>
        <label>{text.spaceName}<input value={spaceName} onChange={(event) => setSpaceName(event.target.value)} required /></label>
        <button className="member-primary" type="submit" disabled={creating}>{text.spaceCreate}</button>
      </form>

      {spaces.length === 0 ? <div className="member-empty">{text.spaceNoSpacesAdmin}</div> : (
        <>
          <label className="member-admin-inline-form"><span>{text.spaceSelect}</span>
            <select value={selectedSpaceId} onChange={(event) => setSelectedSpaceId(event.target.value)}>
              {spaces.map((space) => <option key={space.id} value={space.id}>{space.name} ({space.type})</option>)}
            </select>
          </label>

          <h2>{text.spaceMembersTitle}</h2>
          {members.length === 0 ? <div className="member-empty">{text.spaceNoMembers}</div> : (
            <table className="member-admin-table">
              <thead><tr><th>{text.userColumnEmail}</th><th>{text.spaceMemberRole}</th><th>{text.actions}</th></tr></thead>
              <tbody>{members.map((member) => (
                <tr key={member.accountId}>
                  <td>{member.email ?? member.accountId}<small>{member.displayName}</small></td>
                  <td>
                    <select value={member.permission} onChange={(event) => void onUpdatePermission(member.accountId, event.target.value as SpaceMemberRole)}>
                      <option value="viewer">{text.spaceRoleViewer}</option>
                      <option value="editor">{text.spaceRoleEditor}</option>
                      <option value="manager">{text.spaceRoleManager}</option>
                    </select>
                  </td>
                  <td><button className="member-table-action member-table-danger" type="button" onClick={() => void onRemoveMember(member.accountId)}>{text.spaceMemberRemove}</button></td>
                </tr>
              ))}</tbody>
            </table>
          )}

          <form className="member-admin-inline-form" onSubmit={onSetMember}>
            <label>{text.spaceMemberAccountId}<input value={memberAccountId} onChange={(event) => setMemberAccountId(event.target.value)} required /></label>
            <label>{text.spaceMemberRole}
              <select value={memberPermission} onChange={(event) => setMemberPermission(event.target.value as SpaceMemberRole)}>
                <option value="viewer">{text.spaceRoleViewer}</option>
                <option value="editor">{text.spaceRoleEditor}</option>
                <option value="manager">{text.spaceRoleManager}</option>
              </select>
            </label>
            <button className="member-primary" type="submit" disabled={loading}>{text.spaceMemberAdd}</button>
          </form>
        </>
      )}
    </div>
  );
}

// -- Emergency access -------------------------------------------------------------------

export function AdminEmergencyPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [records, setRecords] = useState<EmergencyAccessPayload[]>([]);
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState({ spaceId: '', password: '', totpCode: '', reason: '' });
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [recordResponse, spaceResponse] = await Promise.all([listEmergencyAccess(), listAdminSpaces()]);
      setRecords(recordResponse.items ?? []);
      setSpaces(spaceResponse.items.filter((space) => space.type === 'personal'));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!form.spaceId || !form.reason.trim()) return;
    setCreating(true);
    setError('');
    try {
      await createEmergencyAccess({
        spaceId: form.spaceId,
        password: form.password,
        totpCode: form.totpCode,
        reason: form.reason.trim(),
      });
      setForm({ spaceId: '', password: '', totpCode: '', reason: '' });
      setFormOpen(false);
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function onRevoke(record: EmergencyAccessPayload) {
    setLoading(true);
    setError('');
    try {
      await revokeEmergencyAccess(record.id);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  function spaceLabel(spaceId: string) {
    return spaces.find((space) => space.id === spaceId)?.name ?? spaceId;
  }

  function statusLabel(record: EmergencyAccessPayload) {
    const status = emergencyStatus(record);
    if (status === 'active') return text.emergencyStatusActive;
    if (status === 'expired') return text.emergencyStatusExpired;
    return text.emergencyStatusRevoked;
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.emergencyTitle}</h1><p>{text.emergencyDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
          <button className="member-primary" type="button" onClick={() => setFormOpen(true)} disabled={spaces.length === 0}>{text.emergencyCreate}</button>
        </div>
      </div>
      <p className="member-admin-hint">{text.emergencyWarning}</p>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : records.length === 0 ? <div className="member-empty">{text.emergencyNoRecords}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.emergencyColumnTarget}</th><th>{text.emergencyColumnReason}</th><th>{text.emergencyColumnExpires}</th><th>{text.emergencyColumnStatus}</th><th>{text.actions}</th></tr></thead>
          <tbody>{records.map((record) => (
            <tr key={record.id}>
              <td>{spaceLabel(record.targetSpaceId)}</td>
              <td>{record.reason}</td>
              <td>{formatDate(record.expiresAt, locale)}</td>
              <td>{statusLabel(record)}</td>
              <td>{emergencyStatus(record) === 'active' && <button className="member-table-action member-table-danger" type="button" onClick={() => void onRevoke(record)} disabled={loading}>{text.emergencyRevoke}</button>}</td>
            </tr>
          ))}</tbody>
        </table>
      )}
      {formOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form" onSubmit={onSubmit}>
            <h2 className="member-admin-form-wide">{text.emergencyCreate}</h2>
            <label className="member-admin-form-wide">{text.emergencyTargetSpace}
              <select value={form.spaceId} onChange={(event) => setForm({ ...form, spaceId: event.target.value })} required>
                <option value="" disabled>{text.emergencyTargetSpace}</option>
                {spaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}
              </select>
            </label>
            <label>{text.emergencyPassword}<input type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} autoComplete="current-password" required /></label>
            <label>{text.emergencyTotp}<input value={form.totpCode} onChange={(event) => setForm({ ...form, totpCode: event.target.value })} inputMode="numeric" autoComplete="one-time-code" required /></label>
            <label className="member-admin-form-wide">{text.emergencyReason}<input value={form.reason} onChange={(event) => setForm({ ...form, reason: event.target.value })} required /></label>
            {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={() => setFormOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={creating}>{text.emergencySubmit}</button></div>
          </form>
        </div>
      )}
    </div>
  );
}

// -- Network entries -------------------------------------------------------------------

export function AdminNetworkPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [entries, setEntries] = useState<NetworkEntryPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [saving, setSaving] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listNetworkEntries();
      setEntries(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function patchEntry(name: NetworkEntryPayload['name'], patch: Partial<NetworkEntryPayload>) {
    setEntries((current) => current.map((entry) => (entry.name === name ? { ...entry, ...patch } : entry)));
  }

  async function onSave(entry: NetworkEntryPayload) {
    setSaving(entry.name);
    setError('');
    setNotice('');
    try {
      const updated = await putNetworkEntry(entry);
      setEntries((current) => current.map((item) => (item.name === updated.name ? updated : item)));
      if (updated.rebound) {
        setNotice(text.networkRebound);
      } else if (updated.restartRequired) {
        setNotice(updated.rebindError ? `${text.networkRestartRequired} (${updated.rebindError})` : text.networkRestartRequired);
      }
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setSaving('');
    }
  }

  const lan = entries.find((entry) => entry.name === 'lan_http');
  const proxy = entries.find((entry) => entry.name === 'proxy_https');

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.networkTitle}</h1><p>{text.networkDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <div className="member-readonly member-page-error">{notice}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : (
        <>
          {lan && (
            <form className="member-admin-form" onSubmit={(event) => { event.preventDefault(); void onSave(lan); }}>
              <h2 className="member-admin-form-wide">{text.networkLanHttp}</h2>
              <label className="member-admin-checkbox"><input type="checkbox" checked={lan.enabled} onChange={(event) => patchEntry('lan_http', { enabled: event.target.checked })} />{text.networkEnabled}</label>
              <label>{text.networkBindAddress}<input value={lan.bindAddr ?? ''} onChange={(event) => patchEntry('lan_http', { bindAddr: event.target.value })} /></label>
              <label className="member-admin-form-wide">{text.networkAllowedCidrs}<input value={(lan.cidrs ?? []).join(', ')} onChange={(event) => patchEntry('lan_http', { cidrs: event.target.value.split(',').map((value) => value.trim()).filter(Boolean) })} /></label>
              {lan.activeBindAddr && <p className="member-readonly member-admin-form-wide">{text.networkActiveBind}: {lan.activeBindAddr}</p>}
              {lan.enabled && <p className="member-readonly member-admin-form-wide">{text.networkUnencryptedWarning}</p>}
              <div className="member-admin-form-actions"><button className="member-primary" type="submit" disabled={saving === 'lan_http'}>{text.networkSave}</button></div>
            </form>
          )}
          {proxy && (
            <form className="member-admin-form" onSubmit={(event) => { event.preventDefault(); void onSave(proxy); }}>
              <h2 className="member-admin-form-wide">{text.networkProxyHttps}</h2>
              <label className="member-admin-checkbox"><input type="checkbox" checked={proxy.enabled} onChange={(event) => patchEntry('proxy_https', { enabled: event.target.checked })} />{text.networkEnabled}</label>
              <label>{text.networkExternalUrl}<input value={proxy.externalHttpsUrl ?? ''} onChange={(event) => patchEntry('proxy_https', { externalHttpsUrl: event.target.value })} /></label>
              <label className="member-admin-form-wide">{text.networkTrustedProxyCidrs}<input value={(proxy.cidrs ?? []).join(', ')} onChange={(event) => patchEntry('proxy_https', { cidrs: event.target.value.split(',').map((value) => value.trim()).filter(Boolean) })} /></label>
              <div className="member-admin-form-actions"><button className="member-primary" type="submit" disabled={saving === 'proxy_https'}>{text.networkSave}</button></div>
            </form>
          )}
        </>
      )}
    </div>
  );
}

// -- Share governance -------------------------------------------------------------------

export function AdminShareGovernancePanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [shares, setShares] = useState<SharePayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminShares();
      setShares(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onRevoke(share: SharePayload) {
    setLoading(true);
    setError('');
    try {
      await revokeAdminShare(share.id);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.shareGovTitle}</h1><p>{text.shareGovDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : shares.length === 0 ? <div className="member-empty">{text.shareGovNoShares}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.shareColumnTarget}</th><th>{text.shareGovColumnCreator}</th><th>{text.shareColumnStatus}</th><th>{text.shareColumnExpires}</th><th>{text.actions}</th></tr></thead>
          <tbody>{shares.map((share) => (
            <tr key={share.id}>
              <td>{share.relativePath}<small>{share.spaceId} · {share.mountId}</small></td>
              <td>{share.publicId}</td>
              <td>{share.status}</td>
              <td>{formatDate(share.expiresAt, locale)}</td>
              <td><button className="member-table-action member-table-danger" type="button" onClick={() => void onRevoke(share)} disabled={loading}>{text.shareRevoke}</button></td>
            </tr>
          ))}</tbody>
        </table>
      )}
    </div>
  );
}

// -- Token governance -------------------------------------------------------------------

export function AdminTokenGovernancePanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [tokens, setTokens] = useState<AiTokenListItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminAiTokens();
      setTokens(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onRevoke(token: AiTokenListItem) {
    setLoading(true);
    setError('');
    try {
      await revokeAdminAiToken(token.id);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.tokenGovTitle}</h1><p>{text.tokenGovDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : tokens.length === 0 ? <div className="member-empty">{text.tokenGovNoTokens}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.tokenColumnName}</th><th>{text.tokenGovColumnOwner}</th><th>{text.tokenColumnScopes}</th><th>{text.tokenColumnStatus}</th><th>{text.actions}</th></tr></thead>
          <tbody>{tokens.map((token) => (
            <tr key={token.id}>
              <td>{token.name}</td>
              <td>{token.accountId ?? '--'}</td>
              <td>{(token.scopes ?? []).join(', ') || '--'}</td>
              <td>{token.status ?? '--'}</td>
              <td><button className="member-table-action member-table-danger" type="button" onClick={() => void onRevoke(token)} disabled={loading}>{text.tokenRevoke}</button></td>
            </tr>
          ))}</tbody>
        </table>
      )}
    </div>
  );
}

// -- Backups ------------------------------------------------------------------------------

export function AdminBackupsPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const [backups, setBackups] = useState<BackupPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [creating, setCreating] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState<BackupPayload | null>(null);
  const [confirmPhrase, setConfirmPhrase] = useState('');
  const [restoring, setRestoring] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const response = await listAdminBackups();
      setBackups(response.items ?? []);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function onCreate() {
    setCreating(true);
    setError('');
    setNotice('');
    try {
      await createAdminBackup();
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setCreating(false);
    }
  }

  async function onRestore() {
    if (!restoreTarget) return;
    setRestoring(true);
    setError('');
    setNotice('');
    try {
      await restoreAdminBackup(restoreTarget.id, confirmPhrase.trim());
      setNotice(text.backupRestored);
      setRestoreTarget(null);
      setConfirmPhrase('');
      await load();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setRestoring(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.backupsTitle}</h1><p>{text.backupsDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
          <button className="member-primary" type="button" onClick={() => void onCreate()} disabled={creating}>{text.backupCreate}</button>
        </div>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <div className="member-readonly member-page-error">{notice}</div>}
      {loading ? <div className="member-loading">{text.loading}</div> : backups.length === 0 ? <div className="member-empty">{text.backupNoBackups}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.backupColumnCreated}</th><th>{text.backupColumnPath}</th><th>{text.backupColumnStatus}</th><th>{text.backupColumnNotes}</th><th>{text.actions}</th></tr></thead>
          <tbody>{backups.map((backup) => (
            <tr key={backup.id}>
              <td>{formatDate(backup.createdAt, locale)}</td>
              <td>{backup.path ?? '--'}</td>
              <td>{backup.status}</td>
              <td>{backup.notes ?? '--'}</td>
              <td>
                <button
                  className="member-table-action"
                  type="button"
                  disabled={backup.status !== 'completed' || !backup.path}
                  onClick={() => { setRestoreTarget(backup); setConfirmPhrase(''); }}
                >
                  {text.backupRestore}
                </button>
              </td>
            </tr>
          ))}</tbody>
        </table>
      )}
      {restoreTarget && (
        <div className="member-modal-backdrop">
          <form className="member-modal" onSubmit={(event) => { event.preventDefault(); void onRestore(); }}>
            <h2>{text.backupRestoreConfirmTitle}</h2>
            <p>{text.backupRestoreConfirmDetail}</p>
            <p className="member-readonly">{restoreTarget.path}</p>
            <label>{text.backupRestoreConfirmPhrase}<input value={confirmPhrase} onChange={(event) => setConfirmPhrase(event.target.value)} autoFocus /></label>
            <div className="member-modal-actions">
              <button className="member-secondary-action" type="button" onClick={() => setRestoreTarget(null)} disabled={restoring}>{text.cancel}</button>
              <button className="member-modal-danger" type="submit" disabled={restoring || confirmPhrase.trim() !== 'RESTORE'}>{text.backupRestoreSubmit}</button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
