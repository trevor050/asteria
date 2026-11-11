<script lang="ts">
  // A skill's icon: its own Lucide pictogram, or its family's, drawn thin and
  // tinted quietly by family so a long list can be scanned by shape and hue.
  import { familyIcons, icons } from './icons'
  import { tintFamily } from './families'

  type Props = { name?: string; family?: string; size?: number; tint?: boolean }
  let { name = '', family = '', size = 16, tint = true }: Props = $props()

  let key = $derived(icons[name] ? name : (familyIcons[family] ?? 'file'))
  let fam = $derived(familyIcons[family] ? (tintFamily[family] ?? family) : 'meta')
</script>

<svg
  class="icon"
  class:tint
  data-family={fam}
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="1.6"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true">{@html icons[key] ?? ''}</svg
>

<style>
  .icon {
    flex: none;
    display: inline-block;
    vertical-align: -0.2em;
  }
  .tint {
    color: var(--fam, var(--muted));
  }
  .tint[data-family='convert'] {
    --fam: var(--fam-convert);
  }
  .tint[data-family='transform'] {
    --fam: var(--fam-transform);
  }
  .tint[data-family='sizes'] {
    --fam: var(--fam-sizes);
  }
  .tint[data-family='compress'] {
    --fam: var(--fam-compress);
  }
  .tint[data-family='adjust'] {
    --fam: var(--fam-adjust);
  }
  .tint[data-family='filter'] {
    --fam: var(--fam-filter);
  }
  .tint[data-family='compose'] {
    --fam: var(--fam-compose);
  }
  .tint[data-family='info'] {
    --fam: var(--fam-info);
  }
  .tint[data-family='pdf'] {
    --fam: var(--fam-pdf);
  }
  .tint[data-family='media'] {
    --fam: var(--fam-media);
  }
  .tint[data-family='archive'] {
    --fam: var(--fam-archive);
  }
  .tint[data-family='data'] {
    --fam: var(--fam-data);
  }
  .tint[data-family='document'] {
    --fam: var(--fam-document);
  }
</style>
