
<script lang="ts">
  import { Button } from "../ui";
  import Modal from "../ui/Modal.svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import { t } from "../lib/i18n.svelte";

  let { title, description, confirmLabel, danger = true, busy = false, error, onConfirm, onCancel }: {
    title: string;
    description: string;
    confirmLabel: string;
    danger?: boolean;
    busy?: boolean;
    error?: unknown;
    onConfirm: () => void;
    onCancel: () => void;
  } = $props();

      </script>

<Modal {title} {description} onClose={() => { if (!busy) onCancel(); }}>
  {#if error}<ErrorNotice {error} />{/if}
  <fieldset disabled={busy} class="flex justify-end gap-2 disabled:opacity-60">
    <Button type="button" onclick={onCancel}>{t("common.cancel")}</Button>
    <Button variant={danger ? "danger" : "primary"} type="button" onclick={onConfirm}>
      {confirmLabel}
    </Button>
  </fieldset>
</Modal>
