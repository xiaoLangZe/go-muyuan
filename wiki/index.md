---
layout: home

hero:
  name: go-muyuan（木鸢）
  text: 高速 Go 下载库
  tagline: 多连接、可续传、运行时可重配的可复用下载库
  image:
    src: /logo.png
    alt: go-muyuan 木鸢
  actions:
    - text: 快速上手
      link: /guide/getting-started
      theme: brand
    - text: 简介
      link: /guide/introduction
      theme: alt
    - text: GitHub
      link: https://github.com/xiaoLangZe/go-muyuan

features:
  - title: 多连接区间下载
    details: 把文件切成 N 段，由 T 条并发连接抓取。段数与连接数相互独立。
  - title: Workers 总和模式
    details: 只设一个预算，库自动分配连接与切片，并在运行中拆分慢分片以追求最高性能。
  - title: 运行时热重配
    details: 下载过程中随时改连接数、切片数；已下载字节自动保留，不重下。
  - title: 批量队列
    details: 有界并发批量下载多个文件，任务可运行中添加，单文件与整体均可控制。
  - title: 跨进程续传
    details: 进度持久化在输出文件旁，用 Content-Length / ETag / Last-Modified 校验后断点续传。
  - title: SSRF 防护
    details: http/https only，校验与拨号双重拒绝内网地址，可防 DNS rebinding。
---
