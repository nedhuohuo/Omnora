import { useEffect, useState, useCallback } from 'react';
import { ApiError, listMemberDirectoryChildren, type MemberContentLocator } from '../api';
import { localeMessages, type MemberLocale } from './i18n';
import { formatDirectoryChildren, type MemberDirectoryEntry } from './types';

function describeError(error: unknown) {
  if (error instanceof ApiError) {
    const body = error.body as { error?: { message?: string } } | undefined;
    return body?.error?.message || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

function parentPath(path: string) {
  const parts = path.split('/').filter((part) => part && part !== '.');
  parts.pop();
  return parts.join('/') || '.';
}

function joinPath(directory: string, name: string) {
  return directory === '.' ? name : `${directory.replace(/\/+$/, '')}/${name}`;
}

function normalizePath(path: string) {
  const trimmed = path.trim();
  if (!trimmed || trimmed === '.' || trimmed === '/') return '.';
  return trimmed.replace(/^\/+|\/+$/g, '');
}

type TreeNode = {
  id: string;
  name: string;
  path: string;
  children: TreeNode[];
  loaded: boolean;
  loading: boolean;
};

function nodePath(node: TreeNode) {
  return node.path;
}

type Props = {
  locale: MemberLocale;
  activeSource: { locator: MemberContentLocator; label: string };
  entry: MemberDirectoryEntry;
  move: boolean;
  onCancel: () => void;
  onConfirm: (targetDir: string) => void;
};

export default function MemberMoveCopyPicker({ locale, activeSource, entry, move, onCancel, onConfirm }: Props) {
  const text = localeMessages[locale];
  const [root, setRoot] = useState<TreeNode>({ id: '__root__', name: text.shareBrowseRoot, path: '.', children: [], loaded: false, loading: false });
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [error, setError] = useState('');

  const loadChildren = useCallback(async (node: TreeNode) => {
    if (node.loaded) return node;
    const locator: MemberContentLocator = { ...activeSource.locator, path: node.path };
    try {
      const payload = await listMemberDirectoryChildren(locator);
      const listing = formatDirectoryChildren(payload);
      const dirs = listing.entries.filter((item) => item.kind === 'dir');
      const children: TreeNode[] = dirs.map((dir) => ({
        id: dir.relativePath,
        name: dir.name,
        path: normalizePath(dir.relativePath),
        children: [],
        loaded: false,
        loading: false,
      }));
      return { ...node, children, loaded: true, loading: false };
    } catch {
      return { ...node, children: [], loaded: true, loading: false };
    }
  }, [activeSource.locator]);

  useEffect(() => {
    void loadChildren(root).then(setRoot);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const toggleExpand = useCallback(async (nodeId: string) => {
    setRoot((prev) => {
      const next = structuredClone(prev) as TreeNode;
      const find = (n: TreeNode): TreeNode | null => {
        if (n.id === nodeId) return n;
        for (const c of n.children) { const found = find(c); if (found) return found; }
        return null;
      };
      const target = find(next);
      if (!target) return prev;
      if (target.children.length > 0) {
        target.children = [];
      } else if (!target.loaded) {
        target.loading = true;
        void loadChildren(target).then((loaded) => {
          setRoot((prev2) => {
            const next2 = structuredClone(prev2) as TreeNode;
            const t = find(next2);
            if (t) { t.children = loaded.children; t.loaded = true; t.loading = false; }
            return next2;
          });
        });
      }
      return next;
    });
  }, [loadChildren]);

  const sourceParent = parentPath(entry.relativePath);
  const isSameDirectory = selectedPath !== null && normalizePath(selectedPath) === normalizePath(sourceParent);
  const isSelfOrDescendant =
    entry.kind === 'dir' &&
    selectedPath !== null &&
    (normalizePath(selectedPath) === normalizePath(entry.relativePath) ||
      normalizePath(selectedPath).startsWith(`${normalizePath(entry.relativePath)}/`));
  const canConfirm = selectedPath !== null && !isSameDirectory && !isSelfOrDescendant;

  const countDirs = (node: TreeNode): number => {
    let count = node.children.length;
    for (const c of node.children) count += countDirs(c);
    return count;
  };

  const renderBreadcrumbs = () => {
    if (selectedPath === null) {
      return <span className="mcp-bc-item current">{text.shareBrowseRoot}</span>;
    }
    const parts = selectedPath === '.' ? [] : selectedPath.split('/').filter(Boolean);
    const segments: { label: string; path: string }[] = [{ label: text.shareBrowseRoot, path: '.' }];
    parts.forEach((part, i) => {
      segments.push({ label: part, path: parts.slice(0, i + 1).join('/') });
    });
    return segments.map((seg, i) => {
      const isLast = i === segments.length - 1;
      return (
        <span key={seg.path} className="mcp-bc-segment">
          {i > 0 && <span className="mcp-bc-sep">›</span>}
          {isLast
            ? <span className="mcp-bc-item current">{seg.label}</span>
            : <button type="button" className="mcp-bc-item" onClick={() => setSelectedPath(seg.path)}>{seg.label}</button>
          }
        </span>
      );
    });
  };

  const renderTree = (node: TreeNode, depth: number) => {
    const isExpanded = node.children.length > 0;
    const isSelected = selectedPath !== null && normalizePath(selectedPath) === normalizePath(node.path);
    return (
      <div key={node.id}>
        <div
          className={`mcp-tree-row${isSelected ? ' selected' : ''}`}
          style={{ paddingLeft: 12 + depth * 20 }}
          onClick={() => setSelectedPath(node.path)}
        >
          <button
            type="button"
            className={`mcp-tree-arrow${isExpanded ? ' open' : ''}${!node.loaded && node.children.length === 0 ? '' : ''}`}
            onClick={(e) => { e.stopPropagation(); void toggleExpand(node.id); }}
          >
            {node.loading ? '⋯' : '▶'}
          </button>
          <div className="mcp-tree-icon">📁</div>
          <span className="mcp-tree-name">{node.name}</span>
          <div className={`mcp-tree-check${isSelected ? ' checked' : ''}`} />
        </div>
        {isExpanded && node.children.map((child) => renderTree(child, depth + 1))}
      </div>
    );
  };

  return (
    <div className="member-modal-backdrop" role="presentation" onClick={onCancel}>
      <div
        className="mcp-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={move ? text.moveTitle : text.moveCopyTitle}
        onClick={(event) => event.stopPropagation()}
      >
        <div className="mcp-dialog-head">
          <h2>{move ? text.moveTitle : text.moveCopyTitle}</h2>
          <button type="button" className="mcp-dialog-close" onClick={onCancel}>×</button>
        </div>
        <div className="mcp-dialog-desc">
          {text.movePickerDescription} <strong>{entry.name}</strong>
        </div>

        <div className="mcp-breadcrumb-bar">
          {renderBreadcrumbs()}
          <span className="mcp-folder-count">
            {root.children.length > 0 ? `${root.children.length}${text.movePickerEmpty.includes('文件夹') ? '个文件夹' : ' folders'}` : ''}
          </span>
        </div>

        {error && <div className="member-error member-page-error" style={{ margin: '8px 16px' }}>{text.error}: {error}</div>}

        <div className="mcp-tree-container">
          {root.children.length === 0 && root.loaded
            ? <div className="mcp-empty-state"><div className="mcp-empty-icon">📂</div>{text.movePickerEmpty}</div>
            : root.children.map((child) => renderTree(child, 0))
          }
        </div>

        <div className="mcp-dialog-foot">
          <button type="button" className="mcp-btn mcp-btn-new" onClick={() => void 0}>{text.newFolder}</button>
          <div className="mcp-foot-spacer" />
          <button type="button" className="mcp-btn mcp-btn-cancel" onClick={onCancel}>{text.cancel}</button>
          <button
            type="button"
            className="mcp-btn mcp-btn-primary"
            onClick={() => selectedPath && onConfirm(selectedPath)}
            disabled={!canConfirm}
          >
            {move ? text.moveSubmit : text.copySubmit}
          </button>
        </div>
      </div>
    </div>
  );
}
