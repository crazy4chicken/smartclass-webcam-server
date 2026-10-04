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
      { text: 'Protocol', link: '/protocol/' },
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
            { text: 'Permissions', link: '/guide/permissions' },
            { text: 'Deployment', link: '/guide/deploy' },
            { text: 'Service Integration', link: '/guide/service-integration' }
          ]
        }
      ],
      '/protocol/': [
        {
          text: 'Device Protocol',
          items: [
            { text: 'Overview', link: '/protocol/' },
            { text: 'Registration', link: '/protocol/registration' },
            { text: 'WebSocket Transport', link: '/protocol/transport' },
            { text: 'Control Channel', link: '/protocol/control' },
            { text: 'Media Channels', link: '/protocol/media' }
          ]
        }
      ],
      '/api/': [
        {
          text: 'API Reference',
          items: [
            { text: 'Overview', link: '/api/overview' },
            { text: 'Devices', link: '/api/reference/devices' },
            { text: 'Streams', link: '/api/reference/streams' },
            { text: 'Photos', link: '/api/reference/photos' },
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
