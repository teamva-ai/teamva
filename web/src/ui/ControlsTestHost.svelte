<script lang="ts">
  import { Input, Select, Textarea, Checkbox, Switch, DatePicker, Button, Field, ModelPicker } from "./index";
  let { disabled = false }: { disabled?: boolean } = $props();
  let text = $state("initial");
  let count = $state<number | null>(7);
  let selected = $state("one");
  let numeric = $state(7);
  let note = $state("notes");
  let checked = $state(false);
  let enabled = $state(false);
  let date = $state("2040-01-02");
  let iso = $state("2040-01-02T03:04:05.123Z");
  let model = $state("model-20");
  let inputRef = $state<HTMLInputElement>();
  let submissions = $state(0);
  let changes = $state(0);
</script>

<form aria-label="Primitive form" onsubmit={(event) => { event.preventDefault(); submissions++; }}>
  <fieldset {disabled}>
    <Field label="Text field"><Input name="text" defaultValue="initial" bind:value={text} bind:ref={inputRef} required maxlength={100} /></Field>
    <Field label="Count"><Input type="number" bind:value={count} min="0" max="100" step="1" /></Field>
    <Field label="Choice"><Select bind:value={selected} onchange={() => { changes++; }}><option value="one">One</option><option value="two">Two</option></Select></Field>
    <Field label="Numeric choice"><Select bind:value={numeric}><option value={7}>Seven</option><option value={8}>Eight</option></Select></Field>
    <Field label="Notes"><Textarea bind:value={note} rows={3} /></Field>
    <Field label="Composite field" hint="Primary control description">
      <Input value="primary" />
      <label><Checkbox inheritField={false} />Auxiliary option</label>
    </Field>
    <label><Checkbox bind:checked />Check choice</label>
    <label><Switch bind:checked={enabled} />Switch choice</label>
    <Field label="Date"><DatePicker bind:value={date} /></Field>
    <Field label="ISO time"><DatePicker type="datetime-local" iso defaultValue="2040-01-02T03:04:05.123Z" bind:value={iso} /></Field>
    <ModelPicker chips models={Array.from({length:40}, (_, index) => `model-${index}`)} bind:value={model} label="Model selection" />
    <Button type="submit" variant="primary">Submit</Button>
    <Button type="reset">Reset</Button>
    <Button onclick={() => inputRef?.focus()}>Focus text</Button>
  </fieldset>
</form>
<output aria-label="State">{JSON.stringify({ text, count, selected, numeric, note, checked, enabled, date, iso, model, submissions, changes })}</output>
