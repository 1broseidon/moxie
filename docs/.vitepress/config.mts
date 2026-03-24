// @ts-ignore — VitePress supports async config at runtime

async function getLatestVersion(repo: string): Promise<string | null> {
  try {
    const res = await fetch(`https://api.github.com/repos/1broseidon/${repo}/releases/latest`)
    if (!res.ok) return null
    const data = await res.json() as { tag_name: string }
    return data.tag_name ?? null
  } catch {
    return null
  }
}

export default (async () => {
  const version = await getLatestVersion('moxie')

  return {
    title: 'moxie',
    description: 'Chat agent service for Telegram, Slack, and Webex',
    base: '/moxie/',
    appearance: false,
    cleanUrls: true,
    head: [
      ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
      ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
      ['link', { href: 'https://fonts.googleapis.com/css2?family=Work+Sans:wght@300;400;700&family=JetBrains+Mono:wght@400;500&display=swap', rel: 'stylesheet' }],
    ],
    themeConfig: {
      version,
      nav: [
        { text: 'Guide', link: '/guide/getting-started' },
        { text: 'Transports', link: '/guide/telegram' },
        { text: 'Reference', link: '/reference/cli' },
        { text: 'Changelog', link: '/changelog' },
      ],
      sidebar: [
        {
          text: 'Guide',
          items: [
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'Backends', link: '/guide/backends' },
          ],
        },
        {
          text: 'Transports',
          items: [
            { text: 'Telegram', link: '/guide/telegram' },
            { text: 'Slack', link: '/guide/slack' },
            { text: 'Webex', link: '/guide/webex' },
          ],
        },
        {
          text: 'Features',
          items: [
            { text: 'Schedules', link: '/guide/schedules' },
            { text: 'Subagents', link: '/guide/subagents' },
            { text: 'Chat Commands', link: '/guide/commands' },
          ],
        },
        {
          text: 'Reference',
          items: [
            { text: 'CLI', link: '/reference/cli' },
            { text: 'Configuration', link: '/reference/config' },
          ],
        },
        {
          text: 'Changelog',
          link: '/changelog',
        },
      ],
      socialLinks: [
        { icon: 'github', link: 'https://github.com/1broseidon/moxie' },
      ],
      outline: { level: [2, 3] },
    },
  }
})
