<script lang="ts">
  import { localeInfo, t } from "../lib/i18n.svelte";
  import { defaultScheduleConfig, fromZonedLocal, toZonedLocal, validTimezone, type ScheduleConfig } from "../lib/scheduleConfig";
  import Input from "./Input.svelte";
  import Select from "./Select.svelte";
  import DatePicker from "./DatePicker.svelte";
  import Button from "./Button.svelte";
  import Checkbox from "./Checkbox.svelte";
  import Field from "./Field.svelte";

  let { value = $bindable<ScheduleConfig>(defaultScheduleConfig()), disabled = false }: { value?: ScheduleConfig; disabled?: boolean } = $props();
  const id = $props.id();
  let expressions = $state<Partial<Record<ScheduleConfig["kind"], string>>>({});
  let zoneDraft = $state(value.timezone);
  let invalidWall = $state("");
  $effect(() => { zoneDraft = value.timezone; });
  const weekdays = [1, 2, 3, 4, 5, 6, 0];
  const supportedZones = (() => {
    try { return Intl.supportedValuesOf?.("timeZone") ?? []; } catch { return []; }
  })();
  const zones = $derived([...new Set(["UTC", ...supportedZones, value.timezone])]);
  const presets = ["0 * * * *", "0 9 * * *", "0 9 * * 1-5", "0 9 1 * *"];
  const dayLabel = (day: number) => new Intl.DateTimeFormat(localeInfo.value, { weekday: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2024, 0, 7 + day)));

  function setKind(kind: ScheduleConfig["kind"]) {
    expressions[value.kind] = value.expression;
    value = { ...value, kind, expression: expressions[kind] ?? "" };
    invalidWall = "";
  }
  function setTime(index: number, time: string) {
    value = { ...value, times: value.times.map((item, i) => i === index ? time : item) };
  }
  function setDay(day: number, checked: boolean) {
    value = { ...value, weekdays: checked ? [...new Set([...value.weekdays, day])] : value.weekdays.filter((item) => item !== day) };
  }
  function setWall(wall: string) {
    const expression = fromZonedLocal(wall, value.timezone);
    invalidWall = expression ? "" : wall;
    value = { ...value, expression };
  }
  function setZone() {
    const timezone = zoneDraft.trim();
    if (timezone === value.timezone) return;
    const wall = invalidWall || toZonedLocal(value.expression, value.timezone);
    value = { ...value, timezone, expression: value.kind === "once" && wall && validTimezone(timezone) ? fromZonedLocal(wall, timezone) : value.expression };
    invalidWall = value.kind === "once" && wall && (!value.expression || !validTimezone(timezone)) ? wall : "";
  }
</script>

<fieldset {disabled} class="grid min-w-0 gap-4">
  <Field label={t("schedules.scheduleKind")}>
    <Select value={value.kind} onchange={(event) => setKind(event.currentTarget.value as ScheduleConfig["kind"])}>
      <option value="daily">{t("scheduleEditor.daily")}</option>
      <option value="weekly">{t("scheduleEditor.weekly")}</option>
      <option value="once">{t("schedules.once")}</option>
      <option value="cron">{t("schedules.cron")}</option>
    </Select>
  </Field>

  {#if value.kind === "daily" || value.kind === "weekly"}
    <fieldset class="grid min-w-0 gap-2">
      <legend class="mb-2 text-xs font-medium text-secondary">{t("scheduleEditor.times")}</legend>
      <div class="flex flex-wrap gap-3">
      {#each value.times as time, index}
        <div class="flex min-w-0 items-end gap-2">
          <div class="w-40 min-w-0">
            <Field label={t("scheduleEditor.time", { index: index + 1 })}>
              <DatePicker type="time" step="60" required bind:value={() => value.times[index], (next) => setTime(index, next)} />
            </Field>
          </div>
          <Button disabled={value.times.length <= 1} aria-label={t("scheduleEditor.removeTime", { index: index + 1 })} onclick={() => { value = { ...value, times: value.times.filter((_, i) => i !== index) }; }}>−</Button>
        </div>
      {/each}
      </div>
      <div><Button disabled={value.times.length >= 64} onclick={() => { value = { ...value, times: [...value.times, "09:00"] }; }}>{t("scheduleEditor.addTime")}</Button></div>
    </fieldset>
  {/if}

  {#if value.kind === "weekly"}
    <fieldset class="grid min-w-0 gap-2" aria-describedby={value.weekdays.length ? undefined : `${id}-weekday-error`}>
      <legend class="mb-2 text-xs font-medium text-secondary">{t("scheduleEditor.weekdays")}</legend>
      <div class="flex flex-wrap gap-x-4 gap-y-2">
        {#each weekdays as day}
          <label class="flex items-center gap-2 text-sm"><Checkbox inheritField={false} bind:checked={() => value.weekdays.includes(day), (checked) => setDay(day, checked)} />{dayLabel(day)}</label>
        {/each}
      </div>
      {#if !value.weekdays.length}<small id={`${id}-weekday-error`} class="text-xs text-danger">{t("scheduleEditor.chooseWeekday")}</small>{/if}
    </fieldset>
  {:else if value.kind === "once"}
    <Field label={t("scheduleEditor.dateTime")}>
      <DatePicker type="datetime-local" step="any" required aria-invalid={!value.expression || undefined} aria-describedby={!value.expression ? `${id}-date-error` : undefined} bind:value={() => invalidWall || toZonedLocal(value.expression, value.timezone), setWall} />
      {#if !value.expression}<small id={`${id}-date-error`} class="text-xs text-danger">{t("scheduleEditor.invalidDate")}</small>{/if}
    </Field>
  {:else if value.kind === "cron"}
    <Field label={t("schedules.cronExpression")}>
      <Input bind:value={value.expression} required spellcheck="false" placeholder="0 9 * * *" />
      <div class="flex flex-wrap gap-2" role="group" aria-label={t("scheduleEditor.cronPreset")}>
        {#each presets as preset}<Button controlSize="sm" onclick={() => { value = { ...value, expression: preset }; }}>{preset}</Button>{/each}
      </div>
    </Field>
  {/if}

  <Field label={t("schedules.timezone")} hint={t("scheduleEditor.timezoneHint", { timezone: value.timezone })}>
    <Input bind:value={zoneDraft} oninput={(event) => { zoneDraft = event.currentTarget.value; setZone(); }} onchange={(event) => { zoneDraft = event.currentTarget.value; setZone(); }} list={`${id}-zones`} required spellcheck="false" aria-invalid={!validTimezone(zoneDraft) || undefined} />
    <datalist id={`${id}-zones`}>{#each zones as timezone}<option value={timezone}></option>{/each}</datalist>
  </Field>
  {#if value.kind === "daily"}
    <p class="text-xs text-muted">{t("scheduleEditor.summaryDaily", { times: value.times.join(", "), timezone: value.timezone })}</p>
  {:else if value.kind === "weekly"}
    <p class="text-xs text-muted">{t("scheduleEditor.summaryWeekly", { weekdays: weekdays.filter((day) => value.weekdays.includes(day)).map(dayLabel).join(", "), times: value.times.join(", "), timezone: value.timezone })}</p>
  {/if}
</fieldset>
