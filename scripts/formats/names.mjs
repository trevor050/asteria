// Human-readable names for file types, used by docs/formats.json and the
// README. Keys are the extension without the dot. Keep names short and in
// the words people use ("Word document", not "Office Open XML").
export const names = {
  // Images
  jpg: 'JPEG image', jpeg: 'JPEG image', png: 'PNG image', gif: 'GIF image', webp: 'WebP image',
  heic: 'HEIC photo (iPhone)', heif: 'HEIF photo', avif: 'AVIF image', bmp: 'Bitmap image', tif: 'TIFF image',
  tiff: 'TIFF image', ico: 'Windows icon', cur: 'Windows cursor', psd: 'Photoshop document', tga: 'TGA image',
  pcx: 'PCX image', jp2: 'JPEG 2000 image', j2k: 'JPEG 2000 image', jxl: 'JPEG XL image', dds: 'DirectDraw texture',
  xpm: 'X pixmap', xbm: 'X bitmap', ppm: 'PPM image', pgm: 'PGM image', pbm: 'PBM image', pnm: 'PNM image',
  sgi: 'SGI image', ras: 'Sun raster image', exr: 'OpenEXR image', hdr: 'Radiance HDR image',
  dng: 'Adobe DNG raw photo', cr2: 'Canon raw photo', cr3: 'Canon raw photo', nef: 'Nikon raw photo',
  arw: 'Sony raw photo', orf: 'Olympus raw photo', rw2: 'Panasonic raw photo', pef: 'Pentax raw photo',
  raf: 'Fujifilm raw photo', srw: 'Samsung raw photo', x3f: 'Sigma raw photo',
  svg: 'SVG vector image', eps: 'EPS vector file', ps: 'PostScript file',
  cube: 'Cube color LUT', '3dl': '3DL color LUT',
  // Documents
  pdf: 'PDF document', doc: 'Word 97-2003 document', docx: 'Word document', odt: 'OpenDocument text',
  rtf: 'Rich Text document', txt: 'Plain text', md: 'Markdown', markdown: 'Markdown', html: 'Web page',
  htm: 'Web page', rst: 'reStructuredText', tex: 'LaTeX document', org: 'Org-mode document',
  textile: 'Textile document', ipynb: 'Jupyter notebook', wpd: 'WordPerfect document', wps: 'Works document',
  xls: 'Excel 97-2003 workbook', xlsx: 'Excel workbook', ods: 'OpenDocument spreadsheet',
  ppt: 'PowerPoint 97-2003 presentation', pptx: 'PowerPoint presentation', odp: 'OpenDocument presentation',
  // Ebooks
  epub: 'EPUB ebook', mobi: 'Kindle ebook (MOBI)', azw: 'Kindle ebook (AZW)', azw3: 'Kindle ebook (AZW3)',
  fb2: 'FictionBook ebook', lit: 'Microsoft Reader ebook', pdb: 'Palm ebook',
  // Audio and video
  mp4: 'MP4 video', mov: 'QuickTime video', m4v: 'M4V video', mkv: 'Matroska video', webm: 'WebM video',
  avi: 'AVI video', wmv: 'Windows Media video', flv: 'Flash video', mpg: 'MPEG video', mpeg: 'MPEG video',
  '3gp': '3GP video', mp3: 'MP3 audio', wav: 'WAV audio', m4a: 'M4A audio', aac: 'AAC audio',
  flac: 'FLAC audio', ogg: 'Ogg audio', opus: 'Opus audio', wma: 'Windows Media audio', aiff: 'AIFF audio',
  aif: 'AIFF audio',
  // Subtitles
  srt: 'SubRip subtitles', vtt: 'WebVTT subtitles', ass: 'Advanced SubStation subtitles',
  ssa: 'SubStation subtitles', sbv: 'YouTube subtitles', ttml: 'TTML subtitles', dfxp: 'DFXP subtitles',
  // Data and config
  csv: 'CSV spreadsheet', tsv: 'TSV spreadsheet', json: 'JSON data', jsonl: 'JSON Lines', ndjson: 'JSON Lines',
  yaml: 'YAML config', yml: 'YAML config', toml: 'TOML config', xml: 'XML document', ini: 'INI config',
  cfg: 'Config file', conf: 'Config file', env: '.env file', properties: 'Java properties',
  plist: 'Apple property list', sqlite: 'SQLite database', sqlite3: 'SQLite database', db: 'SQLite database',
  parquet: 'Parquet data', sql: 'SQL script', log: 'Log file',
  b64: 'Base64 text', base64: 'Base64 text', bin: 'Binary file',
  // Code (text tools only)
  js: 'JavaScript', ts: 'TypeScript', css: 'CSS stylesheet', go: 'Go source', py: 'Python script',
  java: 'Java source', c: 'C source', h: 'C header', cpp: 'C++ source', cs: 'C# source', rb: 'Ruby script',
  php: 'PHP script', sh: 'Shell script', bat: 'Batch file', ps1: 'PowerShell script',
  // Email
  eml: 'Email message', mbox: 'Mailbox', mbx: 'Mailbox',
  // Fonts
  ttf: 'TrueType font', otf: 'OpenType font', woff: 'WOFF web font', woff2: 'WOFF2 web font',
  // Maps
  geojson: 'GeoJSON map data', kml: 'Google Earth KML', kmz: 'Google Earth KMZ', gpx: 'GPS track (GPX)',
  // 3D
  obj: 'OBJ 3D model', stl: 'STL 3D model', ply: 'PLY 3D model', gltf: 'glTF 3D model', glb: 'GLB 3D model',
  '3mf': '3MF 3D print file',
  // Archives
  zip: 'ZIP archive', '7z': '7-Zip archive', rar: 'RAR archive', tar: 'Tar archive', gz: 'Gzip file',
  tgz: 'Gzipped tar archive', 'tar.gz': 'Gzipped tar archive', xz: 'XZ file', txz: 'XZ tar archive',
  'tar.xz': 'XZ tar archive', zst: 'Zstandard file', tzst: 'Zstandard tar archive',
  'tar.zst': 'Zstandard tar archive', bz2: 'Bzip2 file', tbz: 'Bzip2 tar archive', tbz2: 'Bzip2 tar archive',
  'tar.bz2': 'Bzip2 tar archive', iso: 'Disc image (ISO)', cab: 'Windows cabinet', wim: 'Windows image (WIM)',
  dmg: 'macOS disk image', xar: 'XAR archive', cpio: 'CPIO archive', rpm: 'RPM package', deb: 'Debian package',
  arj: 'ARJ archive', lzh: 'LZH archive', lzma: 'LZMA file', z: 'Unix compressed file',
  // Special
  folder: 'Folder of files', '*': 'Any file',
}

export function nameOf(ext) {
  const key = ext === 'folder' || ext === '*' ? ext : ext.replace(/^\./, '').toLowerCase()
  return names[key] ?? key.toUpperCase() + ' file'
}
