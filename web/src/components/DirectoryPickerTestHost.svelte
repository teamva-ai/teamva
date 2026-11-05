<script lang="ts">
  import { Field } from "../ui";
  import DirectoryPicker from "./DirectoryPicker.svelte";
  let { onSelect }: { onSelect: (path: string) => void } = $props();
</script>

<Field label="Directory" hint="Choose a local directory" id="directory-path">
  <DirectoryPicker computerId="computer-picker-test" {onSelect} />
</Field>
