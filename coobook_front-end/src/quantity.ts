const leadingQuantity = /^(\s*)(\d+\s+\d+\/\d+|\d+\/\d+|\d+(?:\.\d+)?)(.*)$/

function parseQuantity(value: string): number {
  const parts = value.trim().split(/\s+/)
  const parseFraction = (fraction: string): number => {
    const [numerator, denominator] = fraction.split('/').map(Number)
    return denominator ? numerator / denominator : Number.NaN
  }

  if (parts.length === 2 && parts[1].includes('/')) {
    return Number(parts[0]) + parseFraction(parts[1])
  }
  return parts[0].includes('/') ? parseFraction(parts[0]) : Number(parts[0])
}

function formatQuantity(value: number): string {
  return String(Number(value.toFixed(3)))
}

/** Scale a simple leading numeric amount while leaving prose and ranges intact. */
export function scaleQuantity(quantity: string, factor: number): string {
  if (!quantity || !Number.isFinite(factor) || factor <= 0 || factor === 1) return quantity

  const match = quantity.match(leadingQuantity)
  if (!match) return quantity

  const [, whitespace, amount, suffix] = match
  if (/^\s*(?:[-–—]\s*\d|or\s+\d)/i.test(suffix)) return quantity

  const scaled = parseQuantity(amount) * factor
  if (!Number.isFinite(scaled)) return quantity
  return `${whitespace}${formatQuantity(scaled)}${suffix}`
}
