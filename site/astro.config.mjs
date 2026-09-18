// Documentation site: Starlight renders the Markdown pages in ../docs. See docs/pipelines.md.
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { unified } from '@astrojs/markdown-remark';
import remarkMarkdownLinks from './src/plugins/remark-md-links.mjs';

const repository = 'https://github.com/Code-Growers/tigerbeetle-operator';
// GitHub Pages serves a project site under /<repository>/.
const base = '/tigerbeetle-operator';

export default defineConfig({
  site: 'https://code-growers.github.io',
  base,
  trailingSlash: 'always',
  markdown: {
    // Astro 7 renders Markdown with Sätteri by default; remark plugins need the unified processor.
    processor: unified({
      remarkPlugins: [
        [remarkMarkdownLinks, { docsDir: fileURLToPath(new URL('../docs', import.meta.url)), base }],
      ],
    }),
  },
  integrations: [
    starlight({
      title: 'TigerBeetle Operator',
      description: 'Run TigerBeetle clusters on Kubernetes.',
      social: [{ icon: 'github', label: 'GitHub', href: repository }],
      editLink: { baseUrl: `${repository}/edit/main/docs/` },
      lastUpdated: false,
      sidebar: [
        {
          label: 'User guide',
          items: [
            { label: 'Installing the operator', slug: 'installation' },
            { label: 'Deploying a cluster', slug: 'deploying' },
            { label: 'Connecting applications', slug: 'clients' },
            { label: 'Operations', slug: 'operations' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Helm chart values', slug: 'chart-values' },
            { label: 'Argo CD health check', slug: 'argocd-health-check' },
          ],
        },
        {
          label: 'Development',
          collapsed: true,
          items: [
            { label: 'Pipelines and releases', slug: 'pipelines' },
            { label: 'Design notes: multi-cluster', slug: 'design/multi-cluster' },
          ],
        },
      ],
    }),
  ],
});
