import type { ReactNode } from 'react';

/**
 * File type icon set rendered as inline SVG (zero dependency, offline-safe,
 * colored via currentColor so it follows the theme tokens of
 * .member-file-icon). See docs/design/ui-design-system.md §8.1.
 */

export type FileIconType = 'dir' | 'image' | 'video' | 'audio' | 'doc' | 'archive' | 'code' | 'file';

const IMAGE_EXTS = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'heic', 'heif', 'avif', 'ico', 'tiff', 'tif'];
const VIDEO_EXTS = ['mp4', 'mov', 'avi', 'mkv', 'webm', 'm4v', 'wmv', 'flv', 'mpg', 'mpeg', 'ts', '3gp'];
const AUDIO_EXTS = ['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'opus', 'wma', 'aiff', 'mid', 'midi'];
const DOC_EXTS = ['pdf', 'doc', 'docx', 'txt', 'md', 'markdown', 'rtf', 'ppt', 'pptx', 'xls', 'xlsx', 'csv', 'odt', 'ods', 'epub', 'pages', 'numbers', 'key'];
const ARCHIVE_EXTS = ['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'tgz', 'zst', 'iso', 'dmg', 'cab'];
const CODE_EXTS = ['js', 'jsx', 'ts', 'tsx', 'py', 'go', 'java', 'c', 'h', 'cpp', 'hpp', 'cs', 'rs', 'rb', 'php', 'html', 'htm', 'css', 'scss', 'json', 'yaml', 'yml', 'sh', 'bash', 'sql', 'xml', 'toml', 'ini', 'conf', 'lock'];

const EXT_GROUPS: ReadonlyArray<readonly [FileIconType, readonly string[]]> = [
  ['image', IMAGE_EXTS],
  ['video', VIDEO_EXTS],
  ['audio', AUDIO_EXTS],
  ['doc', DOC_EXTS],
  ['archive', ARCHIVE_EXTS],
  ['code', CODE_EXTS],
];

/** Maps an entry to its icon type; directories always get the folder icon. */
export function fileIconType(kind: 'dir' | 'file', name: string): FileIconType {
  if (kind === 'dir') return 'dir';
  const ext = name.includes('.') ? (name.split('.').pop() ?? '').toLowerCase() : '';
  for (const [type, exts] of EXT_GROUPS) {
    if (exts.includes(ext)) return type;
  }
  return 'file';
}

// Inline SVG paths, 16×16 viewBox, 1.5px stroke, rounded caps/joins.
// Shape conventions: file/folder outlines from the top-left corner, inner
// details (text lines, mountains, zipper) kept to the center-right area.
const PATHS: Record<FileIconType, ReactNode> = {
  dir: (
    <path d="M1.5 4.5A1.5 1.5 0 0 1 3 3h2.6l1.5 1.8H13a1.5 1.5 0 0 1 1.5 1.5v5.2A1.5 1.5 0 0 1 13 13H3a1.5 1.5 0 0 1-1.5-1.5v-7z" />
  ),
  image: (
    <>
      <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" />
      <circle cx="5.5" cy="6.2" r="1.3" />
      <path d="M1.8 11.6l3.5-3.4a1 1 0 0 1 1.4 0l2.4 2.3 1.4-1.4a1 1 0 0 1 1.4 0l2.3 2.3" />
    </>
  ),
  video: (
    <>
      <rect x="1.5" y="3" width="13" height="10" rx="1.5" />
      <path d="M6.4 5.7v4.6a.45.45 0 0 0 .7.38l3.6-2.3a.45.45 0 0 0 0-.76L7.1 5.32a.45.45 0 0 0-.7.38z" fill="currentColor" stroke="none" />
    </>
  ),
  audio: (
    <>
      <path d="M7.5 12.5V3.2l5-1.1v8.4" />
      <circle cx="5.3" cy="12.5" r="2" />
      <circle cx="10.3" cy="10.5" r="2" />
    </>
  ),
  doc: (
    <>
      <path d="M4 1.5h5.4L13 5.1v9.4H4z" />
      <path d="M9.4 1.5V5.1H13" />
      <path d="M6 8.2h4M6 10.7h4" />
    </>
  ),
  archive: (
    <>
      <rect x="1.5" y="3.5" width="13" height="9" rx="1.5" />
      <path d="M4.2 5.6 3.4 7.8h9.2l-.8-2.2" />
      <path d="M6.4 7.8v2M9.6 7.8v2" />
    </>
  ),
  code: (
    <>
      <path d="M5.6 4.6 2.2 8l3.4 3.4" />
      <path d="M10.4 4.6 13.8 8l-3.4 3.4" />
    </>
  ),
  file: (
    <>
      <path d="M4 1.5h5.4L13 5.1v9.4H4z" />
      <path d="M9.4 1.5V5.1H13" />
    </>
  ),
};

export default function FileTypeIcon({ kind, name, className }: {
  kind: 'dir' | 'file';
  name: string;
  className?: string;
}) {
  const type = fileIconType(kind, name);
  return (
    <span className={className} aria-hidden="true">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
        {PATHS[type]}
      </svg>
    </span>
  );
}
