<script lang="ts">
  // Renders the first page of a PDF with pdf.js, loaded only when needed.
  // Falls back to a quiet type card if pdf.js cannot load or render.
  type Props = { url: string; maxWidth: number; maxHeight: number; pages?: number }
  let { url, maxWidth, maxHeight, pages = 0 }: Props = $props()

  let canvas: HTMLCanvasElement | undefined = $state()
  let failed = $state(false)
  let ready = $state(false)

  $effect(() => {
    const target = canvas
    const w = maxWidth
    const h = maxHeight
    const src = url
    if (!target || w < 40 || h < 40) return
    let cancelled = false
    let task: { cancel: () => void } | undefined
    ;(async () => {
      try {
        const pdfjs = await import('pdfjs-dist')
        const worker = (await import('pdfjs-dist/build/pdf.worker.min.mjs?url')).default
        pdfjs.GlobalWorkerOptions.workerSrc = worker
        const loading = pdfjs.getDocument({ url: src })
        const doc = await loading.promise
        const page = await doc.getPage(1)
        if (cancelled) return
        const base = page.getViewport({ scale: 1 })
        const scale = Math.min(w / base.width, h / base.height)
        const dpr = Math.min(2, window.devicePixelRatio || 1)
        const vp = page.getViewport({ scale: scale * dpr })
        target.width = Math.floor(vp.width)
        target.height = Math.floor(vp.height)
        target.style.width = `${Math.floor(vp.width / dpr)}px`
        target.style.height = `${Math.floor(vp.height / dpr)}px`
        const render = page.render({ canvas: target, viewport: vp })
        task = render
        await render.promise
        if (!cancelled) ready = true
        void loading.destroy()
      } catch {
        if (!cancelled) failed = true
      }
    })()
    return () => {
      cancelled = true
      task?.cancel()
    }
  })
</script>

{#if failed}
  <div class="card" style={`width:${Math.min(maxWidth, maxHeight / 1.294)}px;height:${Math.min(maxHeight, maxWidth * 1.294)}px`}>
    <span class="ext">PDF</span>
    {#if pages}<span class="sum">{pages} {pages === 1 ? 'page' : 'pages'}</span>{/if}
  </div>
{:else}
  <canvas bind:this={canvas} class:ready aria-label="First page"></canvas>
{/if}

<style>
  canvas {
    display: block;
    background: #fff;
    border-radius: 4px;
    box-shadow:
      0 0 0 1px rgb(0 0 0 / 0.06),
      0 18px 40px -18px rgb(0 0 0 / 0.5);
    opacity: 0;
    transition: opacity 0.3s var(--ease-out);
  }
  canvas.ready {
    opacity: 1;
  }
  .card {
    display: grid;
    place-content: center;
    gap: 4px;
    text-align: center;
    border-radius: 12px;
    background: var(--well);
    box-shadow: inset 0 0 0 1px var(--line);
  }
  .ext {
    font-size: 34px;
    font-weight: 200;
    letter-spacing: 0.06em;
  }
  .sum {
    font-size: 13px;
    color: var(--muted);
  }
</style>
