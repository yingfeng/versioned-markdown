/**
 * GraphView — entity-level knowledge graph.
 * Styled after vector-graph-rag: rounded rect nodes, bezier edges with labels.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  Handle,
  Position,
  MarkerType,
  type Node,
  type Edge,
  type NodeProps,
  type EdgeProps,
  type ReactFlowInstance,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { GraphNode, GraphEdge } from '../lib/graph/wiki-graph'

// ── Entity Node (vector-graph-rag style) ──

function EntityNode({ data }: NodeProps<{ label: string; linkCount: number }>) {
  const size = Math.max(70, Math.min(150, 60 + data.linkCount * 12))
  return (
    <div
      style={{
        padding: '8px 14px',
        borderRadius: 10,
        minWidth: size,
        maxWidth: 180,
        textAlign: 'center',
        background: 'var(--nim-bg-secondary)',
        border: '2px solid var(--nim-primary)',
        boxShadow: '0 2px 8px rgba(0,0,0,0.15)',
        cursor: 'pointer',
        transition: 'all 0.2s',
      }}
      onMouseEnter={e => { e.currentTarget.style.boxShadow = '0 4px 16px color-mix(in srgb, var(--nim-primary) 30%, transparent)'; e.currentTarget.style.transform = 'scale(1.05)'; }}
      onMouseLeave={e => { e.currentTarget.style.boxShadow = '0 2px 8px rgba(0,0,0,0.15)'; e.currentTarget.style.transform = 'scale(1)'; }}
      title={`${data.label} — mentioned in ${data.linkCount} page${data.linkCount > 1 ? 's' : ''}`}
    >
      <Handle type="target" position={Position.Left} style={{ width: 8, height: 8, background: 'var(--nim-primary)', border: '2px solid var(--nim-bg)', borderRadius: '50%' }} />
      <Handle type="source" position={Position.Right} style={{ width: 8, height: 8, background: 'var(--nim-primary)', border: '2px solid var(--nim-bg)', borderRadius: '50%' }} />

      <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--nim-text)', lineHeight: 1.3, wordBreak: 'break-word' }}>
        {data.label}
      </div>
      <div style={{ fontSize: 10, color: 'var(--nim-text-muted)', marginTop: 4 }}>
        {data.linkCount} page{data.linkCount > 1 ? 's' : ''}
      </div>
    </div>
  )
}

// ── Relation Edge (bezier + label, vector-graph-rag style) ──

function RelationEdge({
  id,
  sourceX, sourceY, targetX, targetY,
  sourcePosition, targetPosition,
  data,
  selected,
}: EdgeProps) {
  const edgeData = data as { weight: number; label: string }
  const isStrong = edgeData.weight > 1

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX, sourceY, sourcePosition,
    targetX, targetY, targetPosition,
    curvature: 0.25,
  })

  return (
    <>
      {/* Main edge path */}
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: selected ? 'var(--nim-primary)' : (isStrong ? 'var(--nim-text-muted)' : 'var(--nim-border)'),
          strokeWidth: selected ? 3 : (isStrong ? 2.5 : 1.5),
          opacity: isStrong ? 0.85 : 0.5,
        }}
        markerEnd={selected ? (MarkerType.ArrowClosed as any) : undefined}
      />

      {/* Glow layer for strong connections */}
      {isStrong && (
        <path
          d={edgePath}
          fill="none"
          stroke="var(--nim-primary)"
          strokeWidth={6}
          opacity={0.12}
          style={{ pointerEvents: 'none' }}
        />
      )}

      {/* Edge label */}
      <EdgeLabelRenderer>
        <div
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
            pointerEvents: 'all',
            fontSize: 9,
            padding: '2px 6px',
            borderRadius: 4,
            background: 'var(--nim-bg-tertiary)',
            color: 'var(--nim-text-faint)',
            border: '1px solid var(--nim-border)',
            whiteSpace: 'nowrap',
          }}
        >
          {edgeData.label}
        </div>
      </EdgeLabelRenderer>
    </>
  )
}

const nodeTypes = { entityNode: EntityNode }
const edgeTypes = { relationEdge: RelationEdge }

// ── Props ──

interface Props {
  nodes: GraphNode[]
  edges: GraphEdge[]
  onNavigate?: (fileId: string) => void
}

// ── Layout helper ──

function layoutGraph(srcNodes: GraphNode[], srcEdges: GraphEdge[]) {
  const N = srcNodes.length
  const centerX = 400
  const centerY = 300
  const radius = Math.max(160, N * 45)

  const flowNodes: Node[] = srcNodes.map((n, i) => {
    const angle = (2 * Math.PI * i) / Math.max(1, N) - Math.PI / 2
    const r = radius * (0.5 + 0.5 * Math.min(1, (n.linkCount) / 6))
    return {
      id: n.id,
      type: 'entityNode',
      position: { x: centerX + r * Math.cos(angle), y: centerY + r * Math.sin(angle) },
      data: { label: n.label, linkCount: n.linkCount, sourcePages: n.sourcePages },
    }
  })

  const flowEdges: Edge[] = srcEdges.map((e, i) => ({
    id: `e-${i}`,
    source: e.source,
    target: e.target,
    type: 'relationEdge',
    data: { weight: e.weight, label: e.label },
  }))

  return { flowNodes, flowEdges }
}

