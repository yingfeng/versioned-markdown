/**
 * MarkdownEditor — supports WYSIWYG (Lexical) and Raw modes.
 *
 * - Editable: Raw uses Monaco Editor.
 * - Read-only: Raw shows a simple <pre> block (GitHub-raw style).
 * Switching between modes keeps both editors in DOM (CSS display toggle).
 */

import { useState, useEffect, useRef, useCallback } from 'react'
import LexicalEditor from '../lib/editor/LexicalEditor'
import RawMarkdownEditor from '../lib/editor/RawMarkdownEditor'

interface Props {
  content: string
  onChange: (content: string) => void
  readOnly?: boolean
  fileName?: string
}

export default function MarkdownEditor({ content, onChange, readOnly }: Props) {
  const [showSource, setShowSource] = useState(false)
  const [rawContent, setRawContent] = useState(content)
  const contentRef = useRef(content)

  useEffect(() => {
    contentRef.current = content
    if (showSource) setRawContent(content)
  }, [showSource, content])

  const handleWysiwygChange = useCallback((md: string) => {
    if (readOnly) return
    contentRef.current = md
    onChange(md)
  }, [onChange, readOnly])

  const handleRawChange = useCallback((value: string) => {
    if (readOnly) return
    setRawContent(value)
    contentRef.current = value
    onChange(value)
  }, [onChange, readOnly])

  const toggleSource = useCallback(() => {
    if (!showSource) setRawContent(contentRef.current)
    setShowSource(prev => !prev)
  }, [showSource])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      <div className="nim-editor-container">
        {/* WYSIWYG */}
        <div style={{ flex: 1, display: showSource ? 'none' : 'flex', flexDirection: 'column', minHeight: 0 }}>
          <LexicalEditor
            content={content}
            onChange={handleWysiwygChange}
            readOnly={readOnly}
            placeholder={readOnly ? '' : 'Start writing...'}
            onToggleSource={toggleSource}
            showSource={showSource}
          />
        </div>
        {/* Raw / Source */}
        <div style={{ flex: 1, display: showSource ? 'flex' : 'none', flexDirection: 'column', minHeight: 0 }}>
          {readOnly ? (
            <div style={{ flex: 1, overflow: 'auto', padding: '16px 20px', fontSize: 13, lineHeight: 1.6, fontFamily: 'Menlo, Consolas, monospace', background: 'var(--nim-code-bg)', color: 'var(--nim-code-text)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '4px 12px', borderBottom: '1px solid var(--nim-border)', background: 'var(--nim-toolbar-bg)', flexShrink: 0, margin: '-16px -20px 12px' }}>
                <button onClick={toggleSource} className="nim-mode-btn" style={{ padding: '4px 10px', border: '1px solid var(--nim-primary)', borderRadius: 4, cursor: 'pointer', fontSize: 12, fontWeight: 500, background: 'var(--nim-bg-selected)', color: 'var(--nim-primary)', fontFamily: 'inherit' }}>
                  ✏ WYSIWYG
                </button>
              </div>
              <pre style={{ margin: 0, fontFamily: 'inherit', fontSize: 'inherit', lineHeight: 'inherit', whiteSpace: 'pre-wrap' }}>{rawContent}</pre>
            </div>
          ) : (
            <RawMarkdownEditor content={rawContent} onChange={handleRawChange} readOnly={readOnly} onToggleSource={toggleSource} />
          )}
        </div>
      </div>
    </div>
  )
}
