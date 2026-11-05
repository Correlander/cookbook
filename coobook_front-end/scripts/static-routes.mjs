// Static HTML routes are an explicit security and serving contract. Keep this
// list aligned with src/pages; static-routes.test.mjs enforces that coverage.
export const staticRoutes = [
  { source: 'index.astro', file: 'index.html', route: '/', aliases: ['/', '/index.html'] },
  { source: 'auth/confirm.astro', file: 'auth/confirm.html', route: '/auth/confirm', aliases: ['/auth/confirm', '/auth/confirm.html'] },
  { source: 'auth/enroll.astro', file: 'auth/enroll.html', route: '/auth/enroll', aliases: ['/auth/enroll', '/auth/enroll.html'] },
  { source: 'account.astro', file: 'account.html', route: '/account', aliases: ['/account', '/account.html'] },
  { source: 'account/security.astro', file: 'account/security.html', route: '/account/security', aliases: ['/account/security', '/account/security.html'] },
  { source: 'account/creator-profile.astro', file: 'account/creator-profile.html', route: '/account/creator-profile', aliases: ['/account/creator-profile', '/account/creator-profile.html'] },
  { source: 'creator.astro', file: 'creator.html', route: '/creator', aliases: ['/creator', '/creator.html'] },
  { source: 'admin.astro', file: 'admin.html', route: '/admin', aliases: ['/admin', '/admin.html'] },
  { source: 'admin/settings.astro', file: 'admin/settings.html', route: '/admin/settings', aliases: ['/admin/settings', '/admin/settings.html'] },
  { source: 'library.astro', file: 'library.html', route: '/library', aliases: ['/library', '/library.html'] },
  { source: 'recipe.astro', file: 'recipe.html', route: '/recipe', aliases: ['/recipe', '/recipe.html'] },
  { source: 'cookbooks.astro', file: 'cookbooks.html', route: '/cookbooks', aliases: ['/cookbooks', '/cookbooks.html'] },
  { source: 'cookbook.astro', file: 'cookbook.html', route: '/cookbook', aliases: ['/cookbook', '/cookbook.html'] },
  { source: 'course.astro', file: 'course.html', route: '/course', aliases: ['/course', '/course.html'] },
  { source: 'menu.astro', file: 'menu.html', route: '/menu', aliases: ['/menu', '/menu.html'] },
  { source: 'discover.astro', file: 'discover.html', route: '/discover', aliases: ['/discover', '/discover.html'] },
  { source: 'create.astro', file: 'create.html', route: '/create', aliases: ['/create', '/create.html'] },
]
