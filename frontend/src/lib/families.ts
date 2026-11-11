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
  ['document', 'Documents'],
  ['ebook', 'Ebooks'],
  ['media', 'Audio & video'],
  ['subtitles', 'Subtitles'],
  ['camera', 'Camera & pro images'],
  ['vector', 'Vector'],
  ['lut', 'Color LUTs'],
  ['fonts', 'Fonts'],
  ['3d', '3D models'],
  ['maps', 'Maps & GPS'],
  ['email', 'Email'],
  ['text', 'Text & encoding'],
  ['archive', 'Archives'],
  ['data', 'Data'],
  ['meta', 'App'],
]

// Newer families share a related family's tint so the list stays calm.
export const tintFamily: Record<string, string> = {
  document: 'document',
  ebook: 'document',
  text: 'document',
  fonts: 'document',
  subtitles: 'media',
  email: 'info',
  camera: 'convert',
  vector: 'compose',
  lut: 'adjust',
  maps: 'transform',
  '3d': 'transform',
}

export function familyName(category: string): string {
  return families.find(([k]) => k === category)?.[1] ?? (category ? category[0].toUpperCase() + category.slice(1) : 'Other')
}

export function familyRank(category: string): number {
  const i = families.findIndex(([k]) => k === category)
  return i < 0 ? families.length : i
}
