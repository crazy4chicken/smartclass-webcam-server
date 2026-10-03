import { defineConfig } from 'vitepress'

const base = '/smartclass-webcam-server/'

export default defineConfig({
  title: 'SmartClass Webcam Server',
  description: 'Camera management, live streaming and recording backend for SmartClass',
  base,
  cleanUrls: true,
  lastUpdated: true,
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Deployment', link: '/guide/deploy' },
      { text: 'API Reference', link: '/api/overview' },
      {
        text: 'GitHub',
        link: 'https://github.com/crazy4chicken/smartclass-webcam-server'
      }
    ],
    sidebar: {
      '/guide/': [
        {
          text: 'Guide',
          items: [
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'Deployment', link: '/guide/deploy' }
          ]
        }
      ],
      '/api/': [
        {
          text: 'API Reference',
          items: [
            { text: 'Overview', link: '/api/overview' },
            { text: 'Cameras', link: '/api/reference/cameras' },
            { text: 'Streams', link: '/api/reference/streams' },
            { text: 'WebSocket', link: '/api/reference/websocket' },
            { text: 'Health', link: '/api/reference/health' },
            // Static file, not a route: VitePress leaves its URL untouched,
            // so the deployment base has to be part of the link.
            { text: 'OpenAPI document', link: `${base}openapi.yaml` }
          ]
        }
      ]
    },
    search: {
      provider: 'local'
    }
  }
})
