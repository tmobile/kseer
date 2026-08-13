import { useState, useEffect, useRef } from 'react'
import { KubernetesObject, TimeRange } from '../types'
import { Close, ChevronLeft, ChevronRight, ContentCopy, Check } from '@mui/icons-material'
import { fetchObjectVersionList, fetchObjectAtVersion } from '../api/clickhouse'

interface ObjectDetailsModalProps {
  object: KubernetesObject | null
  timeRange: TimeRange
  onClose: () => void
}

function ObjectDetailsModal({ object, onClose }: ObjectDetailsModalProps) {
  const [versions, setVersions] = useState<KubernetesObject[]>([])
  const [currentVersionIndex, setCurrentVersionIndex] = useState(0)
  const [showDiff, setShowDiff] = useState(false)
  const [loadingVersions, setLoadingVersions] = useState(false)
  const [loadingObject, setLoadingObject] = useState(false)
  const [copied, setCopied] = useState(false)
  const [currentObjectData, setCurrentObjectData] = useState<any>(null)
  const [previousObjectData, setPreviousObjectData] = useState<any>(null)
  const objectCache = useRef<Record<string, any>>({})

  // Phase 1: fire both queries simultaneously.
  // fetchObjectDetails (ORDER BY timestamp DESC LIMIT 1) returns fast and shows the spec immediately.
  // fetchObjectVersionList (no object column) populates the version nav when it arrives.
  useEffect(() => {
    if (!object) return

    setVersions([])
    setCurrentVersionIndex(0)
    setCurrentObjectData(null)
    setPreviousObjectData(null)
    objectCache.current = {}

    const load = async () => {
      setLoadingVersions(true)

      // Phase 1: show the clicked version immediately using the fast timestamp-bounded
      // query (PREWHERE includes timestamp → single-partition scan, ~2s vs 7s+).
      // fetchObjectDetails used a bare WHERE with no timestamp, causing an unbounded
      // scan that competed with fetchObjectVersionList running concurrently.
      try {
        const details = await fetchObjectAtVersion(
          object.uid, object.cluster_name, object.namespace, object.kind, object.timestamp
        )
        if (details) {
          const key = details.resource_version || details.timestamp
          objectCache.current[key] = details.object
          setCurrentObjectData(details.object)
        }
      } catch {
        // Non-fatal — version list will still populate navigation
      }

      // Phase 2: load version list only after the display query completes so both
      // don't hit ClickHouse concurrently (concurrent unbounded scans slow each other).
      try {
        const list = await fetchObjectVersionList(
          object.uid, object.cluster_name, object.namespace, object.kind
        )
        const toUse = list.length > 0 ? list : [object]
        setVersions(toUse)
        setCurrentVersionIndex(toUse.length - 1)
      } catch (err) {
        console.error('Failed to fetch version list:', err)
        setVersions([object])
        setCurrentVersionIndex(0)
      } finally {
        setLoadingVersions(false)
      }
    }

    load()
  }, [object])

  // Phase 2: fetch full object for the selected version (and previous for diff) on demand
  useEffect(() => {
    if (versions.length === 0) return

    const currentVer = versions[currentVersionIndex]
    const prevVer = currentVersionIndex > 0 ? versions[currentVersionIndex - 1] : null

    const fetchCached = async (ver: KubernetesObject): Promise<any> => {
      const key = ver.resource_version || ver.timestamp
      if (objectCache.current[key]) return objectCache.current[key]
      const result = await fetchObjectAtVersion(
        ver.uid, ver.cluster_name, ver.namespace, ver.kind, ver.timestamp
      )
      const data = result?.object || {}
      objectCache.current[key] = data
      return data
    }

    const load = async () => {
      const curKey = currentVer.resource_version || currentVer.timestamp
      const prevKey = prevVer ? (prevVer.resource_version || prevVer.timestamp) : null

      // If already cached, update synchronously — no loading flash
      if (objectCache.current[curKey]) {
        setCurrentObjectData(objectCache.current[curKey])
        setPreviousObjectData(prevKey ? objectCache.current[prevKey] || null : null)
        return
      }

      setLoadingObject(true)
      try {
        const [cur, prev] = await Promise.all([
          fetchCached(currentVer),
          prevVer ? fetchCached(prevVer) : Promise.resolve(null)
        ])
        setCurrentObjectData(cur)
        setPreviousObjectData(prev)
      } catch (err) {
        console.error('Failed to fetch object data:', err)
        setCurrentObjectData({})
        setPreviousObjectData(null)
      } finally {
        setLoadingObject(false)
      }
    }

    load()
  }, [currentVersionIndex, versions])

  if (!object) return null

  const currentVersion = versions[currentVersionIndex] || object

  const handlePreviousVersion = () => {
    if (currentVersionIndex > 0) setCurrentVersionIndex(currentVersionIndex - 1)
  }

  const handleNextVersion = () => {
    if (currentVersionIndex < versions.length - 1) setCurrentVersionIndex(currentVersionIndex + 1)
  }

  const copyToClipboard = () => {
    navigator.clipboard.writeText(JSON.stringify(currentObjectData, null, 2))
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const renderDiff = (obj1: any, obj2: any, path: string = '') => {
    const diffs: JSX.Element[] = []

    const allKeys = new Set([
      ...Object.keys(obj1 || {}),
      ...Object.keys(obj2 || {})
    ])

    allKeys.forEach(key => {
      const currentPath = path ? `${path}.${key}` : key
      const val1 = obj1?.[key]
      const val2 = obj2?.[key]

      if (val1 === undefined && val2 !== undefined) {
        diffs.push(
          <div key={currentPath} className="diff-line added">
            <span className="diff-marker">+</span>
            <span className="diff-path">{currentPath}:</span>
            <span className="diff-value">{JSON.stringify(val2)}</span>
          </div>
        )
      } else if (val1 !== undefined && val2 === undefined) {
        diffs.push(
          <div key={currentPath} className="diff-line removed">
            <span className="diff-marker">-</span>
            <span className="diff-path">{currentPath}:</span>
            <span className="diff-value">{JSON.stringify(val1)}</span>
          </div>
        )
      } else if (typeof val1 === 'object' && typeof val2 === 'object' && val1 !== null && val2 !== null) {
        diffs.push(...renderDiff(val1, val2, currentPath))
      } else if (val1 !== val2) {
        diffs.push(
          <div key={currentPath} className="diff-group">
            <div className="diff-line removed">
              <span className="diff-marker">-</span>
              <span className="diff-path">{currentPath}:</span>
              <span className="diff-value">{JSON.stringify(val1)}</span>
            </div>
            <div className="diff-line added">
              <span className="diff-marker">+</span>
              <span className="diff-path">{currentPath}:</span>
              <span className="diff-value">{JSON.stringify(val2)}</span>
            </div>
          </div>
        )
      }
    })

    return diffs
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-content" onClick={e => e.stopPropagation()}>
        <div className="modal-header">
          <div className="modal-title">
            <h2>{object.kind}: {object.name}</h2>
            <div className="modal-subtitle">
              <span>Cluster: {object.cluster_name}</span>
              <span className="separator">•</span>
              <span>Namespace: {object.namespace}</span>
            </div>
          </div>
          <button className="modal-close" onClick={onClose}>
            <Close />
          </button>
        </div>

        <div className="modal-navigation">
          <div className="version-controls">
            <button
              onClick={handlePreviousVersion}
              disabled={loadingVersions || currentVersionIndex === 0}
              className="version-nav-button"
            >
              <ChevronLeft sx={{ fontSize: 16 }} />
            </button>
            <span className="version-info">
              {loadingVersions ? 'Loading versions...' : (
                <>
                  Version {currentVersionIndex + 1} of {versions.length}
                  {currentVersion && (
                    <span className="version-details">
                      ({new Date(currentVersion.timestamp).toLocaleString()})
                    </span>
                  )}
                </>
              )}
            </span>
            <button
              onClick={handleNextVersion}
              disabled={loadingVersions || currentVersionIndex === versions.length - 1}
              className="version-nav-button"
            >
              <ChevronRight sx={{ fontSize: 16 }} />
            </button>
          </div>

          <div className="modal-actions">
            <button
              onClick={() => setShowDiff(!showDiff)}
              className={`action-button ${showDiff ? 'active' : ''}`}
              disabled={loadingVersions || currentVersionIndex === 0}
            >
              {showDiff ? 'Show Spec' : 'Show Diff'}
            </button>
            <button
              onClick={copyToClipboard}
              className="action-button"
              disabled={!currentObjectData}
            >
              {copied ? <Check sx={{ fontSize: 16 }} /> : <ContentCopy sx={{ fontSize: 16 }} />}
              {copied ? 'Copied!' : 'Copy'}
            </button>
          </div>
        </div>

        <div className="modal-body">
          {loadingObject ? (
            <div className="loading">Loading spec...</div>
          ) : showDiff && previousObjectData ? (
            <div className="diff-viewer">
              <h3>Changes from previous version</h3>
              <div className="diff-content">
                {renderDiff(previousObjectData, currentObjectData).length > 0 ? (
                  renderDiff(previousObjectData, currentObjectData)
                ) : (
                  <div className="no-changes">No changes in spec</div>
                )}
              </div>
            </div>
          ) : (
            <div className="spec-viewer">
              <pre>{JSON.stringify(currentObjectData, null, 2)}</pre>
            </div>
          )}
        </div>
      </div>

      <style>{`
        .modal-overlay {
          position: fixed;
          top: 0;
          left: 0;
          right: 0;
          bottom: 0;
          background: rgba(0, 0, 0, 0.7);
          display: flex;
          align-items: center;
          justify-content: center;
          z-index: 1000;
        }

        .modal-content {
          background: #1a1a1a;
          border-radius: 8px;
          width: 90%;
          max-width: 800px;
          height: 80vh;
          display: flex;
          flex-direction: column;
          box-shadow: 0 4px 20px rgba(0, 0, 0, 0.5);
        }

        .modal-header {
          display: flex;
          justify-content: space-between;
          align-items: center;
          padding: 20px;
          border-bottom: 1px solid #333;
        }

        .modal-title h2 {
          margin: 0;
          font-size: 24px;
          color: #fff;
        }

        .modal-subtitle {
          margin-top: 8px;
          color: #999;
          font-size: 14px;
        }

        .separator {
          margin: 0 8px;
        }

        .modal-close {
          background: none;
          border: none;
          color: #999;
          cursor: pointer;
          padding: 8px;
          border-radius: 4px;
          transition: all 0.2s;
        }

        .modal-close:hover {
          background: #333;
          color: #fff;
        }

        .modal-navigation {
          display: flex;
          justify-content: space-between;
          align-items: center;
          padding: 16px 20px;
          border-bottom: 1px solid #333;
        }

        .version-controls {
          display: flex;
          align-items: center;
          gap: 12px;
        }

        .version-nav-button {
          background: #333;
          border: none;
          color: #fff;
          padding: 6px;
          border-radius: 4px;
          cursor: pointer;
          transition: all 0.2s;
          display: flex;
          align-items: center;
          justify-content: center;
        }

        .version-nav-button:hover:not(:disabled) {
          background: #444;
        }

        .version-nav-button:disabled {
          opacity: 0.5;
          cursor: not-allowed;
        }

        .version-info {
          color: #fff;
          font-size: 14px;
        }

        .version-details {
          color: #999;
          margin-left: 8px;
          font-size: 12px;
        }

        .modal-actions {
          display: flex;
          gap: 12px;
        }

        .action-button {
          background: #333;
          border: none;
          color: #fff;
          padding: 8px 16px;
          border-radius: 4px;
          cursor: pointer;
          transition: all 0.2s;
          display: flex;
          align-items: center;
          gap: 6px;
          font-size: 14px;
        }

        .action-button:hover:not(:disabled) {
          background: #444;
        }

        .action-button:disabled {
          opacity: 0.5;
          cursor: not-allowed;
        }

        .action-button.active {
          background: #2196F3;
        }

        .modal-body {
          flex: 1;
          overflow: auto;
          padding: 20px;
        }

        .loading {
          display: flex;
          align-items: center;
          justify-content: center;
          height: 100%;
          color: #999;
        }

        .spec-viewer pre {
          margin: 0;
          color: #ccc;
          font-family: 'Monaco', 'Consolas', 'Courier New', monospace;
          font-size: 12px;
          line-height: 1.5;
          white-space: pre-wrap;
          word-wrap: break-word;
        }

        .diff-viewer h3 {
          margin: 0 0 16px 0;
          color: #fff;
          font-size: 16px;
        }

        .diff-content {
          font-family: 'Monaco', 'Consolas', 'Courier New', monospace;
          font-size: 12px;
          line-height: 1.6;
        }

        .diff-line {
          padding: 4px 8px;
          margin: 2px 0;
          border-radius: 3px;
          display: flex;
          align-items: center;
          gap: 8px;
        }

        .diff-line.added {
          background: rgba(76, 175, 80, 0.2);
          color: #4CAF50;
        }

        .diff-line.removed {
          background: rgba(244, 67, 54, 0.2);
          color: #f44336;
        }

        .diff-marker {
          font-weight: bold;
          width: 16px;
        }

        .diff-path {
          color: #999;
          min-width: 200px;
        }

        .diff-value {
          flex: 1;
          word-break: break-all;
        }

        .diff-group {
          margin: 8px 0;
        }

        .no-changes {
          color: #999;
          text-align: center;
          padding: 40px;
        }
      `}</style>
    </div>
  )
}

export default ObjectDetailsModal
