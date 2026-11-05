import { getContext } from "svelte";
export const fieldContextKey = Symbol("teamva-ui-field");
export interface FieldContext { readonly controlId: string; readonly labelId: string; readonly descriptionId?: string }
export function currentField(): FieldContext | undefined { return getContext<FieldContext | undefined>(fieldContextKey); }
