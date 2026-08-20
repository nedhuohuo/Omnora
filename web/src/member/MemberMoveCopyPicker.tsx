import { useEffect, useState } from 'react';
import { ApiError, listMemberDirectoryChildren, type MemberContentLocator } from '../api';
import FileTypeIcon from './FileTypeIcon';
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

function displayPath(path: string) {
  const normalized = normalizePath(path);
  return normalized === '.' ? '/' : `/${normalized}`;
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
  const [browsePath, setBrowsePath] = useState('.');
  const [entries, setEntries] = useState<MemberDirectoryEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selectedDir, setSelectedDir] = useState<string | null>(null);

  const sourceParent = parentPath(entry.relativePath);
  const destination = selectedDir ? joinPath(selectedDir, entry.name) : '';
  const isSameDirectory = selectedDir !== null && normalizePath(selectedDir) === normalizePath(sourceParent);
  const isSelfOrDescendant =
    entry.kind === 'dir' &&
    selectedDir !== null &&
    (normalizePath(selectedDir) === normalizePath(entry.relativePath) ||
      normalizePath(selectedDir).startsWith(`${normalizePath(entry.relativePath)}/`));

  const canConfirm = selectedDir !== null && !isSameDirectory && !isSelfOrDescendant;

  const hint = (() => {
    if (selectedDir === null) return text.movePickerHint;
    if (isSameDirectory) return text.movePickerSameDirectory;
    if (isSelfOrDescendant) return text.movePickerInvalidDescendant;
    return `${text.movePickerSelected}: ${displayPath(destination)}`;
  })();

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError('');
    const locator: MemberContentLocator = { ...activeSource.locator, path: browsePath };
    void listMemberDirectoryChildren(locator, controller.signal)
      .then((payload) => {
        const listing = formatDirectoryChildren(payload);
        // Only directories are valid targets; files are not selectable as destination.
        setEntries(listing.entries.filter((item) => item.kind === 'dir'));
      })
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return;
        setEntries([]);
        setError(describeError(caught));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [activeSource.locator, browsePath]);

  const crumbs = browsePath === '.' ? [] : browsePath.split('/').filter(Boolean);

  return (
    <div className="member-modal-backdrop" role="presentation" onClick={onCancel}>
      <div
        className="member-modal member-admin-form"
        role="dialog"
        aria-modal="true"
        aria-label={move ? text.moveTitle : text.moveCopyTitle}
        onClick={(event) => event.stopPropagation()}
        style={{ maxWidth: 640 }}
      >
        <h2 className="member-admin-form-wide">{move ? text.moveTitle : text.moveCopyTitle}</h2>
        <p className="member-admin-form-wide member-modal-hint">
          {text.movePickerDescription} <strong>{entry.name}</strong>
        </p>

        <div className="member-share-picker member-admin-form-wide">
          <div className="member-share-picker-toolbar">
            <div className="member-share-picker-crumbs">
              <button type="button" onClick={() => setBrowsePath('.')}>
                {text.shareBrowseRoot}
              </button>
              {crumbs.map((part, index) => {
                const path = crumbs.slice(0, index + 1).join('/');
                const isLast = index === crumbs.length - 1;
                return (
                  <span key={path}>
                    <b>/</b>
                    {isLast ? <strong>{part}</strong> : <button type="button" onClick={() => setBrowsePath(path)}>{part}</button>}
                  </span>
                );
              })}
            </div>
            <div className="member-share-picker-actions">
              <button type="button" onClick={() => setSelectedDir(browsePath)}>
                {text.movePickerSelectCurrent}
              </button>
            </div>
          </div>

          <p className="member-share-picker-selected" style={{ color: isSameDirectory || isSelfOrDescendant ? 'var(--danger-text)' : undefined }}>
            {hint}
          </p>

          <div className="member-share-picker-list" role="listbox" aria-label={text.moveTargetPath}>
            {loading ? (
              <div className="member-share-picker-loading">{text.loading}</div>
            ) : error ? (
              <div className="member-share-picker-empty">{error}</div>
            ) : entries.length === 0 ? (
              <div className="member-share-picker-empty">{text.movePickerEmpty}</div>
            ) : (
              entries.map((dir) => {
                const dirPath = normalizePath(dir.relativePath);
                const isSelected = selectedDir !== null && normalizePath(selectedDir) === dirPath;
                return (
                  <div key={`dir-${dirPath}`} className={`member-share-picker-row${isSelected ? ' selected' : ''}`}>
                    <button type="button" className="member-share-picker-item" onClick={() => setBrowsePath(dirPath)}>
                      <FileTypeIcon kind="dir" name={dir.name} className="member-file-icon dir" />
                      <span>{dir.name}</span>
                    </button>
                    <button type="button" className="member-share-picker-select" onClick={() => setSelectedDir(dirPath)}>
                      {text.shareSelectItem}
                    </button>
                  </div>
                );
              })
            )}
          </div>

          {browsePath !== '.' && (
            <button type="button" className="member-secondary-action" style={{ alignSelf: 'start' }} onClick={() => setBrowsePath(parentPath(browsePath))}>
              {text.back}
            </button>
          )}
        </div>

        <div className="member-admin-form-wide member-modal-actions">
          <button type="button" onClick={onCancel}>{text.cancel}</button>
          <button className="member-primary" type="button" onClick={() => selectedDir && onConfirm(selectedDir)} disabled={!canConfirm}>
            {move ? text.moveSubmit : text.copySubmit}
          </button>
        </div>
      </div>
    </div>
  );
}
