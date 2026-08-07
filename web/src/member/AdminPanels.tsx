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
  type SharePayload,
  type SpaceMemberRole,
  isReauthenticationCanceled,
  createAdminBackup,
  createAdminSpace,
  createAdminUser,
  deleteAdminSpace,
  deleteAdminAiToken,
  disableAdminUser,
  enableAdminUser,
  getAdminOverview,
  listAdminAiTokens,
  listAdminBackups,
  listAdminShares,
  listAdminSpaces,
  listAdminUsers,
  listSpaceMembers,
  putSpaceMember,
  removeSpaceMember,
  renameAdminSpace,
  restoreAdminBackup,
  revokeAdminShare,
  revokeAdminUserSessions,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { useRecentReauth } from './RecentReauthProvider';
import { joinReadableLabels, readableLabel } from './displayLabels';

type LocaleText = (typeof localeMessages)[MemberLocale];

function describeError(error: unknown) {
  if (isReauthenticationCanceled(error)) return '';
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

function shareStatusLabel(status: string | undefined, text: LocaleText) {
  const labels: Record<string, string> = {
    active: text.shareStatusActive,
    expired: text.shareStatusExpired,
    revoked: text.shareStatusRevoked,
  };
  return status ? (labels[status] ?? status) : text.shareStatusActive;
}

function shareLocationLabel(share: SharePayload) {
  return joinReadableLabels([share.spaceName, share.mountName]);
}

function shareCreatorLabel(share: SharePayload, text: LocaleText) {
  const name = share.creatorDisplayName?.trim();
  const email = share.creatorEmail?.trim();
  return {
    primary: name || email || text.shareGovUnknownCreator,
    secondary: name && email && name !== email ? email : '',
  };
}

function tokenStatusLabel(status: string | undefined, text: LocaleText) {
  const labels: Record<string, string> = {
    active: text.tokenStatusActive,
    expired: text.tokenStatusExpired,
    revoked: text.tokenStatusRevoked,
  };
  return status ? (labels[status] ?? status) : '--';
}

function tokenOwnerLabel(token: AiTokenListItem) {
  const displayName = readableLabel(token.accountDisplayName);
  const email = readableLabel(token.accountEmail);
  return {
    primary: displayName || email || '--',
    secondary: displayName && email && displayName !== email ? email : '',
  };
}

function backupCreatorLabel(backup: BackupPayload) {
  const label = readableLabel(backup.createdByLabel);
  const displayName = readableLabel(backup.createdByDisplayName);
  const email = readableLabel(backup.createdByEmail);
  return {
    primary: label || displayName || email || '--',
    secondary: email && email !== label && email !== displayName ? email : '',
  };
}

function spaceMemberLabel(member: AdminSpaceMemberPayload) {
  const email = readableLabel(member.email);
  const displayName = readableLabel(member.displayName);
  return {
    primary: email || displayName || '--',
    secondary: email && displayName && displayName !== email ? displayName : '',
  };
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
                {overview.routeGroups
                  .filter((group: AdminRouteGroupItem) => group.id !== 'member_web' && group.id !== 'admin_web')
                  .map((group: AdminRouteGroupItem) => (
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
  const { runSensitive } = useRecentReauth();
  const [users, setUsers] = useState<AdminUserPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState<{ email: string; displayName: string; password: string; role: 'member' | 'admin' }>({
    email: '', displayName: '', password: '', role: 'member',
  });
  const [creating, setCreating] = useState(false);
  const [passwordError, setPasswordError] = useState('');

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

  function passwordMeetsRequirements(value: string) {
    if (value.length < 12) return false;
    const classes = [/[a-z]/.test(value), /[A-Z]/.test(value), /\d/.test(value), /[^a-zA-Z0-9]/.test(value)].filter(Boolean).length;
    return classes >= 3;
  }

  function openForm() {
    setError('');
    setPasswordError('');
    setFormOpen(true);
  }

  function closeForm() {
    setPasswordError('');
    setFormOpen(false);
  }

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPasswordError('');
    if (!passwordMeetsRequirements(form.password)) {
      setPasswordError(text.userPasswordWeak);
      return;
    }
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
        await runSensitive(() => disableAdminUser(user.id));
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
      await runSensitive(() => revokeAdminUserSessions(user.id));
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
          <button className="member-primary" type="button" onClick={openForm}>{text.userCreate}</button>
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
                {!(user.protected && user.status === 'active') && <button className="member-table-action" type="button" onClick={() => void onToggleStatus(user)} disabled={loading}>{user.status === 'active' ? text.userDisable : text.userEnable}</button>}
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
            <small className="member-path-hint member-admin-form-wide">{text.userPasswordHint}</small>
            {passwordError && <div className="member-error member-admin-form-wide">{text.error}: {passwordError}</div>}
            <label className="member-admin-checkbox"><input type="checkbox" checked={form.role === 'admin'} onChange={(event) => setForm({ ...form, role: event.target.checked ? 'admin' : 'member' })} />{text.userIsAdmin}</label>
            {error && <div className="member-error member-page-error member-admin-form-wide">{text.error}: {error}</div>}
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={closeForm}>{text.cancel}</button><button className="member-primary" type="submit" disabled={creating}>{text.userSubmit}</button></div>
          </form>
        </div>
      )}
    </div>
  );
}

// -- Spaces and ACL -------------------------------------------------------------------

export function AdminSpacesPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const { runSensitive } = useRecentReauth();
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [selectedSpaceId, setSelectedSpaceId] = useState('');
  const [members, setMembers] = useState<AdminSpaceMemberPayload[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [spaceName, setSpaceName] = useState('');
  const [creating, setCreating] = useState(false);
  const [memberAccountId, setMemberAccountId] = useState('');
  const [memberPermission, setMemberPermission] = useState<SpaceMemberRole>('viewer');
  const [renameTarget, setRenameTarget] = useState<AdminSpacePayload | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<AdminSpacePayload | null>(null);
  const [deleteConfirmValue, setDeleteConfirmValue] = useState('');
  const selectedSpace = spaces.find((space) => space.id === selectedSpaceId);

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

  function openRenameSpace(space: AdminSpacePayload) {
    setError('');
    setRenameTarget(space);
    setRenameValue(space.name);
  }

  function openDeleteSpace(space: AdminSpacePayload) {
    setError('');
    setDeleteTarget(space);
    setDeleteConfirmValue('');
  }

  async function onRenameSpace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!renameTarget || !renameValue.trim()) return;
    setLoading(true);
    setError('');
    try {
      await renameAdminSpace(renameTarget.id, renameValue.trim());
      setRenameTarget(null);
      setRenameValue('');
      await loadSpaces();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onDeleteSpace(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!deleteTarget || deleteConfirmValue !== deleteTarget.name) return;
    setLoading(true);
    setError('');
    try {
      await runSensitive(() => deleteAdminSpace(deleteTarget.id, deleteConfirmValue));
      setDeleteTarget(null);
      setDeleteConfirmValue('');
      await loadSpaces();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
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
          {selectedSpace?.type === 'shared' && (
            <div className="member-admin-table-actions">
              <button className="member-table-action" type="button" onClick={() => openRenameSpace(selectedSpace)} disabled={loading}>{text.spaceRename}</button>
              <button className="member-table-action member-table-danger" type="button" onClick={() => openDeleteSpace(selectedSpace)} disabled={loading}>{text.spaceDelete}</button>
            </div>
          )}

          <h2>{text.spaceMembersTitle}</h2>
          {members.length === 0 ? <div className="member-empty">{text.spaceNoMembers}</div> : (
            <table className="member-admin-table">
              <thead><tr><th>{text.userColumnEmail}</th><th>{text.spaceMemberRole}</th><th>{text.actions}</th></tr></thead>
              <tbody>{members.map((member) => {
                const account = spaceMemberLabel(member);
                return (
                  <tr key={member.accountId}>
                    <td>{account.primary}{account.secondary && <small>{account.secondary}</small>}</td>
                    <td>
                      {member.protected ? text.userColumnAdmin : <select value={member.permission} onChange={(event) => void onUpdatePermission(member.accountId, event.target.value as SpaceMemberRole)}>
                        <option value="viewer">{text.spaceRoleViewer}</option>
                        <option value="editor">{text.spaceRoleEditor}</option>
                        <option value="manager">{text.spaceRoleManager}</option>
                      </select>}
                    </td>
                    <td>{member.protected ? '--' : <button className="member-table-action member-table-danger" type="button" onClick={() => void onRemoveMember(member.accountId)}>{text.spaceMemberRemove}</button>}</td>
                  </tr>
                );
              })}</tbody>
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

          {renameTarget && (
            <div className="member-modal-backdrop">
              <form className="member-modal" onSubmit={onRenameSpace}>
                <h2>{text.spaceRenameTitle}</h2>
                <label>{text.spaceName}<input autoFocus value={renameValue} onChange={(event) => setRenameValue(event.target.value)} required /></label>
                {error && <div className="member-error">{text.error}: {error}</div>}
                <div className="member-modal-actions">
                  <button type="button" onClick={() => setRenameTarget(null)}>{text.cancel}</button>
                  <button className="member-primary" type="submit" disabled={loading || !renameValue.trim()}>{text.spaceRenameSave}</button>
                </div>
              </form>
            </div>
          )}

          {deleteTarget && (
            <div className="member-modal-backdrop">
              <form className="member-modal" onSubmit={onDeleteSpace}>
                <h2>{text.spaceDeleteTitle}</h2>
                <p className="member-modal-hint">{text.spaceDeleteDetail}</p>
                <p className="member-modal-hint"><strong>{deleteTarget.name}</strong></p>
                <label>{text.spaceDeleteConfirmLabel}<input autoFocus value={deleteConfirmValue} onChange={(event) => setDeleteConfirmValue(event.target.value)} required /></label>
                {error && <div className="member-error">{text.error}: {error}</div>}
                <div className="member-modal-actions">
                  <button type="button" onClick={() => setDeleteTarget(null)}>{text.cancel}</button>
                  <button className="member-modal-danger" type="submit" disabled={loading || deleteConfirmValue !== deleteTarget.name}>{text.spaceDeleteSubmit}</button>
                </div>
              </form>
            </div>
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
          <tbody>{shares.map((share) => {
            const creator = shareCreatorLabel(share, text);
            const location = shareLocationLabel(share);
            return (
              <tr key={share.id}>
                <td>{share.relativePath && share.relativePath !== '.' ? share.relativePath : text.shareBrowseRoot}{location && <small>{location}</small>}</td>
                <td>{creator.primary}{creator.secondary && <small>{creator.secondary}</small>}</td>
                <td>{shareStatusLabel(share.status, text)}</td>
                <td>{formatDate(share.expiresAt, locale)}</td>
                <td><button className="member-table-action member-table-danger" type="button" onClick={() => void onRevoke(share)} disabled={loading}>{text.shareRevoke}</button></td>
              </tr>
            );
          })}</tbody>
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

  async function onDelete(token: AiTokenListItem) {
    setLoading(true);
    setError('');
    try {
      await deleteAdminAiToken(token.id);
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
          <thead><tr><th>{text.tokenColumnName}</th><th>{text.tokenGovColumnOwner}</th><th>{text.tokenColumnScopes}</th><th>{text.tokenColumnExpires}</th><th>{text.tokenColumnLastUsed}</th><th>{text.tokenColumnStatus}</th><th>{text.actions}</th></tr></thead>
          <tbody>{tokens.map((token) => {
            const owner = tokenOwnerLabel(token);
            return (
              <tr key={token.id}>
                <td>{token.name}</td>
                <td>{owner.primary}{owner.secondary && <small>{owner.secondary}</small>}</td>
                <td>{(token.scopes ?? []).join(', ') || '--'}</td>
                <td>{token.expiresAt ? formatDate(token.expiresAt, locale) : text.tokenNeverExpires}</td>
                <td>{token.lastUsedAt ? formatDate(token.lastUsedAt, locale) : '--'}</td>
                <td>{tokenStatusLabel(token.status, text)}</td>
                <td><button className="member-table-action member-table-danger" type="button" onClick={() => void onDelete(token)} disabled={loading}>{text.tokenRevoke}</button></td>
              </tr>
            );
          })}</tbody>
        </table>
      )}
    </div>
  );
}

// -- Backups ------------------------------------------------------------------------------

export function AdminBackupsPanel({ locale }: { locale: MemberLocale }) {
  const text = localeMessages[locale];
  const { runSensitive } = useRecentReauth();
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
      await runSensitive(() => createAdminBackup());
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
      const accepted = await runSensitive(() => restoreAdminBackup(restoreTarget.id, confirmPhrase.trim()));
      setNotice(accepted.requestId ? `${text.backupRestored} · ${accepted.state ?? 'preparing'} · ${accepted.requestId}` : text.backupRestored);
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
          <thead><tr><th>{text.backupColumnCreated}</th><th>{text.backupColumnCreator}</th><th>{text.backupColumnPath}</th><th>{text.backupColumnStatus}</th><th>{text.backupColumnNotes}</th><th>{text.actions}</th></tr></thead>
          <tbody>{backups.map((backup) => {
            const creator = backupCreatorLabel(backup);
            return (
              <tr key={backup.id}>
                <td>{formatDate(backup.createdAt, locale)}</td>
                <td>{creator.primary}{creator.secondary && <small>{creator.secondary}</small>}</td>
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
            );
          })}</tbody>
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
