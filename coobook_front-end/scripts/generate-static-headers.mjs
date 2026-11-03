import { readFile, writeFile } from 'node:fs/promises'
import { join, relative, resolve, sep } from 'node:path'

const dist = resolve(process.argv[2] ?? 'dist')
const pages = [
  { file: 'index.html', route: '/' },
  { file: 'auth/confirm.html', route: '/auth/confirm' },
  { file: 'auth/enroll.html', route: '/auth/enroll' },
  { file: 'account/security.html', route: '/account/security' },
  { file: 'admin/settings.html', route: '/admin/settings' },
  { file: 'library.html', route: '/library' },
  { file: 'recipe.html', route: '/recipe' },
]
const defaults = {
  'default-src': ["'self'"],
  'object-src': ["'none'"],
  'base-uri': ["'self'"],
  'form-action': ["'self'"],
  'frame-ancestors': ["'none'"],
  'img-src': ["'self'", 'data:'],
  'font-src': ["'self'"],
  'connect-src': ["'self'"],
}
const allowed = new Set([...Object.keys(defaults), 'script-src', 'style-src'])
const hashPattern = /^'sha256-([A-Za-z0-9+/]{43}=)'$/

function fail(message) {
  throw new Error(`static header generation: ${message}`)
}

function parsePolicy(html, file) {
  if (/\son[a-z]+\s*=/i.test(html) || /\b(?:href|src)\s*=\s*["']\s*javascript:/i.test(html)) {
    fail(`${file} contains an inline event handler or javascript: URL`)
  }
  const metas = [...html.matchAll(/<meta\b(?=[^>]*\bhttp-equiv\s*=\s*(["'])Content-Security-Policy\1)[^>]*>/gi)]
  if (metas.length !== 1) fail(`${file} must contain exactly one CSP meta policy (found ${metas.length})`)
  const content = metas[0][0].match(/\bcontent\s*=\s*(["'])(.*?)\1/i)?.[2]
  if (!content) fail(`${file} CSP meta has no content`)
  const policy = new Map()
  for (const part of content.split(';')) {
    const tokens = part.trim().split(/\s+/).filter(Boolean)
    if (!tokens.length) continue
    const directive = tokens.shift().toLowerCase()
    if (!allowed.has(directive)) fail(`${file} contains unsupported directive ${directive}`)
    if (policy.has(directive)) fail(`${file} repeats directive ${directive}`)
    if (tokens.some((source) => /unsafe-inline|unsafe-eval/i.test(source))) fail(`${file} contains unsafe CSP source`)
    if (directive === 'script-src' || directive === 'style-src') {
      const hashes = tokens.filter((source) => source.startsWith("'sha256-"))
      if (!hashes.length) fail(`${file} ${directive} must contain an Astro SHA-256 hash`)
      for (const hash of hashes) {
        const match = hash.match(hashPattern)
        if (!match || Buffer.from(match[1], 'base64').length !== 32) fail(`${file} has malformed SHA-256 source ${hash}`)
      }
      if (tokens.some((source) => source !== "'self'" && !hashPattern.test(source))) {
        fail(`${file} ${directive} contains a source outside 'self' and SHA-256 hashes`)
      }
    } else if (tokens.some((source) => !["'self'", "'none'", 'data:'].includes(source))) {
      fail(`${file} ${directive} contains an unapproved source`)
    }
    policy.set(directive, tokens)
  }
  for (const name of ['script-src', 'style-src']) if (!policy.has(name)) fail(`${file} is missing ${name}`)
  for (const [name, values] of Object.entries(defaults)) {
    const supplied = policy.get(name)
    if (supplied && (supplied.length !== values.length || supplied.some((value, index) => value !== values[index]))) {
      fail(`${file} ${name} must retain the approved restrictive default`)
    }
  }
  const merged = new Map(Object.entries(defaults))
  merged.set('script-src', policy.get('script-src'))
  merged.set('style-src', policy.get('style-src'))
  return [...merged].map(([name, values]) => `${name} ${values.join(' ')}`).join('; ')
}

const records = []
for (const { file, route } of pages) {
  let html
  try { html = await readFile(join(dist, file), 'utf8') } catch { fail(`missing built page ${file}`) }
  const policy = parsePolicy(html, file)
  records.push({ file, route, policy })
}

const headers = [
  ...records.flatMap(({ route, file, policy }) => [
    route, `  Content-Security-Policy: ${policy}`,
    ...(file !== 'index.html' ? [`/${file}`, `  Content-Security-Policy: ${policy}`] : []),
  ]),
  '/*',
  '  X-Content-Type-Options: nosniff',
  '  Referrer-Policy: no-referrer',
  ...records.flatMap(({ route, file }) => {
    const htmlPath = `/${file}`
    const aliases = route === '/' ? ['/', htmlPath] : [route, htmlPath]
    return [...new Set(aliases)].flatMap((path) => [path, '  Cache-Control: no-store'])
  }),
  '/_astro/*',
  '  Cache-Control: public, max-age=31536000, immutable',
  '',
].join('\n')
await writeFile(join(dist, '_headers'), headers)
await writeFile(join(dist, 'static-csp.json'), `${JSON.stringify(records, null, 2)}\n`)
const caddy = records.map(({ route, file, policy }, index) => {
  const paths = `${route} /${file}`
  return `@document${index} path ${paths}\nheader @document${index} Content-Security-Policy "${policy}"`
}).join('\n\n') + '\n'
const htmlPaths = [...new Set(records.flatMap(({ route, file }) => route === '/' ? ['/', `/${file}`] : [route, `/${file}`]))]
const cacheCaddy = `@html path ${htmlPaths.join(' ')}\nheader @html Cache-Control "no-store"\n`
await writeFile(join(dist, 'static-csp.caddy'), `${caddy}\n${cacheCaddy}`)
console.log(`Generated static headers for ${records.length} HTML pages in ${relative(process.cwd(), dist).split(sep).join('/')}`)
