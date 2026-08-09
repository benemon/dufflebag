import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'

// PatternFly base styles. Bundled, not fetched — a self-hosted console cannot
// assume internet egress, which also rules out the mockup's Google Fonts link.
import '@patternfly/react-core/dist/styles/base.css'
import './theme.css'

import { App } from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
