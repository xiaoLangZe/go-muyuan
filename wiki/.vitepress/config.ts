import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'go-muyuan',
  description: '高速 Go 下载库用户手册',
  base: '/go-muyuan/',
  cleanUrls: true,
  lastUpdated: true,

  head: [
    ['link', { rel: 'icon', type: 'image/png', href: '/go-muyuan/logo.png' }]
  ],

  locales: {
    // 中文为默认语言（root，无 URL 前缀）
    'root': {
      label: '简体中文',
      lang: 'zh-CN',
      themeConfig: {
        nav: [
          { text: '指南', link: '/guide/introduction', activeMatch: '/guide/' },
          { text: 'API 参考', link: '/api/downloader', activeMatch: '/api/' },
          { text: 'GitHub', link: 'https://github.com/xiaoLangZe/go-muyuan' }
        ],
        sidebar: {
          '/guide/': [
            {
              text: '开始',
              items: [
                { text: '简介', link: '/guide/introduction' },
                { text: '快速上手', link: '/guide/getting-started' }
              ]
            },
            {
              text: '使用',
              items: [
                { text: '配置选项', link: '/guide/configuration' },
                { text: '运行时控制', link: '/guide/runtime-control' },
                { text: '批量队列', link: '/guide/queue' },
                { text: '进度报告', link: '/guide/progress' },
                { text: '安全防护', link: '/guide/security' }
              ]
            },
            {
              text: '设计',
              items: [
                { text: '架构设计', link: '/guide/architecture' }
              ]
            }
          ],
          '/api/': [
            {
              text: 'API 参考',
              items: [
                { text: 'Downloader', link: '/api/downloader' },
                { text: 'Queue', link: '/api/queue' },
                { text: '类型与错误', link: '/api/types' }
              ]
            }
          ]
        },
        outline: { label: '本页目录', level: [2, 3] },
        docFooter: { prev: '上一页', next: '下一页' },
        lastUpdated: { text: '最后更新' },
        returnToTop: { label: '回到顶部' },
        sidebarMenuLabel: '菜单',
        darkModeSwitchLabel: '外观',
        search: {
          provider: 'local',
          options: {
            translations: {
              button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
              modal: {
                noResultsText: '未找到相关结果',
                resetButtonTitle: '清除查询条件',
                footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' }
              }
            }
          }
        }
      }
    },
    // 英文（/en/ 前缀）
    'en': {
      label: 'English',
      lang: 'en-US',
      link: '/en/',
      themeConfig: {
        nav: [
          { text: 'Guide', link: '/en/guide/introduction', activeMatch: '/en/guide/' },
          { text: 'API Reference', link: '/en/api/downloader', activeMatch: '/en/api/' },
          { text: 'GitHub', link: 'https://github.com/xiaoLangZe/go-muyuan' }
        ],
        sidebar: {
          '/en/guide/': [
            {
              text: 'Getting Started',
              items: [
                { text: 'Introduction', link: '/en/guide/introduction' },
                { text: 'Quick Start', link: '/en/guide/getting-started' }
              ]
            },
            {
              text: 'Usage',
              items: [
                { text: 'Configuration', link: '/en/guide/configuration' },
                { text: 'Runtime Control', link: '/en/guide/runtime-control' },
                { text: 'Batch Queue', link: '/en/guide/queue' },
                { text: 'Progress', link: '/en/guide/progress' },
                { text: 'Security', link: '/en/guide/security' }
              ]
            },
            {
              text: 'Design',
              items: [
                { text: 'Architecture', link: '/en/guide/architecture' }
              ]
            }
          ],
          '/en/api/': [
            {
              text: 'API Reference',
              items: [
                { text: 'Downloader', link: '/en/api/downloader' },
                { text: 'Queue', link: '/en/api/queue' },
                { text: 'Types & Errors', link: '/en/api/types' }
              ]
            }
          ]
        },
        outline: { label: 'On this page', level: [2, 3] },
        docFooter: { prev: 'Previous', next: 'Next' },
        lastUpdated: { text: 'Last updated' },
        returnToTop: { label: 'Return to top' },
        sidebarMenuLabel: 'Menu',
        darkModeSwitchLabel: 'Appearance',
        search: { provider: 'local' },
        footer: {
          message: 'Released under the MIT License.',
          copyright: 'Copyright © 2024-present xiaoLangZe'
        }
      }
    }
  }
})
