---
layout: home

hero:
  name: go-muyuan
  text: High-speed Go download library
  tagline: Multi-connection, resumable, live-reconfigurable — as a reusable library
  image:
    src: /logo.png
    alt: go-muyuan
  actions:
    - text: Quick Start
      link: /en/guide/getting-started
      theme: brand
    - text: Introduction
      link: /en/guide/introduction
      theme: alt
    - text: GitHub
      link: https://github.com/xiaoLangZe/go-muyuan

features:
  - title: Multi-connection range downloads
    details: Splits a file into N segments fetched over T concurrent connections. Segments and connections are independent.
  - title: Workers total-budget mode
    details: Set one budget and the library auto-allocates connections and segments, splitting slow segments at runtime.
  - title: Live reconfiguration
    details: Change connections or segments while running; downloaded bytes are preserved and never re-fetched.
  - title: Batch queue
    details: Download many files with bounded concurrency. Tasks can be added while running and controlled individually.
  - title: Resumable across processes
    details: Progress persists beside the output and is validated against Content-Length / ETag / Last-Modified.
  - title: SSRF guard
    details: http/https only, with private addresses rejected at both validation and dial time.
---
