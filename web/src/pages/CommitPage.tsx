import { useState, useEffect } from 'react'
import { useParams, Link } from 'react-router-dom'
import type { TreeNode } from '../types'
import * as api from '../api'

export default function CommitPage() {
  const { name, commitId } = useParams()
  const [tree, setTree] = useState<TreeNode | null>(null)
  const [curNode, setCurNode] = useState<TreeNode | null>(null)
  const [selFile, setSelFile] = useState<TreeNode | null>(null)
  const [content, setContent] = useState('')
  const [error, setError] = useState('')
  const [breadcrumb, setBreadcrumb] = useState<{ id: string; name: string }[]>([])

  useEffect(() => { loadCommit() }, [commitId])

  async function loadCommit() {
    if (!commitId) return
    try {
      const t = await api.getCommitTree(commitId)
      setTree(t)
      setCurNode(t)
      setBreadcrumb([{ id: t.id, name: t.name }])
    } catch (e: any) { setError('Failed to load commit: ' + e.message) }
  }

  function enterFolder(node: TreeNode) {
    setCurNode(node)
    setSelFile(null)
    setContent('')
    setBreadcrumb(prev => [...prev, { id: node.id, name: node.name }])
  }

  function goToBreadcrumb(idx: number) {
    if (!tree) return
    const target = findBreadcrumbNode(tree, breadcrumb, idx)
    if (!target) return
    setCurNode(target)
    setSelFile(null)
    setContent('')
    setBreadcrumb(breadcrumb.slice(0, idx + 1))
  }

  async function selectFile(file: TreeNode) {
    if (!commitId) return
    setSelFile(file)
    try { setContent(await api.getCommitFileContent(commitId, file.id)) }
    catch { setContent('') }
  }

  const files = curNode?.children?.filter(c => c.type === 'file') || []
  const subfolders = curNode?.children?.filter(c => c.type === 'folder') || []

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-hdr">
          <span className="logo">llmwiki</span>
          <span className="badge badge-commit">HISTORY</span>
        </div>
        <div className="section-hdr" style={{padding:'12px 20px 6px'}}>
          <span style={{textTransform:'uppercase',letterSpacing:'1px',fontSize:'11px',color:'var(--fg3)'}}>
            Snapshot · {commitId?.slice(0,8)}
          </span>
          <Link to="/" className="btn-icon-sm" style={{textDecoration:'none'}}>↩</Link>
        </div>

        <div className="breadcrumb-bar">
          {breadcrumb.map((b, i) => (
            <span key={b.id}>
              {i > 0 && <span className="breadcrumb-sep">/</span>}
              <span className={`breadcrumb-item ${i === breadcrumb.length - 1 ? 'active' : ''}`}
                onClick={() => goToBreadcrumb(i)}>
                {b.name}
              </span>
            </span>
          ))}
        </div>

        <div className="files-section">
          {subfolders.map(f => (
            <div key={f.id} className="nav-item sub folder" onClick={() => enterFolder(f)}>
              <span>📁</span>
              <span className="nav-label" style={{marginLeft:8}}>{f.name}</span>
            </div>
          ))}
          {files.map(f => (
            <div key={f.id}
              className={`nav-item sub file ${selFile?.id === f.id ? 'active' : ''}`}
              onClick={() => selectFile(f)}>
              <span>📄</span>
              <span className="nav-label" style={{marginLeft:8}}>{f.name}</span>
            </div>
          ))}
          {subfolders.length === 0 && files.length === 0 && (
            <div className="hint" style={{padding:'12px 20px'}}>Empty folder</div>
          )}
        </div>
      </aside>

      <main className="main">
        {error && <div className="error-bar">{error}</div>}
        {selFile ? (
          <div className="editor-view">
            <div className="editor-toolbar">
              <div className="editor-toolbar-left">
                <span><strong>{selFile.name}</strong></span>
                <span className="badge badge-commit" style={{marginLeft:8}}>Read-only</span>
              </div>
              <Link to="/" className="btn">Back to Home</Link>
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
            <p>Browse the workspace tree in the sidebar</p>
            <p className="hint">Click folders to navigate · Click files to view content at this version</p>
            <Link to="/" className="btn-primary" style={{marginTop:16,display:'inline-flex',padding:'8px 18px',textDecoration:'none'}}>↩ Back to Home</Link>
          </div>
        )}
      </main>
    </div>
  )
}

function findBreadcrumbNode(tree: TreeNode, breadcrumb: { id: string; name: string }[], targetIdx: number): TreeNode | undefined {
  if (targetIdx === 0) return tree
  let result: TreeNode | undefined
  function walk(n: TreeNode, depth: number) {
    if (depth === targetIdx && n.id === breadcrumb[targetIdx]?.id) { result = n; return }
    if (result || depth >= targetIdx) return
    for (const c of n.children || []) walk(c, depth + 1)
  }
  walk(tree, 0)
  return result
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
