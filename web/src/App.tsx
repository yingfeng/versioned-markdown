import { BrowserRouter, Routes, Route } from 'react-router-dom'
import RootPage from './pages/RootPage'
import WorkspacePage from './pages/WorkspacePage'
import CommitPage from './pages/CommitPage'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<RootPage />} />
        <Route path="/:name" element={<WorkspacePage />} />
        <Route path="/:name/commits/:commitId" element={<CommitPage />} />
      </Routes>
    </BrowserRouter>
  )
}
