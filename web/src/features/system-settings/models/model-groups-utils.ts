export function splitMembers(membersText: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const part of membersText.split(/[\n,，、]/)) {
    const model = part.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    out.push(model)
  }
  return out
}

export function appendGroupMembers(
  current: string,
  additions: string[]
): string {
  return [...new Set([...splitMembers(current), ...additions])].join(', ')
}

export function removeUnavailableMembers(
  current: string,
  available: Set<string>
): string {
  return splitMembers(current)
    .filter((model) => available.has(model))
    .join(', ')
}
