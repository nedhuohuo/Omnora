import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import {
  type AdminMountDetail,
  type AdminMountEligibleAccount,
  type AdminMountGrant,
  type AdminMountListItem,
  type AdminSpacePayload,
  type HostDirectoryEntry,
  type SpaceMemberRole,
  type UpdateAdminMountPayload,
  ApiError,
  deleteAdminMount,
  deleteAdminMountGrant,
  getAdminMount,
  listAdminHostDirectories,
  listAdminMounts,
  listAdminSpaces,
  putAdminMountGrant,
  registerAdminMount,
  reverifyAdminMount,
  updateAdminMount,
} from '../../api';
import { readableLabel, spaceDisplayName } from '../../member/displayLabels';
import { type MemberLocale, localeMessages } from '../../member/i18n';
import './storage-locations.css';

type LocaleText = (typeof localeMessages)[MemberLocale];
type DetailTab = 'access' | 'settings';
type MountForm = {
  spaceId: string;
  displayName: string;
  rootPath: string;
  mode: AdminMountDetail['mode'];
  indexEnabled: boolean;
};
type SettingsForm = {
  displayName: string;
  mode: AdminMountDetail['mode'];
  indexEnabled: boolean;
  allowPublicShares: boolean;
};

const emptyMountForm: MountForm = {
  spaceId: '',
  displayName: '',
  rootPath: '',
  mode: 'read_write',
  indexEnabled: true,
};

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

function memberLabel(member: AdminMountEligibleAccount | AdminMountGrant) {
  const displayName = readableLabel(member.displayName);
  const email = readableLabel(member.email);
  return {
    primary: displayName || email || member.accountId,
    secondary: displayName && email && displayName !== email ? email : '',
  };
}

function defaultMountRoot(roots: string[]) {
  return roots.find((root) => root.includes('/mnt/')) ?? roots[0] ?? '';
}

function mountModeLabel(value: string, text: LocaleText) {
  return value === 'read_write' || value === 'read-write' ? text.readWrite : text.readOnly;
}

function mountIndexLabel(value: string, text: LocaleText) {
  return value === 'indexed' ? text.indexReady : text.indexDisabled;
}

function mountStatusLabel(value: string, text: LocaleText) {
  return value === 'active' ? text.statusActive : value === 'unavailable' ? text.statusUnavailable : value;
}

function permissionLabel(value: SpaceMemberRole, text: LocaleText) {
  if (value === 'manager') return text.spaceRoleManager;
  if (value === 'editor') return text.spaceRoleEditor;
  return text.spaceRoleViewer;
}

export type AdminStorageLocationsPanelProps = {
  locale: MemberLocale;
  mountId?: string;
  onMountChange?: (mountId: string) => void;
};

