import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// 将 markdown 文档内的相对资源路径改写为同目录文件的预览链接；
// 绝对 URL 与站内绝对路径原样保留（外部地址仍受 CSP 'self' 限制）。
export type MarkdownAssetResolver = (src: string) => string;

export default function MarkdownPreview({ text, resolveAsset }: { text: string; resolveAsset: MarkdownAssetResolver }) {
  return (
    <div className="member-preview-md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          img: ({ src, alt }) => <img src={resolveAsset(src ?? '')} alt={alt ?? ''} />,
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
