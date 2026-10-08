import { useMemo, useState, type Dispatch, type SetStateAction } from 'react';

import FileTypeIcon from './FileTypeIcon';
import type { MemberSpace } from './types';
import './member-files.css';
import './ShareCollaborationPreview.css';

type PreviewFile = { name: string; kind: 'dir' | 'file'; modified: string };
type MountPreview = { id: string; name: string; source: string; root: string; items: PreviewFile[] };
type PreviewSpace = MemberSpace & { mounts: MountPreview[]; modified: string };
type AccountTab = 'files' | 'trash' | 'shares' | 'tokens' | 'docs' | 'account';

const accountTabs: Array<{ id: AccountTab; label: string }> = [
  { id: 'files', label: '文件' },
  { id: 'trash', label: '回收站' },
  { id: 'shares', label: '分享' },
  { id: 'tokens', label: 'AI Token' },
  { id: 'docs', label: '文档' },
  { id: 'account', label: '账户' },
];

const spaces: PreviewSpace[] = [
  {
    id: 'personal', type: 'personal', name: '我的个人空间', role: 'manager', modified: '2026-08-08 09:20',
    mounts: [{
      id: 'personal-root', name: '个人主目录', source: '系统托管挂载', root: '/',
      items: [
        { name: '我的笔记', kind: 'dir', modified: '2026-08-08 09:20' },
        { name: '收件箱', kind: 'dir', modified: '2026-08-07 18:42' },
        { name: '上传文件', kind: 'dir', modified: '2026-08-06 11:03' },
      ],
    }],
  },
  {
    id: 'shared-research', type: 'shared', name: '研发资料库', role: 'editor', modified: '2026-08-07 20:15',
    mounts: [
      {
        id: 'managed-root', name: '团队主目录', source: '系统托管挂载', root: '/',
        items: [
          { name: '产品路线图', kind: 'dir', modified: '2026-08-07 20:15' },
          { name: '会议纪要', kind: 'dir', modified: '2026-08-07 16:30' },
          { name: 'README.md', kind: 'file', modified: '2026-08-05 10:12' },
        ],
      },
      {
        id: 'design-nas', name: '设计素材 NAS', source: '外部只读挂载', root: '/design-assets',
        items: [
          { name: '品牌规范', kind: 'dir', modified: '2026-07-28 13:05' },
          { name: '发布物料', kind: 'dir', modified: '2026-07-22 09:44' },
          { name: '历史归档', kind: 'dir', modified: '2026-06-30 17:18' },
        ],
      },
    ],
  },
  {
    id: 'shared-travel', type: 'shared', name: '2026 旅行共建', role: 'viewer', modified: '2026-01-25 19:28',
    mounts: [{
      id: 'trip-photos', name: '旅行照片', source: '外部只读挂载', root: '/trip-photos',
      items: [
        { name: '行程规划', kind: 'dir', modified: '2026-01-25 19:28' },
        { name: '相机原片', kind: 'dir', modified: '2026-01-18 14:06' },
        { name: '精选集', kind: 'dir', modified: '2026-01-12 22:41' },
      ],
    }],
  },
];

const defaultActiveSpaceId: string | null = null;

export type PreviewState = { activeSpaceId: string | null };
export type PreviewAction = { type: 'select-space'; spaceId: string };

export function reducePreviewAction(state: PreviewState, action: PreviewAction): PreviewState {
  if (action.type !== 'select-space' || !spaces.some((space) => space.id === action.spaceId)) return state;
  return { ...state, activeSpaceId: action.spaceId };
}

function permissionForRole(role: MemberSpace['role']) {
  return role === 'viewer' ? 'viewer' : 'editor';
}

export function selectSpace(setActiveSpaceId: Dispatch<SetStateAction<string | null>>, spaceId: string) {
  setActiveSpaceId((activeSpaceId) => reducePreviewAction({ activeSpaceId }, { type: 'select-space', spaceId }).activeSpaceId);
}

type ShareCollaborationPreviewSidebarProps = {
  activeAccountTab: AccountTab;
  onSelectAccountTab: (tab: AccountTab) => void;
};

