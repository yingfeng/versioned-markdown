import { useState, useEffect } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import type { TreeNode, FileCommit } from '../types'
import * as api from '../api'

export default function CommitPage() {
  const { folderId, commitId } = useParams()
  const navigate = useNavigate()
  const [commit, setCommit] = useState<FileCommit | null>(null)
  const [tree, setTree] = useState<TreeNode | null>(null)
  const [selFile, setSelFile] = useState<TreeNode | null>(null)
  const [content, setContent] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    loadCommit()
  }, [commitId])

  async function loadCommit() {
    if (!commitId) return
    try {
      const ct = await api.getCommitTree(commitId)
      setTree(ct)
      // Basic commit info from first children
      setCommit({ id: commitId, folder_id: folderId || '', parent_id: '', message: '', author_id: '', file_count: 0 } as FileCommit)
    } catch (e: any) { setError('Failed to load commit: ' + e.message) }
  }

  async function selectFile(file: TreeNode) {
    if (!commitId) return
    setSelFile(file)
    try {
      const text = await api.getCommitFileContent(commitId, file.id)
      setContent(text)
    } catch { setContent('') }
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-hdr">
          <span className="logo">llmwiki</span>
          <span className="badge badge-commit">HISTORY</span>
        </div>
        <div className="section-hdr" style={{padding:'12px 20px 6px'}}>
          <span className="workspace-title" style={{padding:0,textTransform:'uppercase',letterSpacing:'1px',fontSize:'11px'}}>
            Snapshot · {commitId?.slice(0,8)}
          </span>
          <Link to={`/ws/${folderId}`} className="btn-icon-sm" style={{textDecoration:'none'}}>↩</Link>
        </div>
        <div className="commit-meta" style={{padding:'0 20px 8px',fontSize:'11px'}}>{commit?.file_count || 0} files in this snapshot</div>
        <div className="files-section">
          {tree && renderCommitTree(tree, selFile, selectFile)}
          {!tree && !error && <div className="hint">Loading...</div>}
          {error && <div className="hint" style={{color:'var(--danger)'}}>{error}</div>}
        </div>
      </aside>

      <main className="main">
        {selFile ? (
          <div className="editor-view">
            <div className="editor-toolbar">
              <div className="editor-toolbar-left">
                <span className="editor-breadcrumb"><strong>{selFile.name}</strong></span>
                <span className="badge badge-commit">Read-only</span>
              </div>
              <div className="toolbar-actions">
                <Link to={`/ws/${folderId}`} className="btn">Back to Current</Link>
              </div>
            </div>
            <div className="editor-body">
              <textarea className="editor-textarea" value={content} readOnly placeholder="File content at this version..." />
              <div className="preview-pane">
                <div className="preview-hdr">Preview</div>
                <div className="preview-content" dangerouslySetInnerHTML={{ __html: renderMarkdown(content) }} />
              </div>
            </div>
          </div>
        ) : (
          <div className="welcome">
            <h1>📜 Commit Snapshot</h1>
            <p>This is a read-only view of the workspace at this commit</p>
            <p className="hint">Select a file from the sidebar to view its content at this version</p>
            <Link to={`/ws/${folderId}`} className="btn-primary" style={{marginTop:16,display:'inline-flex',padding:'8px 18px',textDecoration:'none'}}>
              ↩ Back to Current Version
            </Link>
          </div>
        )}
      </main>
    </div>
  )
}

function renderCommitTree(
  node: TreeNode,
  selFile: TreeNode | null,
  onClick: (f: TreeNode) => void
): JSX.Element[] {
  return (node.children || []).map(child => {
    if (child.type === 'folder') {
      return (
        <span key={child.id}>
          <div className="nav-item sub folder" style={{cursor:'default'}}>
            <span className="nav-icon">📁</span>
            <span className="nav-label">{child.name}</span>
          </div>
          {renderCommitTree(child, selFile, onClick)}
        </span>
      )
    }
    return (
      <div key={child.id}
        className={`nav-item sub file ${selFile?.id === child.id ? 'active' : ''}`}
        onClick={() => onClick(child)}>
        <span className="nav-icon">📄</span>
        <span className="nav-label">{child.name}</span>
      </div>
    )
  })
}

function renderMarkdown(md: string): string {
  return md
    .replace(/^### (.+)$/gm, '<h3>$1</h3>').replace(/^## (.+)$/gm, '<h2>$1</h2>').replace(/^# (.+)$/gm, '<h1>$1</h1>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>').replace(/\*(.+?)\*/g, '<em>$1</em>')
    .replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code class="lang-$1">$2</code></pre>').replace(/`(.+?)`/g, '<code>$1</code>')
    .replace(/^- (.+)$/gm, '<li class="bullet">$1</li>').replace(/^(\d+)\. (.+)$/gm, '<li class="num">$2</li>')
    .replace(/!\[(.*?)\]\((.*?)\)/g, '<img alt="$1" src="$2" />').replace(/\[(.*?)\]\((.*?)\)/g, '<a href="$2">$1</a>')
    .replace(/\n\n/g, '</p><p>').replace(/\n/g, '<br/>')
}
