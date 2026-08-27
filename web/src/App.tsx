import MemberFilesApp from './member/MemberFilesApp';
import SharePortalApp from './SharePortalApp';

function RouteUnavailable({ title, detail }: { title: string; detail: string }) {
  return <main className="route-unavailable"><strong>Omnora</strong><h1>{title}</h1><p>{detail}</p></main>;
}

export default function App() {
  const pathname = window.location.pathname;
  if (pathname === '/' || pathname === '/app' || pathname.startsWith('/app/')) {
    return <MemberFilesApp entry="member" />;
  }
  if (pathname === '/admin' || pathname.startsWith('/admin/')) {
    return <MemberFilesApp entry="admin" />;
  }
  if (pathname === '/share' || pathname.startsWith('/share/')) {
    return <SharePortalApp />;
  }
  return <RouteUnavailable title="页面不存在" detail="请返回成员文件页面。" />;
}
