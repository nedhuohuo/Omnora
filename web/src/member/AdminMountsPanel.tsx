import { type FormEvent, useCallback, useEffect, useState } from 'react';
import {
  type AdminMountGrant,
  type AdminMountGrantInput,
  type AdminMountListItem,
  type AdminUserPayload,
  ApiError,
  deleteAdminMount,
  deleteAdminMountGrant,
  listAdminHostDirectories,
  listAdminMountGrants,
  listAdminMounts,
  listAdminUsers,
  putAdminMountGrant,
  registerAdminMount,
  reverifyAdminMount,
  updateAdminMount,
} from '../api';
import { type MemberLocale, localeMessages } from './i18n';
import { useRecentReauth } from './RecentReauthProvider';

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string; code?: string } } | undefined;
    return body?.error?.message || body?.error?.code || `HTTP ${error.status}`;
  }
  return error instanceof Error ? error.message : String(error);
}

function modeLabel(mode: AdminMountListItem['mode'], text: typeof localeMessages[MemberLocale]) {
  return mode === 'read_only' ? text.readOnly : text.readWrite;
}

function statusLabel(status: AdminMountListItem['status'], text: typeof localeMessages[MemberLocale]) {
  const labels: Record<string, string> = {
    active: text.statusActive,
    unavailable: text.statusUnavailable,
    disabled: text.statusDisabled,
    pending: text.statusPending,
  };
  return labels[status] ?? status;
}

function grantLabel(grant: AdminMountGrant) {
  return grant.displayName || grant.email || grant.accountId;
}

type MountForm = {
  displayName: string;
  rootPath: string;
  governance: 'normal' | 'restricted';
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  grants: AdminMountGrantInput[];
};

const emptyForm: MountForm = {
  displayName: '',
  rootPath: '',
  governance: 'normal',
  mode: 'read_write',
  indexEnabled: true,
  grants: [],
};

