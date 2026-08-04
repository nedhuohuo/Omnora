import MemberFilesApp from './member/MemberFilesApp';

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
    return <RouteUnavailable title="分享页面独立部署中" detail="分享访问不会显示成员空间导航。" />;
  }
  return <RouteUnavailable title="页面不存在" detail="请返回成员文件空间。" />;
}
