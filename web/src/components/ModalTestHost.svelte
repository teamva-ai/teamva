<script lang="ts">
  import Modal from "../ui/Modal.svelte";

  let open = $state(false);
  let nestedOpen = $state(false);
  let closeCount = $state(0);
  let nestedCloseCount = $state(0);

  function close() {
    closeCount += 1;
    open = false;
  }

  function closeNested() {
    nestedCloseCount += 1;
    nestedOpen = false;
  }
</script>

<button type="button" data-testid="modal-opener" onclick={() => (open = true)}>Open repository selector</button>
<output data-testid="modal-close-count">{closeCount}</output>
<output data-testid="nested-modal-close-count">{nestedCloseCount}</output>

{#if open}
  <Modal title="Choose repository" description="Select one repository" onClose={close} layer={90}>
    <label>
      Repository
      <select aria-label="Repository">
        <option>Frontend</option>
        <option>Backend</option>
      </select>
    </label>
    <button type="button" onclick={close}>Done</button>
    <button type="button" data-testid="nested-modal-opener" onclick={() => (nestedOpen = true)}>Open nested dialog</button>

    {#if nestedOpen}
      <Modal title="Nested confirmation" onClose={closeNested}>
        <button type="button" disabled>Disabled nested action</button>
        <input aria-label="Nested choice" />
        <button type="button" data-testid="nested-modal-last" onclick={closeNested}>Confirm nested</button>
      </Modal>
    {/if}
    <input aria-label="Disabled input" disabled />
    <input aria-label="Hidden input" type="hidden" />
    <button type="button" hidden>Hidden action</button>
    <span aria-hidden="true"><button type="button">Aria hidden action</button></span>
    <button type="button" tabindex="-2">Negative tabindex action</button>
  </Modal>
{/if}
