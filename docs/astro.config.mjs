import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: 'https://marcomaiermm.github.io',
	base: '/DiffHarbor',
	integrations: [
		starlight({
			title: 'DiffHarbor',
			description: 'Self-hosted AI code review for GitHub and GitLab.',
			logo: { src: './src/assets/logo.png' },
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/marcomaiermm/DiffHarbor' }],
			editLink: { baseUrl: 'https://github.com/marcomaiermm/DiffHarbor/edit/main/docs/' },
			sidebar: [
				{ label: 'Start', items: ['quick-start', 'how-it-works'] },
				{ label: 'Setup', items: ['configuration', 'per-repo', 'security'] },
				{ label: 'More', items: ['development'] },
			],
		}),
	],
});