export default function AdminStorageLocationsPanel({ locale, mountId, onMountChange }: AdminStorageLocationsPanelProps) {
  const text = localeMessages[locale];
  const [spaces, setSpaces] = useState<AdminSpacePayload[]>([]);
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [selectedMountId, setSelectedMountId] = useState(mountId ?? '');
  const [detail, setDetail] = useState<AdminMountDetail | null>(null);
  const [grants, setGrants] = useState<AdminMountGrant[]>([]);
  const [eligibleAccounts, setEligibleAccounts] = useState<AdminMountEligibleAccount[]>([]);
  const [activeTab, setActiveTab] = useState<DetailTab>('access');
  const [memberSearch, setMemberSearch] = useState('');
  const [newPermission, setNewPermission] = useState<SpaceMemberRole>('viewer');
  const [settings, setSettings] = useState<SettingsForm>({ displayName: '', mode: 'read_write', indexEnabled: true, allowPublicShares: false });
  const [loadingList, setLoadingList] = useState(false);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [pendingAction, setPendingAction] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [mountForm, setMountForm] = useState<MountForm>(emptyMountForm);
  const [allowedRoots, setAllowedRoots] = useState<string[]>([]);
  const [pathSuggestions, setPathSuggestions] = useState<HostDirectoryEntry[]>([]);
  const [showPathSuggestions, setShowPathSuggestions] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteData, setDeleteData] = useState(false);
  const detailRequest = useRef(0);
  const suggestionRequest = useRef(0);

  const loadList = useCallback(async (preferredMountId?: string) => {
    setLoadingList(true);
    setError('');
    try {
      const [spaceResponse, mountResponse, hostDirectories] = await Promise.all([
        listAdminSpaces(),
        listAdminMounts(),
        listAdminHostDirectories('/'),
      ]);
      const roots = hostDirectories.roots ?? [];
      setSpaces(spaceResponse.items ?? []);
      setMounts(mountResponse.items ?? []);
      setAllowedRoots(roots);
      setMountForm((current) => ({
        ...current,
        spaceId: spaceResponse.items.some((space) => space.id === current.spaceId) ? current.spaceId : (spaceResponse.items[0]?.id ?? ''),
        rootPath: current.rootPath || defaultMountRoot(roots),
      }));
      setSelectedMountId((current) => {
        const preferred = preferredMountId && mountResponse.items.some((mount) => mount.id === preferredMountId) ? preferredMountId : current;
        return mountResponse.items.some((mount) => mount.id === preferred) ? preferred : (mountResponse.items[0]?.id ?? '');
      });
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoadingList(false);
    }
  }, []);

  const loadDetail = useCallback(async (mountId: string) => {
    const requestId = ++detailRequest.current;
    if (!mountId) {
      setDetail(null);
      setGrants([]);
      setEligibleAccounts([]);
      return;
    }
    setLoadingDetail(true);
    setError('');
    try {
      const nextDetail = await getAdminMount(mountId);
      if (requestId !== detailRequest.current) return;
      setDetail(nextDetail);
      setGrants(nextDetail.grants ?? []);
      setEligibleAccounts(nextDetail.eligibleAccounts ?? []);
      setSettings({
        displayName: nextDetail.name,
        mode: nextDetail.mode,
        indexEnabled: nextDetail.indexEnabled,
        allowPublicShares: nextDetail.allowPublicShares,
      });
    } catch (caught) {
      if (requestId === detailRequest.current) setError(describeError(caught));
    } finally {
      if (requestId === detailRequest.current) setLoadingDetail(false);
    }
  }, []);

  useEffect(() => {
    void loadList(mountId);
  }, [loadList]);

  useEffect(() => {
    if (mountId !== undefined) setSelectedMountId(mountId);
  }, [mountId]);

  useEffect(() => {
    setMemberSearch('');
    setNotice('');
    void loadDetail(selectedMountId);
  }, [loadDetail, selectedMountId]);

  useEffect(() => {
    if (!createOpen) return;
    const requestId = ++suggestionRequest.current;
    const timer = window.setTimeout(() => {
      void listAdminHostDirectories(mountForm.rootPath || '/')
        .then((response) => {
          if (requestId !== suggestionRequest.current) return;
          setAllowedRoots(response.roots ?? []);
          setPathSuggestions(response.entries ?? []);
        })
        .catch(() => {
          if (requestId === suggestionRequest.current) setPathSuggestions([]);
        });
    }, 180);
    return () => window.clearTimeout(timer);
  }, [createOpen, mountForm.rootPath]);

  const grantedAccountIds = new Set(grants.map((grant) => grant.accountId));
  const normalizedSearch = memberSearch.trim().toLocaleLowerCase(locale);
  const eligibleMembers = eligibleAccounts.filter((member) => {
    if (grantedAccountIds.has(member.accountId)) return false;
    if (!normalizedSearch) return true;
    return [member.displayName, member.email, member.accountId]
      .some((value) => value?.toLocaleLowerCase(locale).includes(normalizedSearch));
  });
  const settingsDirty = Boolean(detail && (
    settings.displayName.trim() !== detail.name
    || settings.mode !== detail.mode
    || settings.indexEnabled !== detail.indexEnabled
    || settings.allowPublicShares !== detail.allowPublicShares
  ));

  async function onCreateMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPendingAction('create');
    setError('');
    setNotice('');
    try {
      await registerAdminMount(mountForm);
      setCreateOpen(false);
      setMountForm((current) => ({ ...emptyMountForm, spaceId: current.spaceId, rootPath: defaultMountRoot(allowedRoots) }));
      setNotice(text.adminOperationComplete);
      await loadList();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onSaveSettings(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!detail || !settings.displayName.trim() || !settingsDirty) return;
    if (detail.allowPublicShares && !settings.allowPublicShares && !window.confirm(text.storageDisablePublicSharesWarning)) return;
    const payload: UpdateAdminMountPayload = {};
    const displayName = settings.displayName.trim();
    if (displayName !== detail.name) payload.displayName = displayName;
    if (settings.mode !== detail.mode) payload.mode = settings.mode;
    if (settings.indexEnabled !== detail.indexEnabled) payload.indexEnabled = settings.indexEnabled;
    if (settings.allowPublicShares !== detail.allowPublicShares) payload.allowPublicShares = settings.allowPublicShares;
    setPendingAction('settings');
    setError('');
    setNotice('');
    try {
      await updateAdminMount(detail.id, payload);
      setNotice(text.storageSettingsSaved);
      await Promise.all([loadDetail(detail.id), loadList(detail.id)]);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onAddGrant(accountId: string) {
    if (!detail) return;
    setPendingAction(`add:${accountId}`);
    setError('');
    setNotice('');
    try {
      await putAdminMountGrant(detail.id, accountId, newPermission);
      setNotice(text.storageGrantAdded);
      await loadDetail(detail.id);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onUpdateGrant(grant: AdminMountGrant, permission: SpaceMemberRole) {
    if (!detail || grant.permission === permission) return;
    setPendingAction(`grant:${grant.accountId}`);
    setError('');
    setNotice('');
    try {
      await putAdminMountGrant(detail.id, grant.accountId, permission);
      setNotice(text.storageGrantUpdated);
      await loadDetail(detail.id);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onDeleteGrant(grant: AdminMountGrant) {
    if (!detail) return;
    setPendingAction(`grant:${grant.accountId}`);
    setError('');
    setNotice('');
    try {
      await deleteAdminMountGrant(detail.id, grant.accountId);
      setNotice(text.storageGrantRemoved);
      await loadDetail(detail.id);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onReverify() {
    if (!detail) return;
    setPendingAction('reverify');
    setError('');
    setNotice('');
    try {
      await reverifyAdminMount(detail.id);
      setNotice(text.adminOperationComplete);
      await Promise.all([loadDetail(detail.id), loadList(detail.id)]);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  async function onDeleteMount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!detail) return;
    setPendingAction('delete');
    setError('');
    setNotice('');
    try {
      await deleteAdminMount(detail.id, deleteData);
      setDeleteOpen(false);
      setDeleteData(false);
      onMountChange?.('');
      setNotice(text.adminOperationComplete);
      await loadList();
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setPendingAction('');
    }
  }

  return (
    <div className="member-admin-workspace storage-admin">
      <div className="member-heading storage-heading">
        <div><h1>{text.storageLocationsTitle}</h1><p>{text.storageLocationsDetail}</p></div>
        <div className="member-admin-table-actions">
          <button className="member-secondary-action" type="button" onClick={() => void loadList(selectedMountId)} disabled={loadingList}>{text.refresh}</button>
          <button className="member-primary" type="button" onClick={() => setCreateOpen(true)} disabled={spaces.length === 0}>{text.storageLocationCreate}</button>
        </div>
      </div>

      {error && <div className="member-error member-page-error storage-feedback">{text.error}: {error}</div>}
      {notice && <p className="member-admin-notice storage-feedback">{notice}</p>}

      <div className="storage-layout">
        <aside className="storage-list" aria-label={text.storageLocationsTitle}>
          <div className="storage-list-header"><strong>{text.storageLocationList}</strong><span>{mounts.length}</span></div>
          {loadingList && mounts.length === 0 ? <div className="storage-list-state">{text.loading}</div> : mounts.length === 0 ? <div className="storage-list-state">{text.noAdminMounts}</div> : (
            <div className="storage-list-items">
              {mounts.map((mount) => (
                <button key={mount.id} type="button" className="storage-list-item" aria-current={selectedMountId === mount.id ? 'true' : undefined} onClick={() => { setSelectedMountId(mount.id); onMountChange?.(mount.id); }}>
                  <span className={`member-status-dot ${mount.health === 'active' ? 'ok' : 'danger'}`} />
                  <span><strong>{mount.name}</strong><small>{mount.space}</small></span>
                  <small>{mountModeLabel(mount.mode, text)}</small>
                </button>
              ))}
            </div>
          )}
        </aside>

        <section className="storage-detail" aria-live="polite">
          {!selectedMountId ? <div className="storage-detail-state">{text.storageLocationSelect}</div> : loadingDetail && !detail ? <div className="storage-detail-state">{text.loading}</div> : !detail ? <div className="storage-detail-state">{text.storageLocationUnavailable}</div> : (
            <>
              <header className="storage-detail-header">
                <div>
                  <div className="storage-title-row"><h2>{detail.name}</h2><span className={`storage-status ${detail.health === 'active' ? 'active' : 'unavailable'}`}>{mountStatusLabel(detail.health, text)}</span></div>
                  <p>{spaceDisplayName(spaces.find((space) => space.id === detail.spaceId), text.mySpace) || detail.spaceName} / {detail.rootPath}</p>
                </div>
                {detail.health !== 'active' && <button className="member-secondary-action" type="button" onClick={() => void onReverify()} disabled={pendingAction === 'reverify'}>{text.reverifyMount}</button>}
              </header>

              <dl className="storage-facts">
                <div><dt>{text.mountKind}</dt><dd>{detail.kind === 'managed' ? text.managedMount : text.externalMount}</dd></div>
                <div><dt>{text.mountMode}</dt><dd>{mountModeLabel(detail.mode, text)}</dd></div>
                <div><dt>{text.enableIndex}</dt><dd>{detail.indexEnabled ? text.indexReady : text.indexDisabled}</dd></div>
                <div><dt>{text.storagePublicSharing}</dt><dd>{detail.allowPublicShares ? text.storageEnabled : text.storageDisabled}</dd></div>
              </dl>

              <div className="storage-tabs" role="tablist" aria-label={text.storageLocationDetailTabs}>
                <button type="button" role="tab" aria-selected={activeTab === 'access'} onClick={() => setActiveTab('access')}>{text.storageAccessTab}</button>
                <button type="button" role="tab" aria-selected={activeTab === 'settings'} onClick={() => setActiveTab('settings')}>{text.storageSettingsTab}</button>
              </div>

              {activeTab === 'access' ? (
                <div className="storage-tab-panel" role="tabpanel">
                  <div className="storage-section-heading"><div><h3>{text.storageWhitelistTitle}</h3><p>{text.storageWhitelistDetail}</p></div><strong>{grants.length}</strong></div>
                  <div className="storage-member-search">
                    <label><span>{text.storageEligibleSearch}</span><input type="search" value={memberSearch} onChange={(event) => setMemberSearch(event.target.value)} placeholder={text.storageEligibleSearchPlaceholder} /></label>
                    <label><span>{text.spaceMemberRole}</span><select value={newPermission} onChange={(event) => setNewPermission(event.target.value as SpaceMemberRole)}><option value="viewer">{text.spaceRoleViewer}</option><option value="editor">{text.spaceRoleEditor}</option><option value="manager">{text.spaceRoleManager}</option></select></label>
                  </div>
                  {eligibleMembers.length > 0 && (
                    <div className="storage-eligible-list">
                      {eligibleMembers.slice(0, 8).map((member) => {
                        const label = memberLabel(member);
                        return <div className="storage-person-row" key={member.accountId}><div><strong>{label.primary}</strong>{label.secondary && <small>{label.secondary}</small>}</div><button className="member-table-action" type="button" onClick={() => void onAddGrant(member.accountId)} disabled={pendingAction === `add:${member.accountId}`}>{text.storageGrantAdd}</button></div>;
                      })}
                    </div>
                  )}
                  {normalizedSearch && eligibleMembers.length === 0 && <p className="storage-inline-empty">{text.storageEligibleEmpty}</p>}

                  <div className="storage-grants">
                    {grants.length === 0 ? <div className="storage-access-empty"><strong>{text.storageNoGrants}</strong><p>{text.storageNoGrantsDetail}</p></div> : grants.map((grant) => {
                      const label = memberLabel(grant);
                      const actionPending = pendingAction === `grant:${grant.accountId}`;
                      return (
                        <div className="storage-grant-row" key={grant.accountId}>
                          <div><strong>{label.primary}</strong>{label.secondary && <small>{label.secondary}</small>}</div>
                          <span className="storage-effective-permission" title={`${text.storageSpacePermission}: ${permissionLabel(grant.spacePermission, text)}`}><small>{text.storageEffectivePermission}</small><strong>{permissionLabel(grant.effectivePermission, text)}</strong></span>
                          <select aria-label={`${label.primary} ${text.spaceMemberRole}`} value={grant.permission} onChange={(event) => void onUpdateGrant(grant, event.target.value as SpaceMemberRole)} disabled={actionPending}>
                            <option value="viewer">{permissionLabel('viewer', text)}</option><option value="editor">{permissionLabel('editor', text)}</option><option value="manager">{permissionLabel('manager', text)}</option>
                          </select>
                          <button className="member-table-action member-table-danger" type="button" onClick={() => void onDeleteGrant(grant)} disabled={actionPending}>{text.storageGrantRemove}</button>
                        </div>
                      );
                    })}
                  </div>
                </div>
              ) : (
                <div className="storage-tab-panel" role="tabpanel">
                  <form className="storage-settings-form" onSubmit={onSaveSettings}>
                    <label>{text.mountName}<input value={settings.displayName} onChange={(event) => setSettings({ ...settings, displayName: event.target.value })} required /></label>
                    <label>{text.mountMode}<select value={settings.mode} onChange={(event) => setSettings({ ...settings, mode: event.target.value as AdminMountDetail['mode'] })}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
                    <label className="storage-toggle"><input type="checkbox" checked={settings.indexEnabled} onChange={(event) => setSettings({ ...settings, indexEnabled: event.target.checked })} /><span><strong>{text.enableIndex}</strong><small>{text.storageIndexHint}</small></span></label>
                    <label className="storage-toggle"><input type="checkbox" checked={settings.allowPublicShares} onChange={(event) => setSettings({ ...settings, allowPublicShares: event.target.checked })} /><span><strong>{text.storagePublicSharing}</strong><small>{text.storagePublicSharingHint}</small></span></label>
                    <div className="storage-settings-actions"><button className="member-primary" type="submit" disabled={pendingAction === 'settings' || !settings.displayName.trim() || !settingsDirty}>{text.storageSettingsSave}</button></div>
                  </form>
                  <div className="storage-danger-zone"><div><strong>{text.deleteMount}</strong><p>{text.deleteMountDetail}</p></div><button className="member-table-action member-table-danger" type="button" onClick={() => setDeleteOpen(true)}>{text.deleteMount}</button></div>
                </div>
              )}
            </>
          )}
        </section>
      </div>

      {createOpen && (
        <div className="member-modal-backdrop">
          <form className="member-modal member-admin-form storage-create-modal" onSubmit={onCreateMount}>
            <h2 className="member-admin-form-wide">{text.storageLocationCreate}</h2>
            <label>{text.mountSpace}<select value={mountForm.spaceId} onChange={(event) => setMountForm({ ...mountForm, spaceId: event.target.value })} required>{spaces.map((space) => <option key={space.id} value={space.id}>{spaceDisplayName(space, text.mySpace)}</option>)}</select></label>
            <label>{text.mountName}<input value={mountForm.displayName} onChange={(event) => setMountForm({ ...mountForm, displayName: event.target.value })} required /></label>
            <label>{text.mountMode}<select value={mountForm.mode} onChange={(event) => setMountForm({ ...mountForm, mode: event.target.value as AdminMountDetail['mode'] })}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
            <label className="member-admin-form-wide">{text.mountRoot}<div className="member-path-suggest"><input value={mountForm.rootPath} onChange={(event) => setMountForm({ ...mountForm, rootPath: event.target.value })} onFocus={() => setShowPathSuggestions(true)} onBlur={() => window.setTimeout(() => setShowPathSuggestions(false), 120)} placeholder={text.mountRootPlaceholder} autoComplete="off" required />{showPathSuggestions && <div className="member-path-suggest-menu" role="listbox" aria-label={text.mountRoot}>{pathSuggestions.length === 0 ? <div className="member-path-suggest-empty">{text.noPathSuggestions}</div> : pathSuggestions.map((entry) => <button key={entry.path} type="button" className="member-path-suggest-item" onMouseDown={(event) => event.preventDefault()} onClick={() => setMountForm({ ...mountForm, rootPath: entry.path })}>{entry.path}</button>)}</div>}</div><small className="member-path-hint">{text.mountRootHint}</small>{allowedRoots.length > 0 && <div className="member-path-roots"><span>{text.allowedRoots}</span>{allowedRoots.map((root) => <button key={root} type="button" className="member-path-root" onClick={() => setMountForm({ ...mountForm, rootPath: root })}>{root}</button>)}</div>}</label>
            <label className="member-admin-checkbox"><input type="checkbox" checked={mountForm.indexEnabled} onChange={(event) => setMountForm({ ...mountForm, indexEnabled: event.target.checked })} />{text.enableIndex}</label>
            <div className="member-admin-form-wide member-modal-actions"><button type="button" onClick={() => setCreateOpen(false)}>{text.cancel}</button><button className="member-primary" type="submit" disabled={pendingAction === 'create' || !mountForm.spaceId}>{text.createMount}</button></div>
          </form>
        </div>
      )}

      {deleteOpen && detail && (
        <div className="member-modal-backdrop">
          <form className="member-modal" onSubmit={onDeleteMount}>
            <h2>{text.deleteMount}</h2><p className="member-modal-hint">{text.deleteMountDetail}</p><p className="member-modal-hint"><strong>{detail.name}</strong> / {spaceDisplayName(spaces.find((space) => space.id === detail.spaceId), text.mySpace) || detail.spaceName}</p>
            <label className="member-admin-checkbox"><input type="checkbox" checked={deleteData} onChange={(event) => setDeleteData(event.target.checked)} />{text.deleteMountData}</label><p className="member-modal-hint">{text.deleteMountDataHint}</p>
            <div><button type="button" onClick={() => { setDeleteOpen(false); setDeleteData(false); }}>{text.cancel}</button><button className="member-modal-danger" type="submit" disabled={pendingAction === 'delete'}>{text.confirmDeleteMount}</button></div>
          </form>
        </div>
      )}
    </div>
  );
}