export function ShareCollaborationPreviewSidebar({ activeAccountTab, onSelectAccountTab }: ShareCollaborationPreviewSidebarProps) {
  return (
    <nav className="member-preview-account-nav" aria-label="账户功能">
      {accountTabs.map((tab) => (
        <button
          className={`member-nav ${activeAccountTab === tab.id ? 'active' : ''}`}
          type="button"
          onClick={() => onSelectAccountTab(tab.id)}
          aria-current={activeAccountTab === tab.id ? 'page' : undefined}
          key={tab.id}
        >
          {tab.label}
        </button>
      ))}
    </nav>
  );
}

function TeamSpacesPanel({ onOpenSpace }: { onOpenSpace: (spaceId: string) => void }) {
  const teamSpaces = spaces.filter((space) => space.type === 'shared');
  const [selectedSpaceId, setSelectedSpaceId] = useState(teamSpaces[0].id);
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [permission, setPermission] = useState<'viewer' | 'editor'>('editor');
  const selectedSpace = teamSpaces.find((space) => space.id === selectedSpaceId) ?? teamSpaces[0];

  return (
    <div className="member-preview-sharing-panel" aria-label="分享内容">
      <section className="member-preview-sharing-hero">
        <div>
          <span className="member-preview-placeholder-kicker">账户级协作</span>
          <h2>分享</h2>
          <p>公开分享、团队空间和 MCP 空间权限都归属当前账户；切换空间不会改变左侧功能入口。</p>
        </div>
        <button type="button" className="member-preview-primary-button" onClick={() => setShowCreateForm((open) => !open)}>
          {showCreateForm ? '收起创建表单' : '创建团队空间'}
        </button>
      </section>

      {showCreateForm && (
        <form className="member-preview-create-space" onSubmit={(event) => event.preventDefault()}>
          <label>空间名称<input aria-label="团队空间名称" placeholder="例如：产品发布协作" /></label>
          <button type="submit">创建（Mock）</button>
        </form>
      )}

      <div className="member-preview-sharing-grid">
        <section className="member-preview-sharing-card">
          <div className="member-preview-card-heading"><div><span>团队空间</span><p>创建后作为“文件”根目录中的文件夹进入。</p></div><strong>{teamSpaces.length} 个</strong></div>
          <div className="member-preview-team-list">
            {teamSpaces.map((space) => (
              <button key={space.id} type="button" className={space.id === selectedSpace.id ? 'active' : ''} onClick={() => setSelectedSpaceId(space.id)}>
                <FileTypeIcon kind="dir" name={space.name} className="member-file-icon dir" />
                <span><strong>{space.name}</strong><small>{space.role === 'viewer' ? '仅查看' : '可编辑'} · {space.mounts.length} 个挂载</small></span>
              </button>
            ))}
          </div>
          <button className="member-preview-text-button" type="button" onClick={() => onOpenSpace(selectedSpace.id)}>在文件中打开 {selectedSpace.name}</button>
        </section>

        <section className="member-preview-sharing-card" aria-label="成员与权限">
          <div className="member-preview-card-heading"><div><span>成员与权限</span><p>{selectedSpace.name} · 当前账号为 {selectedSpace.role}</p></div></div>
          <div className="member-preview-member-row"><span>me@omnora.local</span><strong>管理员</strong></div>
          <div className="member-preview-member-row"><span>nora@omnora.local</span><button type="button" className={permission === 'editor' ? 'selected' : ''} onClick={() => setPermission('editor')}>可编辑</button></div>
          <div className="member-preview-member-row"><span>kai@omnora.local</span><button type="button" className={permission === 'viewer' ? 'selected' : ''} onClick={() => setPermission('viewer')}>仅查看</button></div>
        </section>
      </div>

      <section className="member-preview-mcp-summary">
        <div><span>MCP 空间访问摘要</span><p>按账号为 MCP 分配整个空间；文件夹黑名单及其子目录始终禁止访问。</p></div>
        <strong>已授权 2 个空间</strong>
        <button type="button">编辑授权（Mock）</button>
      </section>
    </div>
  );
}

