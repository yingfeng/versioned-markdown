import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import WorkspacePage from './pages/WorkspacePage'
import CommitPage from './pages/CommitPage'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/ws/:folderId" element={<WorkspacePage />} />
        <Route path="/ws/:folderId/commit/:commitId" element={<CommitPage />} />
        <Route path="*" element={<Navigate to="/ws/default" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
