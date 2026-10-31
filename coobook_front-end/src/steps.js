export function createStep(text = '') {
  return { id: globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`, text }
}

export function moveStep(steps, index, offset) {
  const target = index + offset
  if (index < 0 || target < 0 || index >= steps.length || target >= steps.length) return steps

  const ordered = [...steps]
  ;[ordered[index], ordered[target]] = [ordered[target], ordered[index]]
  return ordered
}