// ── Component ──

export default function GraphView({ nodes: srcNodes, edges: srcEdges, onNavigate }: Props) {
  const rfRef = useRef<ReactFlowInstance>(null)
  const onInit = useCallback((instance: ReactFlowInstance) => {
    rfRef.current = instance
    console.log('[GraphView] initializing')
    setTimeout(() => instance.fitView({ padding: 0.3 }), 200)
  }, [])

  const { flowNodes: initialNodes, flowEdges: initialEdges } = useMemo(
    () => {
      const result = layoutGraph(srcNodes, srcEdges)
      console.log('[GraphView] layout:', result.flowNodes.length, 'nodes at positions:',
        result.flowNodes.map(n => `${n.id}@(${Math.round(n.position.x)},${Math.round(n.position.y)})`))
      return result
    },
    [srcNodes, srcEdges],
  )

  const [flowNodes, setFlowNodes, onNodesChange] = useNodesState(initialNodes)
  const [flowEdges, setFlowEdges, onEdgesChange] = useEdgesState(initialEdges)
  const [entityPopup, setEntityPopup] = useState<{ label: string; pages: { fileId: string; label: string }[] } | null>(null)

  useEffect(() => {
    const { flowNodes: n, flowEdges: e } = layoutGraph(srcNodes, srcEdges)
    console.log('[GraphView] relayout:', n.length, 'nodes')
    setFlowNodes(n)
    setFlowEdges(e)
  }, [srcNodes, srcEdges, setFlowNodes, setFlowEdges])

  useEffect(() => {
    if (rfRef.current) {
      setTimeout(() => rfRef.current.fitView({ padding: 0.3 }), 100)
    }
  }, [flowNodes])

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    const sourcePages = node.data.sourcePages as { fileId: string; label: string }[] | undefined
    if (sourcePages && sourcePages.length > 0) {
      setEntityPopup({ label: node.data.label as string, pages: sourcePages })
    }
  }, [])

  const onPaneClick = useCallback(() => {
    setEntityPopup(null)
  }, [])

  if (srcNodes.length === 0) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--nim-text-faint)' }}>
        No entity relationships found in this workspace.
      </div>
    )
  }

  return (
    <div style={{ width: '100%', height: '100%', position: 'relative' }}>
      <style>{`
        .graph-controls { overflow: hidden; }
        .graph-controls button {
          background: var(--nim-bg-secondary) !important;
          border-bottom: 1px solid var(--nim-border) !important;
          fill: var(--nim-text) !important;
          color: var(--nim-text) !important;
        }
        .graph-controls button:hover {
          background: var(--nim-bg-hover) !important;
        }
        .graph-controls button svg {
          fill: var(--nim-text) !important;
        }
      `}</style>
      <div style={{ position: 'absolute', top: 8, left: 8, zIndex: 10, fontSize: 11, color: 'var(--nim-text-faint)', padding: '4px 10px', background: 'var(--nim-bg-tertiary)', borderRadius: 6, border: '1px solid var(--nim-border)' }}>
        {srcNodes.length} entities · {srcEdges.length} connections
      </div>
      <ReactFlow
        nodes={flowNodes}
        edges={flowEdges}
        onInit={onInit}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onPaneClick={onPaneClick}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.1}
        maxZoom={3}
        style={{ background: 'var(--nim-bg)' }}
      >
        <Background color="var(--nim-border)" gap={24} size={1} />
        <Controls
          style={{
            background: 'var(--nim-bg-secondary)',
            border: '1px solid var(--nim-border)',
            borderRadius: 8,
            color: 'var(--nim-text)',
          }}
          className="graph-controls"
        />
        <MiniMap
          nodeColor={() => { const s = getComputedStyle(document.documentElement); return s.getPropertyValue('--nim-primary').trim() || '#60a5fa'; }}
          style={{
            background: 'var(--nim-bg-secondary)',
            border: '1px solid var(--nim-border)',
            borderRadius: 8,
          }}
        />
      </ReactFlow>

      {/* Entity popup */}
      {entityPopup && (
        <div style={{
          position: 'absolute', top: 16, right: 16, zIndex: 50,
          background: 'var(--nim-bg-secondary)', border: '1px solid var(--nim-border)',
          borderRadius: 8, padding: 12, minWidth: 180, boxShadow: '0 4px 16px rgba(0,0,0,0.3)',
        }}>
          <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--nim-text)', marginBottom: 8 }}>{entityPopup.label}</div>
          <div style={{ fontSize: 11, color: 'var(--nim-text-faint)', marginBottom: 6 }}>Referenced in:</div>
          {entityPopup.pages.map(p => (
            <div
              key={p.fileId}
              onClick={() => { setEntityPopup(null); onNavigate?.(p.fileId); }}
              style={{
                padding: '4px 8px', cursor: 'pointer', borderRadius: 4, fontSize: 12,
                color: 'var(--nim-primary)', marginBottom: 2,
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--nim-bg-hover)'; }}
              onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
            >
              {p.label}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