export default function AdminMountsPanel({ locale, isInitialAdmin }: { locale: MemberLocale; isInitialAdmin: boolean }) {
  const text = localeMessages[locale];
  const { runSensitive } = useRecentReauth();
  const [mounts, setMounts] = useState<AdminMountListItem[]>([]);
  const [users, setUsers] = useState<AdminUserPayload[]>([]);
  const [roots, setRoots] = useState<string[]>([]);
  const [form, setForm] = useState<MountForm>(emptyForm);
  const [selectedMountId, setSelectedMountId] = useState('');
  const [grants, setGrants] = useState<AdminMountGrant[]>([]);
  const [grantAccountId, setGrantAccountId] = useState('');
  const [grantPermission, setGrantPermission] = useState<'viewer' | 'editor'>('viewer');
  const [editName, setEditName] = useState('');
  const [editMode, setEditMode] = useState<'read_only' | 'read_write'>('read_write');
  const [editIndexEnabled, setEditIndexEnabled] = useState(false);
  const [editShareEnabled, setEditShareEnabled] = useState(true);
  const [deleteTarget, setDeleteTarget] = useState<AdminMountListItem | null>(null);
  const [deleteConfirmation, setDeleteConfirmation] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const selectedMount = mounts.find((mount) => mount.id === selectedMountId) ?? null;

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [mountResponse, userResponse, directoryResponse] = await Promise.all([
        listAdminMounts(),
        listAdminUsers(),
        listAdminHostDirectories('/'),
      ]);
      setMounts(mountResponse.items ?? []);
      setUsers((userResponse.items ?? []).filter((user) => user.status === 'active'));
      const externalRoots = (directoryResponse.rootDetails ?? [])
        .filter((root) => root.kind === 'external')
        .map((root) => root.path);
      setRoots(externalRoots);
      setSelectedMountId((current) => mountResponse.items.some((mount) => mount.id === current) ? current : (mountResponse.items[0]?.id ?? ''));
      setForm((current) => ({ ...current, rootPath: current.rootPath || externalRoots[0] || '' }));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadGrants = useCallback(async (mountId: string) => {
    if (!mountId) {
      setGrants([]);
      return;
    }
    try {
      const response = await listAdminMountGrants(mountId);
      setGrants(response.items ?? []);
    } catch (caught) {
      setGrants([]);
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!selectedMount) {
      setGrants([]);
      return;
    }
    setEditName(selectedMount.displayName);
    setEditMode(selectedMount.mode);
    setEditIndexEnabled(selectedMount.indexEnabled);
    setEditShareEnabled(selectedMount.shareEnabled);
    void loadGrants(selectedMount.id);
  }, [loadGrants, selectedMount]);

  function addInitialGrant() {
    if (!grantAccountId || form.grants.some((grant) => grant.accountId === grantAccountId)) return;
    setForm((current) => ({
      ...current,
      grants: [...current.grants, { accountId: grantAccountId, permission: grantPermission }],
    }));
    setGrantAccountId('');
  }

  async function onCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    setNotice('');
    try {
      await runSensitive(() => registerAdminMount({ ...form, governance: isInitialAdmin ? form.governance : 'normal' }));
      setForm({ ...emptyForm, rootPath: roots[0] ?? '' });
      setGrantAccountId('');
      setNotice(text.adminOperationComplete);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onSaveSettings(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedMount) return;
    setLoading(true);
    setError('');
    setNotice('');
    try {
      await runSensitive(() => updateAdminMount(selectedMount.id, {
        displayName: editName.trim(),
        mode: editMode,
        indexEnabled: editIndexEnabled,
        shareEnabled: editShareEnabled,
      }));
      setNotice(text.adminOperationComplete);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onAddGrant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedMount || !grantAccountId) return;
    setLoading(true);
    setError('');
    try {
      const grant = await runSensitive(() => putAdminMountGrant(selectedMount.id, grantAccountId, grantPermission));
      setGrants((current) => [...current.filter((item) => item.accountId !== grant.accountId), grant]);
      setGrantAccountId('');
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onRemoveGrant(grant: AdminMountGrant) {
    if (!selectedMount) return;
    setLoading(true);
    setError('');
    try {
      await runSensitive(() => deleteAdminMountGrant(selectedMount.id, grant.accountId));
      setGrants((current) => current.filter((item) => item.accountId !== grant.accountId));
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setLoading(false);
    }
  }

  async function onReverify(mount: AdminMountListItem) {
    setLoading(true);
    setError('');
    try {
      await runSensitive(() => reverifyAdminMount(mount.id));
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  async function onDelete(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!deleteTarget || deleteConfirmation !== deleteTarget.displayName) return;
    setLoading(true);
    setError('');
    try {
      await runSensitive(() => deleteAdminMount(deleteTarget.id, deleteConfirmation));
      setDeleteTarget(null);
      setDeleteConfirmation('');
      setNotice(text.adminOperationComplete);
      await load();
    } catch (caught) {
      setError(describeError(caught));
      setLoading(false);
    }
  }

  return (
    <div className="member-admin-workspace">
      <div className="member-heading">
        <div><h1>{text.mountManagement}</h1><p>{text.mountManagementDetail}</p></div>
        <button className="member-secondary-action" type="button" onClick={() => void load()} disabled={loading}>{text.refresh}</button>
      </div>
      {error && <div className="member-error member-page-error">{text.error}: {error}</div>}
      {notice && <p className="member-admin-notice">{notice}</p>}

      <form className="member-admin-form" onSubmit={onCreate}>
        <label>{text.mountName}<input value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} required /></label>
        <label className="member-admin-form-wide">{text.mountRoot}
          <input value={form.rootPath} onChange={(event) => setForm({ ...form, rootPath: event.target.value })} list="admin-external-root-suggestions" placeholder={text.mountRootPlaceholder} required />
          <small className="member-path-hint">{text.adminMountRootHint}</small>
          <datalist id="admin-external-root-suggestions">{roots.map((root) => <option value={root} key={root} />)}</datalist>
        </label>
        {isInitialAdmin && <label>{text.mountGovernance}<select value={form.governance} onChange={(event) => setForm({ ...form, governance: event.target.value as MountForm['governance'] })}><option value="normal">{text.normalMount}</option><option value="restricted">{text.restrictedMount}</option></select></label>}
        <label>{text.mountMode}<select value={form.mode} onChange={(event) => setForm({ ...form, mode: event.target.value as MountForm['mode'] })}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
        <label className="member-admin-checkbox"><input type="checkbox" checked={form.indexEnabled} onChange={(event) => setForm({ ...form, indexEnabled: event.target.checked })} />{text.enableIndex}</label>
        <label>{text.mountGrantAccount}<select value={grantAccountId} onChange={(event) => setGrantAccountId(event.target.value)}><option value="">{text.mountNoGrants}</option>{users.filter((user) => !form.grants.some((grant) => grant.accountId === user.id)).map((user) => <option value={user.id} key={user.id}>{user.displayName} · {user.email}</option>)}</select></label>
        <label>{text.mountGrantPermission}<select value={grantPermission} onChange={(event) => setGrantPermission(event.target.value as MountForm['grants'][number]['permission'])}><option value="viewer">{text.readOnly}</option><option value="editor">{text.readWrite}</option></select></label>
        <div className="member-admin-form-actions"><button type="button" onClick={addInitialGrant} disabled={!grantAccountId}>{text.mountAddGrant}</button><button className="member-primary" type="submit" disabled={loading || !form.displayName.trim() || !form.rootPath.trim()}>{text.createMount}</button></div>
        {form.grants.length > 0 && <div className="member-path-roots member-admin-form-wide"><span className="member-path-roots-title">{text.mountGrants}</span>{form.grants.map((grant) => <p className="member-path-hint" key={grant.accountId}>{users.find((user) => user.id === grant.accountId)?.displayName ?? grant.accountId} · {grant.permission}<button type="button" onClick={() => setForm((current) => ({ ...current, grants: current.grants.filter((item) => item.accountId !== grant.accountId) }))}>{text.remove}</button></p>)}</div>}
      </form>

      {loading && mounts.length === 0 ? <div className="member-loading">{text.loading}</div> : mounts.length === 0 ? <div className="member-empty">{text.noAdminMounts}</div> : (
        <table className="member-admin-table">
          <thead><tr><th>{text.mountName}</th><th>{text.mountGovernance}</th><th>{text.mountMode}</th><th>{text.enableIndex}</th><th>{text.status}</th><th>{text.actions}</th></tr></thead>
          <tbody>{mounts.map((mount) => <tr key={mount.id}>
            <td>{mount.displayName}<small>{mount.rootPath}</small></td>
            <td>{mount.governance === 'restricted' ? text.restrictedMount : text.normalMount}</td>
            <td>{modeLabel(mount.mode, text)}</td>
            <td>{mount.indexEnabled ? text.indexReady : text.indexDisabled}</td>
            <td>{statusLabel(mount.status, text)}</td>
            <td><div className="member-admin-table-actions"><button className="member-table-action" type="button" onClick={() => setSelectedMountId(mount.id)}>{text.mountGrants}</button>{mount.status !== 'active' && <button className="member-table-action" type="button" onClick={() => void onReverify(mount)} disabled={loading}>{text.reverifyMount}</button>}<button className="member-table-action member-table-danger" type="button" onClick={() => { setDeleteTarget(mount); setDeleteConfirmation(''); }}>{text.deleteMount}</button></div></td>
          </tr>)}</tbody>
        </table>
      )}

      {selectedMount && <div className="member-admin-form member-admin-form-wide">
        <h2>{selectedMount.displayName} · {text.mountGrants}</h2>
        <form className="member-admin-inline-form" onSubmit={onSaveSettings}>
          <label>{text.mountName}<input value={editName} onChange={(event) => setEditName(event.target.value)} required /></label>
          <label>{text.mountMode}<select value={editMode} onChange={(event) => setEditMode(event.target.value as typeof editMode)}><option value="read_write">{text.readWrite}</option><option value="read_only">{text.readOnly}</option></select></label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={editIndexEnabled} onChange={(event) => setEditIndexEnabled(event.target.checked)} />{text.enableIndex}</label>
          <label className="member-admin-checkbox"><input type="checkbox" checked={editShareEnabled} onChange={(event) => setEditShareEnabled(event.target.checked)} />{editShareEnabled ? text.mountShareEnabled : text.mountShareDisabled}</label>
          <button className="member-primary" type="submit" disabled={loading}>{text.mountSettingsSave}</button>
        </form>
        {grants.length === 0 ? <p className="member-path-hint">{text.mountNoGrants}</p> : <table className="member-admin-table"><thead><tr><th>{text.mountGrantAccount}</th><th>{text.mountGrantPermission}</th><th>{text.actions}</th></tr></thead><tbody>{grants.map((grant) => <tr key={grant.accountId}><td>{grantLabel(grant)}<small>{grant.email}</small></td><td>{grant.permission === 'viewer' ? text.readOnly : text.readWrite}</td><td><button className="member-table-action member-table-danger" type="button" onClick={() => void onRemoveGrant(grant)} disabled={loading}>{text.remove}</button></td></tr>)}</tbody></table>}
        <form className="member-admin-inline-form" onSubmit={onAddGrant}><label>{text.mountGrantAccount}<select value={grantAccountId} onChange={(event) => setGrantAccountId(event.target.value)} required><option value="">{text.selectAccount}</option>{users.filter((user) => !grants.some((grant) => grant.accountId === user.id)).map((user) => <option value={user.id} key={user.id}>{user.displayName} · {user.email}</option>)}</select></label><label>{text.mountGrantPermission}<select value={grantPermission} onChange={(event) => setGrantPermission(event.target.value as 'viewer' | 'editor')}><option value="viewer">{text.readOnly}</option><option value="editor">{text.readWrite}</option></select></label><button className="member-primary" type="submit" disabled={loading || !grantAccountId}>{text.mountAddGrant}</button></form>
      </div>}

      {deleteTarget && <div className="member-modal-backdrop"><form className="member-modal" onSubmit={onDelete}><h2>{text.deleteMount}</h2><p className="member-modal-hint">{text.mountDeleteConfirmDetail}</p><label>{text.mountDeleteConfirmLabel}<input autoFocus value={deleteConfirmation} onChange={(event) => setDeleteConfirmation(event.target.value)} required /></label><div className="member-modal-actions"><button type="button" onClick={() => setDeleteTarget(null)}>{text.cancel}</button><button className="member-modal-danger" type="submit" disabled={loading || deleteConfirmation !== deleteTarget.displayName}>{text.confirmDeleteMount}</button></div></form></div>}
    </div>
  );
}
