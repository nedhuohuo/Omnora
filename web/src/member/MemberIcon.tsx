export type MemberIconName =
  | 'account'
  | 'admin'
  | 'audit'
  | 'back'
  | 'backup'
  | 'collaboration'
  | 'docs'
  | 'folder'
  | 'home'
  | 'mount'
  | 'more'
  | 'refresh'
  | 'route'
  | 'search'
  | 'security'
  | 'settings'
  | 'share'
  | 'token'
  | 'trash'
  | 'upload'
  | 'users';

const paths: Record<MemberIconName, string[]> = {
  account: ['M10 10.2a3.2 3.2 0 1 0 0-6.4 3.2 3.2 0 0 0 0 6.4Z', 'M4.2 17.2c.8-3.2 3-4.8 5.8-4.8s5 1.6 5.8 4.8'],
  admin: ['M10 2.8 16 5v4.8c0 3.8-2.4 6.4-6 7.4-3.6-1-6-3.6-6-7.4V5l6-2.2Z', 'M7.2 10.1 9.1 12l3.7-4'],
  audit: ['M5.2 3.2h7.4l2.2 2.2v11.4H5.2z', 'M12.6 3.2v2.2h2.2', 'M7.4 8.4h5.2M7.4 11h5.2M7.4 13.6h3.4'],
  back: ['M12.8 5.2 8 10l4.8 4.8', 'M8.3 10h8.2'],
  backup: ['M5.1 14.7a4.8 4.8 0 0 1 .8-9.5A5.5 5.5 0 0 1 16 8.1a3.5 3.5 0 0 1-.8 6.8', 'M10 8.2v7', 'M7.6 10.6 10 8.2l2.4 2.4'],
  collaboration: ['M6.2 9.4a2.8 2.8 0 1 0 0-5.6 2.8 2.8 0 0 0 0 5.6Z', 'M13.8 9.4a2.8 2.8 0 1 0 0-5.6 2.8 2.8 0 0 0 0 5.6Z', 'M2.6 16.4c.6-2.4 2-3.6 3.8-3.6s3.2 1.2 3.8 3.6', 'M9.8 16.4c.6-2.4 2-3.6 3.8-3.6s3.2 1.2 3.8 3.6'],
  docs: ['M5.4 3.2h6.2L14.6 6v10.8H5.4z', 'M11.6 3.2V6h3', 'M7.5 9h5M7.5 11.8h5M7.5 14.4h3.2'],
  folder: ['M3.2 6.2V5.1c0-.9.6-1.5 1.5-1.5h3L9.2 5h6.1c.9 0 1.5.6 1.5 1.5v7.8c0 .9-.6 1.5-1.5 1.5H4.7c-.9 0-1.5-.6-1.5-1.5V6.2Z'],
  home: ['M3.6 9.2 10 3.8l6.4 5.4', 'M5.3 8.7v7.5h3.2v-4.6h3v4.6h3.2V8.7'],
  more: ['M5.1 10h.1M10 10h.1M14.9 10h.1'],
  mount: ['M4 5.2h12v9.6H4z', 'M6.2 8h7.6', 'M6.2 12h3.4', 'M13.2 12h.1'],
  refresh: ['M15.8 6.8A6.2 6.2 0 0 0 4.2 5.4L3 7.2', 'M3.2 13.2a6.2 6.2 0 0 0 11.6 1.4l1.2-1.8', 'M3 3.8v3.4h3.4', 'M17 16.2v-3.4h-3.4'],
  route: ['M4.4 5.2h4.2a2 2 0 0 1 0 4H7a2 2 0 0 0 0 4h8.6', 'M4.4 5.2l2-2M4.4 5.2l2 2', 'M15.6 13.2l-2-2M15.6 13.2l-2 2'],
  search: ['M9 14.2a5.2 5.2 0 1 0 0-10.4 5.2 5.2 0 0 0 0 10.4Z', 'm12.8 12.8 3.4 3.4'],
  security: ['M10 2.8 16 5v4.8c0 3.8-2.4 6.4-6 7.4-3.6-1-6-3.6-6-7.4V5l6-2.2Z', 'M10 8v3.8'],
  share: ['M13.8 6.2 16 4m0 0v5.4M16 4l-6.8 6.8', 'M8.4 5.2H5.2c-1 0-1.6.6-1.6 1.6v8c0 1 .6 1.6 1.6 1.6h8c1 0 1.6-.6 1.6-1.6v-3.2'],
  token: ['M8.4 10.4a3.6 3.6 0 1 1 1.8 1.8L7 15.4H4.6v-2.2L8.4 10.4Z', 'M13.4 6.6h.1'],
  trash: ['M4.8 6.2h10.4', 'M8 6.2V4.4h4v1.8', 'M6.2 6.2l.6 10h6.4l.6-10', 'M8.8 9.2v4.6M11.2 9.2v4.6'],
  upload: ['M10 14.8V4.2', 'M6.5 7.7 10 4.2l3.5 3.5', 'M4.2 15.8h11.6'],
  users: ['M7.4 9.3a2.9 2.9 0 1 0 0-5.8 2.9 2.9 0 0 0 0 5.8Z', 'M2.8 16.4c.7-2.7 2.3-4 4.6-4s3.9 1.3 4.6 4', 'M13 9.2a2.3 2.3 0 1 0 0-4.6', 'M13.4 12.5c1.8.2 3.1 1.5 3.8 3.9'],
  settings: ['M10 8.2a2 2 0 1 0 0 3.6 2 2 0 0 0 0-3.6Z', 'M10 3.2v1.2M10 15.6v1.2M3.2 10h1.2M15.6 10h1.2M5.2 5.2l.8.8M14 14l.8.8M14.8 5.2l-.8.8M6 14l-.8.8'],
};

export default function MemberIcon({ name, className }: { name: MemberIconName; className?: string }) {
  return (
    <span className={className ?? 'member-icon'} aria-hidden="true">
      <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
        {paths[name].map((path) => <path key={path} d={path} />)}
      </svg>
    </span>
  );
}
