<script lang="ts">
  import SchedulePicker from "./SchedulePicker.svelte";
  import { untrack } from "svelte";
  import { defaultScheduleConfig, type ScheduleConfig } from "../lib/scheduleConfig";
  let { initial = defaultScheduleConfig(), disabled = false }: { initial?: ScheduleConfig; disabled?: boolean } = $props();
  let value = $state<ScheduleConfig>(untrack(() => initial));
</script>

<SchedulePicker bind:value {disabled} />
<output aria-label="Schedule state">{JSON.stringify(value)}</output>
