<script lang="ts">
  import { Button, Field, FieldGroup, Input, ModelPicker, Select } from "./index";
  let { disabled = false, columns, collapseAt, hint = "Name hint", showActions = false }: {
    disabled?: boolean; columns?: 1 | 2 | 3; collapseAt?: "sm" | "md" | "lg"; hint?: string; showActions?: boolean;
  } = $props();
</script>

{#snippet actionContent()}<Button type="button">Apply fields</Button>{/snippet}
<fieldset {disabled} aria-label="Grouped controls">
  <FieldGroup {columns} {collapseAt} actions={showActions ? actionContent : undefined} data-testid="field-group" class="host-group" aria-label="Configuration">
    <Field label="Name" id="group-name" {hint}><Input value="Example" /></Field>
    <Field label="Provider"><Select value="ready"><option value="ready">Ready</option></Select></Field>
    <Field label="Model" hint="Choose or enter a model">
      <ModelPicker models={["model-one", "model-two"]} value="model-one" />
    </Field>
  </FieldGroup>
  <Button>Outside group</Button>
</fieldset>
