import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

// The pages live in the repository's docs/ directory, not in src/content/docs/, so they stay
// readable on GitHub at the same paths. The pattern is the one Starlight's own docsLoader uses.
export const collections = {
  docs: defineCollection({
    loader: glob({ base: '../docs', pattern: '**/[^_]*.{md,mdx}' }),
    schema: docsSchema(),
  }),
};
