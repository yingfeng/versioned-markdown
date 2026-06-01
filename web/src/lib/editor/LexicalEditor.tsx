/**
 * LexicalEditor — WYSIWYG markdown editor using Meta's Lexical framework.
 *
 * Ported from Nimbalyst's NimbalystEditor + Editor.tsx.
 * Markdown import on init, markdown export on change.
 * Wraps content in nimbalyst-editor container for theming.
 */

import type { JSX } from 'react';
import { useMemo, useEffect, useRef } from 'react';
import { LexicalComposer } from '@lexical/react/LexicalComposer';
import { RichTextPlugin } from '@lexical/react/LexicalRichTextPlugin';
import { HistoryPlugin } from '@lexical/react/LexicalHistoryPlugin';
import { MarkdownShortcutPlugin } from '@lexical/react/LexicalMarkdownShortcutPlugin';
import { ListPlugin } from '@lexical/react/LexicalListPlugin';
import { LinkPlugin } from '@lexical/react/LexicalLinkPlugin';
import { HashtagPlugin } from '@lexical/react/LexicalHashtagPlugin';
import { TablePlugin } from '@lexical/react/LexicalTablePlugin';
import { LexicalErrorBoundary } from '@lexical/react/LexicalErrorBoundary';
import { useLexicalComposerContext } from '@lexical/react/LexicalComposerContext';
import { $getRoot, $createParagraphNode } from 'lexical';

import {
  $convertFromEnhancedMarkdownString,
  $convertToEnhancedMarkdownString,
  CORE_TRANSFORMERS,
  type Transformer,
} from './markdown';
import nodes from './nodes';
import theme from './editor-theme';
import ContentEditable from './ContentEditable';

// Direct CSS import (more reliable than @import in App.css)
import './editor-theme.css';

interface Props {
  content: string;
  onChange?: (markdown: string) => void;
  readOnly?: boolean;
  placeholder?: string;
  onToggleSource?: () => void;
  showSource?: boolean;
}

function InitialContentPlugin({ content, transformers }: { content: string; transformers: Transformer[] }) {
  const [editor] = useLexicalComposerContext();
  const seeded = useRef(false);

  useEffect(() => {
    // Seed ONCE on mount. New file = new key = new component = fresh seeded ref.
    if (seeded.current) return;
    seeded.current = true;

    editor.update(() => {
      const root = $getRoot();
      root.clear();
      if (content && content.trim()) {
        $convertFromEnhancedMarkdownString(content, transformers);
      } else {
        root.append($createParagraphNode());
      }
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editor]);

  return null;
}

function ChangeListener({ onChange, transformers }: { onChange?: (md: string) => void; transformers: Transformer[] }) {
  const [editor] = useLexicalComposerContext();
  const initRef = useRef(false);

  useEffect(() => {
    if (!onChange) return;
    const removeListener = editor.registerUpdateListener(({ dirtyElements, dirtyLeaves }) => {
      if (!initRef.current) {
        initRef.current = true;
        return;
      }
      if (dirtyElements.size === 0 && dirtyLeaves.size === 0) return;
      const markdown = editor.read(() => $convertToEnhancedMarkdownString(transformers));
      onChange(markdown);
    });
    return removeListener;
  }, [editor, onChange, transformers]);

  return null;
}

import ToolbarPlugin from './plugins/ToolbarPlugin';
import FloatingSelectionToolbar from './plugins/FloatingSelectionToolbar';
import TableActionsPlugin from './plugins/TableActionsPlugin';

export default function LexicalEditor({ content, onChange, readOnly = false, placeholder = 'Start writing...', onToggleSource, showSource = false }: Props): JSX.Element {
  const transformers = useMemo(() => CORE_TRANSFORMERS, []);

  const initialConfig = useMemo(() => ({
    namespace: 'LlmWikiEditor',
    nodes: [...nodes],
    theme,
    editable: !readOnly,
    onError: (error: Error) => { console.error('[LexicalEditor] Error:', error); },
  }), [readOnly]);

  return (
    <div className="nimbalyst-editor">
      <div className="editor-shell">
        <LexicalComposer initialConfig={initialConfig}>
          <div className="editor-container">
            <ToolbarPlugin onToggleSource={onToggleSource || (() => {})} showSource={showSource} />
            <InitialContentPlugin content={content} transformers={transformers} />
            <ChangeListener onChange={onChange} transformers={transformers} />
            <FloatingSelectionToolbar />
            <RichTextPlugin
              contentEditable={
                <div className="nim-editor-scroller">
                  <div className="nim-editor">
                    <ContentEditable placeholder={placeholder} />
                  </div>
                </div>
              }
              ErrorBoundary={LexicalErrorBoundary}
            />
            <HistoryPlugin />
            <MarkdownShortcutPlugin transformers={transformers} />
            <ListPlugin />
            <LinkPlugin />
            <HashtagPlugin />
            <TablePlugin />
            <TableActionsPlugin />
          </div>
        </LexicalComposer>
      </div>
    </div>
  );
}
