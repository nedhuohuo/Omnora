# Omnora / 万境

Omnora is a lightweight, self-hosted digital space for files, people, and AI.

万境是一个面向个人、家庭和小团队的轻量自托管数字空间。第一版聚焦 NAS 文件管理、安全分享、浏览器预览，以及通过 REST 和 MCP 为 AI 提供受控文件访问。

> Project status: design phase. There is no runnable release yet.

## First-release goals

- Run as one Go application container with an embedded Web UI.
- Use SQLite in WAL mode for accounts, permissions, shares, tokens, audit events, jobs, and file metadata.
- Support managed storage and existing NAS directories mounted read-only or read-write.
- Provide member accounts, space permissions, quotas, recycle bin, and resumable transfers.
- Share files or folders with an optional password, expiration, access limits, and revocation.
- Preview images, PDF, text, Markdown, and browser-native audio/video without server-side conversion.
- Expose a versioned REST API, OpenAPI 3.1 specification, and Streamable HTTP MCP server.
- Give AI credentials explicit operation scopes and directory boundaries.
- Maintain a lightweight global metadata index without reading file contents or calculating hashes.

## Resource model

Omnora is designed for low-power NAS hardware:

- one application container;
- no PostgreSQL, Redis, OpenSearch, office converter, or video transcoder;
- no GPU dependency;
- one low-priority indexing task at a time;
- target idle memory usage of 100-300 MB;
- `linux/amd64` and `linux/arm64` images.

The metadata index stores only file name, normalized path, type, size, and modification time. Indexing is rate-limited, pausable, and optional per storage root.

## Preview support

| Type | First-release behavior |
| --- | --- |
| Images | Browser display |
| PDF | Browser-side PDF.js rendering |
| Text | Size-limited browser display |
| Markdown | Sanitized browser rendering |
| Audio and video | Browser-native codecs only |
| Word, Excel, PowerPoint | Download only |
| Other formats | File information and download |

Omnora does not perform office conversion, video transcoding, OCR, full-text extraction, or semantic indexing in the first release.

## Documentation

- [Product and architecture design](docs/superpowers/specs/2026-08-02-omnora-design.md)
- [Community license](LICENSE)
- [Commercial licensing](COMMERCIAL-LICENSE.md)
- [Contribution policy](CONTRIBUTING.md)
- [Notices](NOTICE)

## Licensing

Omnora uses a dual-license model:

- The community edition is available under the GNU Affero General Public License v3.0 only (`AGPL-3.0-only`).
- Organizations that need proprietary terms without AGPL obligations must obtain a separate written commercial license from the Omnora Project maintainers.

Commercial activity is not automatically prohibited by the AGPL. Users may use Omnora commercially under the AGPL if they comply with its terms. The separate commercial license is for users who need different terms.

See [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md) for the licensing policy. This repository does not currently accept external code contributions because a legal commercial licensor has not yet been designated.
