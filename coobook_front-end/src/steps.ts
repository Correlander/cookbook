export interface Step {
  id: string
  text: string
}

export function createStep(text = ''): Step {
  return { id: globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random()}`, text }
}

export function moveStep<T>(steps: T[], index: number, offset: number): T[] {
  const target = index + offset
  if (index < 0 || target < 0 || index >= steps.length || target >= steps.length) return steps

  const ordered = [...steps]
  ;[ordered[index], ordered[target]] = [ordered[target], ordered[index]]
  return ordered
}