type ShareCollaborationPreviewProps = {
  initialActiveSpaceId?: string | null;
  initialAccountTab?: AccountTab;
};

export default function ShareCollaborationPreview({ initialActiveSpaceId = defaultActiveSpaceId, initialAccountTab = 'files' }: ShareCollaborationPreviewProps) {
  const [activeAccountTab, setActiveAccountTab] = useState<AccountTab>(initialAccountTab);
  const [activeSpaceId, setActiveSpaceId] = useState<string | null>(() => reducePreviewAction(
    { activeSpaceId: defaultActiveSpaceId },
    initialActiveSpaceId ? { type: 'select-space', spaceId: initialActiveSpaceId } : { type: 'select-space', spaceId: '' },
  ).activeSpaceId);
  const [currentPath, setCurrentPath] = useState('.');
  const [query, setQuery] = useState('');
  const [selectedNames, setSelectedNames] = useState<string[]>([]);
  const activeSpace = spaces.find((space) => space.id === activeSpaceId);
  const activePermission = activeSpace ? permissionForRole(activeSpace.role) : null;
  const allFiles = useMemo(() => activeSpace ? activeSpace.mounts.flatMap((mount) => mount.items.map((item) => ({ ...item, mount }))) : [], [activeSpace]);
  const visibleFiles = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    return normalizedQuery ? allFiles.filter(({ name, mount }) => `${name} ${mount.name}`.toLocaleLowerCase().includes(normalizedQuery)) : allFiles;
  }, [allFiles, query]);
  const visibleSpaces = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    return normalizedQuery ? spaces.filter((space) => `${space.name} ${space.role}`.toLocaleLowerCase().includes(normalizedQuery)) : spaces;
  }, [query]);
  const visibleNames = activeSpace ? visibleFiles.map(({ name }) => name) : visibleSpaces.map((space) => space.name);
  const allVisibleSelected = visibleNames.length > 0 && visibleNames.every((name) => selectedNames.includes(name));
  const tabLabel = accountTabs.find((tab) => tab.id === activeAccountTab)?.label ?? '文件';

  function resetFileSelection() { setCurrentPath('.'); setQuery(''); setSelectedNames([]); }
  function changeSpace(spaceId: string) { selectSpace(setActiveSpaceId, spaceId); resetFileSelection(); }
  function leaveSpace() { setActiveSpaceId(null); resetFileSelection(); }
  function toggleSelected(name: string) { setSelectedNames((current) => current.includes(name) ? current.filter((item) => item !== name) : [...current, name]); }
  function selectAllVisible() { setSelectedNames(allVisibleSelected ? [] : visibleNames); }
  function showFileSpace(spaceId: string) { setActiveAccountTab('files'); changeSpace(spaceId); }

  return (
    <main className="member-app member-preview-app">
      <header className="member-topbar member-preview-topbar">
        <div className="member-brand"><span>O</span>Omnora</div>
        <h1 className="member-preview-top-title">{tabLabel}</h1>
        <div className="member-top-actions"><button className="member-account" type="button">退出登录</button></div>
      </header>
      <div className="member-layout">
        <aside className="member-sidebar"><ShareCollaborationPreviewSidebar activeAccountTab={activeAccountTab} onSelectAccountTab={setActiveAccountTab} /></aside>
        <section className="member-content member-preview-content" aria-label={activeSpace ? `${activeSpace.name} 文件内容` : `${tabLabel} 内容`}>
          {activeAccountTab === 'shares' ? <TeamSpacesPanel onOpenSpace={showFileSpace} /> : activeAccountTab !== 'files' ? (
            <div className="member-preview-placeholder"><span className="member-preview-placeholder-kicker">账户功能</span><h2>{tabLabel}</h2><p>这是账户级功能，空间不会替换或分割左侧导航。</p></div>
          ) : (
            <div className="member-preview-file-shell">
              <div className="member-preview-file-header">
                <div className="member-preview-breadcrumb" aria-label="当前路径">
                  <button type="button" className="member-preview-icon-button" aria-label="返回上级目录" disabled={!activeSpace} onClick={leaveSpace}>↑</button>
                  <span>文件</span><span className="member-preview-breadcrumb-separator">/</span><span>{activeSpace?.name ?? '空间'}</span><span>{activeSpace && currentPath !== '.' ? ` / ${currentPath}` : ''}</span>
                  <button type="button" className="member-preview-refresh" aria-label="刷新">↻</button>
                </div>
                <label className="member-preview-file-search"><span aria-hidden="true">⌕</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={activeSpace ? `搜索 ${activeSpace.name}` : '搜索空间'} aria-label={activeSpace ? `搜索 ${activeSpace.name}` : '搜索空间'} /></label>
              </div>
              <div className="member-preview-file-toolbar">
                <button type="button" disabled={selectedNames.length === 0}>删除</button><button type="button" disabled={!activeSpace || activePermission === 'viewer'}>新建文件夹</button><button type="button" className="member-preview-upload" disabled={!activeSpace || activePermission === 'viewer'}>上传文件</button><span className="member-preview-toolbar-spacer" />
                {activeSpace ? <span className={`member-preview-permission ${activePermission}`}>{activePermission === 'viewer' ? '仅查看' : '可编辑'}</span> : <span className="member-preview-root-badge">空间目录</span>}
                <button type="button" className="member-preview-view-button" aria-label="按列表查看" aria-pressed="true">☷</button>
              </div>
              <div className="member-preview-file-context">
                {activeSpace ? <><span>{activeSpace.mounts.length} 个挂载</span><span>当前账号为 {activePermission}</span><span className="member-preview-context-note">空间内容直接展示，挂载来源显示在“存储”列</span></> : <><span>{visibleSpaces.length} 个空间</span><span>选择一个空间进入内容</span><span className="member-preview-context-note">空间以文件夹形式展示，进入后再查看目录内容</span></>}
              </div>
              <div className="member-preview-file-table" role="table" aria-label={activeSpace ? `${activeSpace.name} 文件列表` : '空间文件夹列表'}>
                <div className="member-preview-file-row member-preview-file-row-header" role="row"><label className="member-preview-check-cell"><input type="checkbox" checked={allVisibleSelected} onChange={selectAllVisible} aria-label="全选" /></label><span role="columnheader">名称</span><span role="columnheader">修改时间</span><span role="columnheader">存储</span></div>
                {activeSpace ? visibleFiles.map(({ name, kind, modified, mount }) => <div className="member-preview-file-row" role="row" key={`${mount.id}-${name}`}><label className="member-preview-check-cell"><input type="checkbox" checked={selectedNames.includes(name)} onChange={() => toggleSelected(name)} aria-label={`选择 ${name}`} /></label><div className="member-preview-file-name" role="cell"><button type="button" onClick={() => kind === 'dir' && setCurrentPath(name)}><FileTypeIcon kind={kind} name={name} className={`member-file-icon ${kind}`} /><span>{name}</span></button></div><span role="cell" className="member-preview-file-date">{modified}</span><span role="cell" className="member-preview-file-mount" title={`${mount.source} · ${mount.root}`}>{mount.name}</span></div>) : visibleSpaces.map((space) => <div className="member-preview-file-row member-preview-space-row" role="row" key={space.id}><label className="member-preview-check-cell"><input type="checkbox" checked={selectedNames.includes(space.name)} onChange={() => toggleSelected(space.name)} aria-label={`选择 ${space.name}`} /></label><div className="member-preview-file-name" role="cell"><button type="button" data-space-id={space.id} onClick={() => changeSpace(space.id)}><FileTypeIcon kind="dir" name={space.name} className="member-file-icon dir" /><span>{space.name}</span></button></div><span role="cell" className="member-preview-file-date">{space.modified}</span><span role="cell" className="member-preview-file-mount">{space.mounts.length} 个挂载</span></div>)}
                {((activeSpace && visibleFiles.length === 0) || (!activeSpace && visibleSpaces.length === 0)) && <div className="member-preview-file-empty">没有匹配的内容</div>}
              </div>
            </div>
          )}
        </section>
      </div>
    </main>
  );
}
