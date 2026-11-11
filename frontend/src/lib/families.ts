// Skill categories as people think of them, in the order the ranker uses.
export const families: [string, string][] = [
  ['convert', 'Convert'],
  ['transform', 'Size & shape'],
  ['sizes', 'Social & wallpaper'],
  ['compress', 'Compress'],
  ['adjust', 'Color & tone'],
  ['filter', 'Effects'],
  ['compose', 'Compose'],
  ['info', 'Info & cleanup'],
  ['pdf', 'PDF'],
  ['media', 'Audio & video'],
  ['archive', 'Archives'],
  ['data', 'Data'],
  ['meta', 'App'],
]

export function familyName(category: string): string {
  return families.find(([k]) => k === category)?.[1] ?? (category ? category[0].toUpperCase() + category.slice(1) : 'Other')
}

export function familyRank(category: string): number {
  const i = families.findIndex(([k]) => k === category)
  return i < 0 ? families.length : i
}
