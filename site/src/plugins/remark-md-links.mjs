// Rewrites links between Markdown pages (`installation.md`, `../operations.md#upgrades`) into site
// routes (`/tigerbeetle-operator/installation/`), so the pages link correctly both when read on
// GitHub and when rendered here.
import path from 'node:path';

const pageLink = /^(?![a-z][a-z0-9+.-]*:|\/|#)([^#?]+)\.mdx?(#.*)?$/i;

export default function remarkMarkdownLinks({ docsDir, base }) {
  const prefix = base.replace(/\/$/, '');
  return (tree, file) => {
    const page = file.path ?? file.history?.[0];
    if (!page || !page.startsWith(docsDir)) return;
    const fromDir = path.relative(docsDir, path.dirname(page)).split(path.sep).join('/');

    const visit = (node) => {
      if (node.type === 'link') {
        const match = pageLink.exec(node.url);
        if (match) {
          const target = path.posix.normalize(path.posix.join(fromDir, match[1]));
          if (!target.startsWith('..')) {
            const slug = target === 'index' ? '' : `${target.replace(/\/index$/, '').toLowerCase()}/`;
            node.url = `${prefix}/${slug}${match[2] ?? ''}`;
          }
        }
      }
      node.children?.forEach(visit);
    };
    visit(tree);
  };
}
